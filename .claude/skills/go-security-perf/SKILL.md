---
name: go-security-perf
description: Go security, performance and benchmarking. TRIGGER when Go code runs external commands, handles untrusted input or secrets, fetches URLs or sets timeouts, or when measuring, profiling or speeding up code.
---

# Go Security and Performance

Security, performance, and benchmarking for any Go program: services, CLIs and libraries.

## Security

### Security Thinking Model

Before writing or reviewing code:
1. **What are the trust boundaries?** — Where does untrusted data enter?
2. **What can an attacker control?** — Which inputs flow into sensitive operations?
3. **What is the blast radius?** — If this defense fails, what's the worst outcome?

### Quick Reference

| Severity | Vulnerability | Defense | Stdlib Solution |
| --- | --- | --- | --- |
| Critical | Command Injection | Pass args separately | `exec.Command` with separate args |
| High | XSS | Auto-escaping | `html/template` |
| High | SSRF | Check the IP being dialed, after DNS | `net.Dialer.Control` + `net/netip` (below) |
| Medium | Slow requests (DoS) | Server timeouts | `http.Server.ReadHeaderTimeout` |
| High | Crypto Issues | Vetted algorithms | `crypto/aes` GCM |
| Medium | Rate Limiting | Rate limits | `golang.org/x/time/rate`, server timeouts |

### SSRF — Fetching User-Supplied URLs

Checking the URL's host is not enough: a public name that resolves to `127.0.0.1` (`127.0.0.1.nip.io`) or a redirect to an internal address passes any string check. Reject the address at dial time — `Control` sees the resolved IP for every connection, redirects included. Allow-list (global unicast, not private) rather than deny-list the `Is*` checks: a deny-list lets CGNAT, 0/8, 240/4 and NAT64/6to4 addresses that tunnel to internal IPv4 through:

```go
var specialUse = []netip.Prefix{ // special-use ranges IsGlobalUnicast lets through
    netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"), // this-network, CGNAT
    netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("240.0.0.0/4"), // benchmarking, reserved
    netip.MustParsePrefix("::/96"), netip.MustParsePrefix("2002::/16"), // v4-compatible, 6to4
    netip.MustParsePrefix("64:ff9b::/96"), netip.MustParsePrefix("64:ff9b:1::/48"), // NAT64
}

func ssrfControl(_, address string, _ syscall.RawConn) error {
    ap, err := netip.ParseAddrPort(address) // the resolved IP, after DNS
    if err != nil { return err }
    ip := ap.Addr().Unmap().WithZone("") // a zoned address never matches Prefix.Contains
    ok := ip.IsGlobalUnicast() && !ip.IsPrivate()
    for _, p := range specialUse {
        ok = ok && !p.Contains(ip)
    }
    if !ok { return fmt.Errorf("ssrf: blocked %s", ap) }
    return nil
}

t := http.DefaultTransport.(*http.Transport).Clone()
t.Proxy = nil // through a proxy the dialer would check the proxy, not the target
t.DialContext = (&net.Dialer{Timeout: 5 * time.Second, Control: ssrfControl}).DialContext
client := &http.Client{Timeout: 10 * time.Second, Transport: t}
```

`IsGlobalUnicast` already excludes loopback, link-local (cloud metadata `169.254.169.254`), multicast and unspecified. The list is the IANA special-purpose registry entries that matter for dialing; add your own network's internal public ranges.

### Research Before Reporting

Trace the full data flow — don't assess in isolation. Check upstream validation, trust boundaries, surrounding middleware. Adjust severity based on defense layers. Add inline comments: `// security: safe here — input validated by parseID() which returns int`.

### Security Tooling

```bash
go tool gosec ./...        # SAST scanner — pinned via `go get -tool github.com/securego/gosec/v2/cmd/gosec@latest`
go tool govulncheck ./...  # known vulnerabilities — `go get -tool golang.org/x/vuln/cmd/govulncheck@latest`
go test -race ./...        # race detector
go test -fuzz=FuzzParse ./pkg/parser  # fuzz testing — one fuzz test, one package
```

### Common Security Mistakes

| Severity | Mistake | Fix |
| --- | --- | --- |
| Critical | `exec.Command("bash", "-c", userInput)` | Pass args separately |
| Critical | Hardcoded secrets | Env vars or secret managers |
| Medium | `&http.Server{}` without `ReadHeaderTimeout`, or `http.ListenAndServe` (no timeouts at all) | Set `ReadHeaderTimeout` — a client trickling one header byte at a time holds the connection forever (gosec G112/G114) |
| High | MD5/SHA1 for passwords | Argon2id or bcrypt |
| High | AES without GCM | GCM provides encrypt+authenticate; `cipher.NewGCMWithRandomNonce` (Go 1.24+) generates and prepends a random nonce, so the caller never manages nonces (limit: 2^32 messages per key) |

### Security Anti-Patterns

| Anti-Pattern | Fix |
| --- | --- |
| Security through obscurity | Auth + authz on all endpoints |
| Trusting client headers (`X-Forwarded-For`) | Server-side identity verification |
| Client-side authorization | Server-side permission checks |
| Shared secrets across environments | Per-environment secrets |
| Ignoring crypto errors | Always check — fail closed |
| Rolling your own crypto | Use stdlib `crypto/*` |

