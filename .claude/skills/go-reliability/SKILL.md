---
name: go-reliability
description: Go error handling, safety, concurrency, context, testing, and troubleshooting. TRIGGER when writing Go code that involves error handling, goroutines, channels, context propagation, or tests.
---

# Go Reliability

Error handling, safety, concurrency, context, testing, and troubleshooting for robust Go code.

## Error Handling

### Core Rules

1. Returned errors MUST always be checked — NEVER discard with `_`
2. Errors MUST be wrapped with context: `fmt.Errorf("{context}: %w", err)`
3. Error strings MUST be lowercase, without trailing punctuation
4. Use `%w` internally, `%v` at system boundaries to control chain exposure
5. Extract typed errors with `errors.AsType[T]` (Go 1.26+; `errors.As` before) — never a type assertion on a returned error
6. Errors MUST be either logged OR returned, NEVER both (single handling rule)
7. NEVER use `panic` for expected error conditions

### Error Creation

```go
// Sentinel errors — for expected conditions callers need to match
var ErrNotFound = errors.New("not found")
var ErrUnauthorized = errors.New("unauthorized")

// Custom error types — when callers need structured data
type ValidationError struct {
    Field   string
    Message string
}
func (e *ValidationError) Error() string {
    return fmt.Sprintf("validation failed on %s: %s", e.Field, e.Message)
}

// Custom types that wrap other errors — implement Unwrap()
type QueryError struct {
    Query string
    Err   error
}
func (e *QueryError) Error() string   { return fmt.Sprintf("query %q: %v", e.Query, e.Err) }
func (e *QueryError) Unwrap() error   { return e.Err }
```

| Situation | Strategy |
| --- | --- |
| Caller needs to match a specific condition | Sentinel error (`errors.New` as package var) |
| Caller needs to extract structured data | Custom error type |
| Error is purely informational | `fmt.Errorf` or `errors.New` |

### Error Wrapping

Wrap at each layer to build a readable chain: `creating order: charging card: connection refused`

```go
// Internal — wrap to preserve chain
return fmt.Errorf("querying database: %w", err)

// Public API boundary — break chain to hide internals
return fmt.Errorf("item unavailable: %v", err)  // %v — callers cannot unwrap
```

### Error Inspection

```go
// errors.Is — match sentinel values (traverses entire chain)
if errors.Is(err, sql.ErrNoRows) {
    return nil, ErrNotFound
}

// errors.AsType — extract a typed error from the chain (Go 1.26+)
if ve, ok := errors.AsType[*ValidationError](err); ok {
    log.Printf("validation failed on field %s", ve.Field)
}
// before Go 1.26: var ve *ValidationError; if errors.As(err, &ve) { ... }

// errors.Join — combine independent errors (Go 1.20+)
func validateUser(u User) error {
    var errs []error
    if u.Name == "" { errs = append(errs, errors.New("name is required")) }
    if u.Email == "" { errs = append(errs, errors.New("email is required")) }
    return errors.Join(errs...)  // nil if empty; works with errors.Is/As
}
```

### Low-Cardinality Log Messages

Log and APM tools group by message. Where an error is logged, keep the message static and attach variable data (IDs, paths, counts) as structured attributes. Returned errors may carry operational context; static wrap prefixes are always fine.

```go
// Bad — every user/path combination becomes its own log group
slog.Error(fmt.Sprintf("user %s not found in %s", userID, path))

// Good — static message, variable data as attributes
slog.Error("user lookup failed", "error", err, "user_id", userID, "path", path)
```

---

## Safety

### Nil Safety

**Nil behavior table:**

| Type | Read from nil | Write to nil | Len/Cap | Range |
| --- | --- | --- | --- | --- |
| Map | Zero value | **panic** | len 0 (`cap` does not compile on maps) | 0 iterations |
| Slice | **panic** (index) | **panic** (index) | 0 | 0 iterations |
| Channel | Blocks forever | Blocks forever | 0 | Blocks forever |

**Nil pointer receivers** — a method call on nil doesn't always panic (only if it dereferences the receiver). But NEVER rely on this — always check for nil.

**Nil function values** — calling a nil `func` panics. Provide no-op defaults:

```go
func NewWorker(opts ...Option) *Worker {
    return &Worker{onComplete: func(string) {}}  // no-op default
}
```

### Slice & Map Safety

**Append aliasing** — `append` reuses backing array if capacity allows:

```go
a := make([]int, 3, 5)
b := append(a, 4)
b[0] = 99  // also modifies a[0]!

// Fix — full slice expression forces new allocation
c := append(a[:len(a):len(a)], 4)
c[0] = 99  // a is untouched
```

**Subslice retains full backing array** — use `slices.Clone()` to detach small slices from large arrays.

**Defensive copies** — exported functions returning slices/maps SHOULD return copies:

```go
type Config struct { hosts []string }
func (c *Config) Hosts() []string { return slices.Clone(c.hosts) }
```

**Range loop variable capture** — pre-Go 1.22 all closures see the last value. Go 1.22+ fixed this with per-iteration scoping. Check your `go.mod` version.

**Deleting during iteration** — maps: safe. Slices: use `slices.DeleteFunc` or iterate backwards.

**Comparing slices/maps** — use `slices.Equal`/`maps.Equal` (Go 1.21+), never `==`.

### Numeric Safety

```go
// Float comparison — on float64 values (untyped constants are exact: 0.1+0.2 == 0.3 is true)
a, b := 0.1, 0.2
fmt.Println(a+b == 0.3)                  // false
fmt.Println(math.Abs((a+b)-0.3) < 1e-9)  // true

// Integer division by zero panics — always guard
```

### Variable Shadowing

`:=` inside an inner scope creates a NEW variable — the outer one stays unchanged:

```go
// Bug — inner err shadows outer err, outer stays nil
var err error
if condition {
    val, err := doSomething()  // new err! outer err is still nil
    if err != nil {
        log.Print(err)
    }
    process(val)
}
return err  // always nil — the real error was in the inner scope

// Fix — use = so the outer err is set, and skip val on error
var err error
if condition {
    var val int
    val, err = doSomething()  // assigns to outer err
    if err == nil {
        process(val)
    }
}
return err
```

Detect with the `shadow` analyzer (`golang.org/x/tools/go/analysis/passes/shadow`) — in golangci-lint, `govet` with `enable: [shadow]`. `go vet` has no shadow check. This is one of the most common Go bugs.

### Resource Safety

Defer arguments are evaluated when the `defer` statement runs, not at function exit:

```go
// Gotcha — x is evaluated when defer runs
x := 1
defer fmt.Println(x)  // prints 1, not 2
x = 2

// Fix — a closure reads x at function exit
defer func() { fmt.Println(x) }()  // prints 2
```

### Initialization Safety

Use `sync.Once` for lazy init under concurrency:

```go
type DB struct {
    once sync.Once
    conn *sql.DB
    err  error
}
func (db *DB) connection() (*sql.DB, error) {
    db.once.Do(func() { db.conn, db.err = sql.Open("postgres", connStr) })
    return db.conn, db.err
}
```

Go 1.21+: `sync.OnceValue` / `sync.OnceValues` for simpler cases:

```go
var loadConfig = sync.OnceValues(func() (*Config, error) {
    return parseConfig("config.yaml")
})

cfg, err := loadConfig()
```

The result is cached, an error included — a failed init stays failed. Use a mutex when the init must be retryable.

---

## Concurrency

### Core Principles

1. Every goroutine MUST have a clear exit — context, done channel, or WaitGroup
2. Send copies, not pointers on channels
3. Specify channel direction (`chan<-`, `<-chan`) in function signatures
4. Default to unbuffered channels — buffers mask backpressure
5. In long-running loops, reuse one `time.NewTimer` + `Reset` instead of `time.After` per iteration — each `time.After` call allocates a timer. It no longer leaks: an unreferenced timer is collected (Go 1.23–1.26 toolchains: only when go.mod says `go 1.23`+; always from 1.27)
6. Track goroutine leaks in tests with `go.uber.org/goleak`

### Goroutine Lifecycle