---

## Performance

### Core Philosophy

1. **Profile before optimizing** — intuition about hot spots is often wrong
2. **Allocation reduction yields the biggest ROI** — Go's GC is fast but not free
3. **Rule out external bottlenecks first** — if 90% of latency is a slow DB query, reducing allocations won't help
4. **One change at a time** — measure, change, re-measure

### Optimization Methodology

1. Define metric (latency, throughput, memory, CPU)
2. Write benchmark: `go test -bench=BenchmarkMyFunc -benchmem -count=10 ./... | tee /tmp/report-1.txt`
3. Diagnose with pprof
4. Apply ONE optimization
5. Compare: `benchstat /tmp/report-1.txt /tmp/report-2.txt`
6. Repeat

Reading and recording results: `~` in the delta column means no significant change — don't claim one. Compare runs from the same toolchain and machine, and never benchmark while other heavy jobs (builds, other agents) share the CPU. Paste the benchstat table into the body of the `perf(...)` commit.

### Decision Tree

| Bottleneck | Signal (pprof) | Action |
| --- | --- | --- |
| Too many allocations | `alloc_objects` high | Reduce allocations, preallocate, sync.Pool |
| CPU-bound hot loop | Function dominates CPU profile | Optimize algorithm, reduce work |
| GC CPU / OOM kills | High GC CPU share, container limits | GC cost is CPU, not pauses (pauses average tens of µs). `GOMEMLIMIT` at 80-90% of the container limit makes the GC collect before garbage pushes the process over it. It is a soft limit: a live heap above it grows past it anyway, with the GC burning CPU all the while, and is OOM-killed at the container limit; cgo and `syscall.Mmap` memory are not counted. To cut GC CPU, raise `GOGC` (or `GOGC=off` together with `GOMEMLIMIT`) |
| Network / I/O latency | Goroutines blocked | Connection pools, circuit breakers |
| Repeated expensive work | Same computation multiple times | `singleflight`, caching |
| Lock contention | Mutex/block profile hot | Reduce critical sections, sharding |

### HTTP Transport Configuration

Default `http.Client` has no timeout and keeps only 2 idle connections per host. Clone `DefaultTransport` rather than starting from `&http.Transport{}` — a bare Transport drops proxy-from-environment and the dial and TLS-handshake timeouts:

```go
t := http.DefaultTransport.(*http.Transport).Clone()
t.MaxIdleConnsPerHost = 10 // default is 2 — too low for concurrent calls to one host
client := &http.Client{Timeout: 30 * time.Second, Transport: t}
```

**Always close the response body**, even on error status. Connection reuse needs the body read to EOF: Go 1.27+ drains it on `Close` (up to a limit); before 1.27 — or for a large unread body — drain it yourself:

```go
defer func() {
    _, _ = io.Copy(io.Discard, resp.Body) // drain what the caller didn't read
    _ = resp.Body.Close()
}()
```

### Streaming JSON

Process large JSON arrays one item at a time instead of loading all into memory:

```go
dec := json.NewDecoder(resp.Body)
if _, err := dec.Token(); err != nil { // read opening [
    return err
}
for dec.More() {
    var item Item
    if err := dec.Decode(&item); err != nil { return err }
    process(item)
}
```

### Common Performance Mistakes

| Mistake | Fix |
| --- | --- |
| Optimizing without profiling | Profile with pprof first |
| Default `http.Client` without Transport config | Set `MaxIdleConnsPerHost` to match concurrency |
| No GC tuning in containers | `GOMEMLIMIT` = 80-90% of container memory |
| `reflect.DeepEqual` in production | `slices.Equal`, `maps.Equal` — type-safe, no reflection, no allocations, typically 5–100x faster; `bytes.Equal` for `[]byte` |
| `panic`/`recover` as control flow | Use error returns |

---

## Benchmarking

### Running Benchmarks

```bash
go test -bench=. -benchmem -count=10 ./... | tee bench.txt
```

| Flag | Purpose |
| --- | --- |
| `-bench=.` | Run all benchmarks |
| `-benchmem` | Report allocations |
| `-count=10` | Run 10 times for statistical significance |
| `-benchtime=3s` | Minimum time per benchmark |
| `-cpuprofile=cpu.prof` | Write CPU profile |
| `-memprofile=mem.prof` | Write memory profile |

### Profiling from Benchmarks

```bash
go test -bench=BenchmarkParse -cpuprofile=cpu.prof ./pkg/parser
go tool pprof cpu.prof

go test -bench=BenchmarkParse -memprofile=mem.prof ./pkg/parser
go tool pprof -alloc_objects mem.prof  # GC churn
go tool pprof -inuse_space mem.prof    # leaks
```

---

## Common Mistakes

| Category | Mistake | Fix |
| --- | --- | --- |
| Security | `exec.Command("bash", "-c", input)` | Pass args separately |
| Security | Hardcoded secrets | Env vars or secret manager |
| Performance | Optimizing without profiling | pprof first |
| Performance | No `GOMEMLIMIT` in containers | Set to 80-90% of container memory |