```go
// Bad — fire-and-forget, leaks on shutdown
go func() { for { doWork(ctx) } }()

// Good — respects cancellation, caller can wait
func startWorker(ctx context.Context) *sync.WaitGroup {
    var wg sync.WaitGroup
    wg.Add(1)
    go func() {
        defer wg.Done()
        for {
            select {
            case <-ctx.Done():
                return
            default:
                doWork(ctx)
            }
        }
    }()
    return &wg
}
```

**Panic recovery at goroutine boundaries** — a panic in a goroutine crashes the entire process:

```go
go func() {
    defer func() {
        if r := recover(); r != nil { /* log */ }
    }()
    doWork(ctx)
}()
```

### Channel vs Mutex vs Atomic

| Scenario | Use |
| --- | --- |
| Passing data between goroutines | Channel |
| Coordinating goroutine lifecycle | Channel + context |
| Protecting shared struct fields | `sync.Mutex` / `sync.RWMutex` |
| Simple counters, flags | `sync/atomic` (typed: `atomic.Int64`, `atomic.Bool`) |
| Read-heavy concurrent map | `sync.Map` |
| Compute once / dedupe concurrent identical calls | `sync.Once` / `singleflight` (neither is a cache) |

### Channel Patterns

**Buffer sizes:**

| Size | When |
| --- | --- |
| 0 (unbuffered) | Default. Synchronizes sender and receiver |
| 1 | Signal channels, sender must not block on single pending item |
| N > 1 | Only with measured justification — document why |

**Timer reuse in loops:**

```go
// Allocates a new timer every iteration
case <-time.After(5 * time.Second):

// Reuses one timer
timer := time.NewTimer(5 * time.Second)
defer timer.Stop()
// ... timer.Reset(5 * time.Second) after each use — Go 1.23+: no drain before Reset
```

### Sync Primitives

| Primitive | Use case | Key notes |
| --- | --- | --- |
| `sync.Mutex` | Protect shared state | Keep critical sections short; never hold across I/O |
| `sync.RWMutex` | Many readers, few writers | Never upgrade RLock to Lock (deadlock) |
| `sync/atomic` | Simple counters, flags | Go 1.19+: typed `atomic.Int64`, `atomic.Bool` |
| `sync.Map` | Concurrent map, read-heavy | No caller-side locking; use `RWMutex`+map when writes dominate |
| `sync.Pool` | Reuse temporary objects | Always `Reset()` before `Put()`; GC can reclaim anytime |
| `sync.Once` | One-time initialization | Go 1.21+: `OnceFunc`, `OnceValue`, `OnceValues` |
| `sync.WaitGroup` | Wait for goroutines | No error result — use `errgroup` for that |
| `singleflight` | Deduplicate concurrent calls | Cache stampede prevention. The shared call runs with the first caller's ctx, so its cancellation fails every waiter: detach with `context.WithoutCancel` |
| `errgroup` | Goroutine group + errors | `SetLimit(n)` replaces hand-rolled worker pools |

**Mutex example:**

```go
type SafeCache struct {
    mu    sync.Mutex  // protects items
    items map[string]string
}
func (c *SafeCache) Get(key string) (string, bool) {
    c.mu.Lock()
    defer c.mu.Unlock()
    v, ok := c.items[key]
    return v, ok
}
```

### Concurrency Checklist

Before spawning a goroutine:
- How will it exit? (context, channel close, signal)
- Can I signal it to stop? (`context.Context` or done channel)
- Can I wait for it? (`WaitGroup` or `errgroup`)
- Should this be synchronous instead?

---

## Context

### Core Rules

1. `ctx` MUST be the first parameter, named `ctx context.Context`
2. NEVER store context in a struct — pass through function parameters
3. NEVER pass `nil` context — use `context.TODO()` if unsure
4. `cancel()` MUST run on every path — `defer cancel()` right after creation, unless the ctx/cancel pair is returned or handed to an owner that calls it (e.g. a `Stop()` method)
5. Context values MUST only carry request-scoped metadata, NEVER function parameters
6. Context value keys MUST be unexported types

### Creating Contexts

| Situation | Use |
| --- | --- |
| Entry point | `context.Background()` |
| Placeholder | `context.TODO()` |
| HTTP handler | `r.Context()` |
| Cancellation | `context.WithCancel(parentCtx)` |
| Timeout | `context.WithTimeout(parentCtx, duration)` |
| Absolute deadline | `context.WithDeadline(parentCtx, time)` |

### Cancellation and Timeouts

Every `WithCancel`/`WithTimeout`/`WithDeadline` allocates resources — always `defer cancel()`:

```go
ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
defer cancel()
return doWork(ctx)
```

Nested timeouts take the shorter deadline — a child with 10s timeout under a parent with 2s still expires at 2s.

**Listening for cancellation:**

```go
select {
case <-ctx.Done():
    return ctx.Err()  // context.Canceled or context.DeadlineExceeded
case <-ticker.C:
    doWork(ctx)
}
```

For CPU-bound loops, periodically check `ctx.Err()`.

### Advanced Context Patterns

**`context.AfterFunc`** (Go 1.21+) — registers cleanup callback that runs when context is cancelled.

---

## Testing

### Core Rules

1. Table-driven tests MUST use named subtests via `t.Run`
2. Integration tests MUST use build tags (`//go:build integration`)
3. Tests MUST NOT depend on execution order
4. NEVER test implementation details — test observable behavior
5. Mock interfaces, not concrete types
6. Keep unit tests fast (< 1ms)
7. Run tests with `-race` in CI

### Table-Driven Tests

```go
func TestCalculatePrice(t *testing.T) {
    tests := []struct {
        name     string
        quantity int
        price    float64
        expected float64
    }{
        {"single item", 1, 10.0, 10.0},
        {"bulk discount", 100, 10.0, 900.0},
        {"zero quantity", 0, 10.0, 0.0},
    }
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            got := CalculatePrice(tt.quantity, tt.price)
            if got != tt.expected {
                t.Errorf("got %.2f, want %.2f", got, tt.expected)
            }
        })
    }
}
```

Anything that captures `t` — a helper struct, testify's `assert.New(t)` / `require.New(t)` — MUST be built inside the `t.Run` closure from the subtest's `t`. Built from the parent `t`, an `assert` failure is reported against the parent while the subtest shows PASS, and a `require` failure calls the parent's `FailNow` from the subtest ("subtest may have called FailNow on a parent test").

### Test File Conventions

Name a test file after the source file it tests: `order.go` → `order_test.go`. Never name it after a function.

```go
package mypackage      // white-box, access unexported
package mypackage_test // black-box, public API only
```

### HTTP in a `testing/synctest` bubble

Use `httptest.NewTestServer(t, h)` (Go 1.27+, in-memory network) with `srv.Client()`. `httptest.NewServer` inside the bubble hangs: a goroutine blocked on socket I/O is not durably blocked, so the fake clock stops.

### Goroutine Leak Detection

```go
func TestMain(m *testing.M) {
    goleak.VerifyTestMain(m)
}
```

### Integration Tests

```go
//go:build integration

func TestDatabaseIntegration(t *testing.T) {
    db, err := sql.Open("postgres", os.Getenv("DATABASE_URL"))
    if err != nil { t.Fatal(err) }
    defer db.Close()
}
```

Run separately: `go test -tags=integration ./...`

### Parallel Tests

```go
t.Run(tt.name, func(t *testing.T) {
    t.Parallel()
    // ...
})
```

### Fuzzing

```go
func FuzzReverse(f *testing.F) {
    f.Add("hello")
    f.Fuzz(func(t *testing.T, input string) {
        if Reverse(Reverse(input)) != input {
            t.Errorf("double reverse failed for %q", input)
        }
    })
}
```

### Debugging Flaky Tests

Common causes: shared mutable state, test order dependence, timing sensitivity, port conflicts. Diagnostic flags:

```bash
go test -shuffle=on ./...     # randomize order — catch order-dependent tests
go test -count=100 ./...      # repeat to find intermittent failures
go test -p 1 -parallel 1 ./... # serialize packages and tests — isolate concurrency issues
```

### Quick Reference

```bash
go test ./...                       # all tests
go test -run TestName ./...         # specific test
go test -run TestName/subtest ./... # subtest
go test -race ./...                 # race detection
go test -cover ./...                # coverage
go test -bench=. -benchmem ./...    # benchmarks
go test -fuzz=FuzzName ./pkg/foo    # fuzzing — one package only
go test -tags=integration ./...     # integration tests
go test -count=10 ./...             # repeat (find flaky tests)
```

---

## Troubleshooting

### Golden Rules

1. **Read the error message first** — file/line, type mismatch, "undefined", "cannot use X as Y"
2. **Reproduce before you fix** — write a failing test, make it deterministic, use `git bisect`
3. **One hypothesis at a time** — change one thing, measure, confirm
4. **Find the root cause** — no band-aid fixes. Trace data flow backwards, question assumptions, ask "why" five times
5. **Research the codebase** — trace callers, check upstream validation, read surrounding code
6. **Start simple** — `fmt.Println` first, escalate to pprof/Delve only when needed
7. **Never propose a fix you cannot explain**

### Decision Tree

| Symptom | First step |
| --- | --- |
| Build won't compile | `go build ./... 2>&1`, `go vet ./...` |
| Wrong output / logic bug | Write a failing test |
| Random crashes / panics | `GOTRACEBACK=all ./app`, `go test -race` |
| Sometimes works, sometimes fails | `go test -race ./...` |
| Program hangs | `curl localhost:6060/debug/pprof/goroutine?debug=2` |
| Goroutine count keeps growing | `curl localhost:6060/debug/pprof/goroutineleak?debug=1` (Go 1.27+) |
| High CPU | pprof CPU profiling |
| Memory growing | pprof heap profiling |
| Slow / high latency | CPU + mutex + block profiles — mutex/block stay empty unless `runtime.SetMutexProfileFraction` / `runtime.SetBlockProfileRate` is set |
| `go test` fails in vet `stdversion` (Go 1.27+) | The code uses an API newer than go.mod's `go` line — raise the line or avoid the API |

Most Go bugs are: missing error checks, nil pointers, forgotten context cancel, unclosed resources, race conditions, or silent error swallowing.

### Red Flags — You're Debugging Wrong

- "Quick fix for now, investigate later" — there is no later
- Multiple simultaneous changes — one hypothesis at a time
- Proposing fixes without understanding the cause
- Each fix reveals a new problem — real bug is elsewhere
- 3+ attempts on the same issue — wrong mental model, start over
- Blaming the framework/stdlib — it's almost never a Go bug

### HTTP Client Rules

1. Always `defer resp.Body.Close()` after checking err — even on error status
2. Connection reuse needs the body read to EOF: Go 1.27+ drains it on `Close` (up to a limit); before 1.27, `io.Copy(io.Discard, resp.Body)` before closing

### pprof in Production

Enable via environment variable. Bind to localhost and reach it through an SSH tunnel or port-forward — never expose it unauthenticated. Capture through the tunnel (`ssh -L 6060:localhost:6060 host`): `go tool pprof http://localhost:6060/debug/pprof/heap`

### Code Review Red Flags

Watch for these patterns — each signals a potential bug:
- Error discarded with `_` or unchecked
- `go func()` without context or shutdown mechanism
- No `defer close` after opening a resource
- `time.After` inside a hot loop (allocates a timer per iteration)
- `:=` inside if/for that shadows an outer variable
- Named return + defer interaction (confusing control flow)
- `reflect.DeepEqual` in production code (much slower than a typed comparison, and allocates)

---

## Common Mistakes

| Mistake | Fix |
| --- | --- |
| Discarding errors with `_` | Always check returned errors |
| Log AND return error | Either log OR return, never both |
| Bare type assertion `v := x.(T)` | Use `v, ok := x.(T)` |
| Writing to nil map | Initialize with `make()` or lazy-init |
| `append` assuming it always copies | Use `s[:len(s):len(s)]` to force new allocation |
| Fire-and-forget goroutine | Provide stop mechanism (context, done channel) |
| `time.After` in a hot loop | Reuse one `time.NewTimer` + `Reset` |
| Storing context in a struct | Pass through function parameters |
| Forgetting `cancel()` | `defer cancel()` right after WithCancel/WithTimeout, unless a `Stop()`/owner takes it over (`go vet` lostcancel flags a cancel that is discarded or missed on a return path) |
| Variable shadowing with `:=` | Pre-declare variables, use `=` not `:=` in inner scopes |
