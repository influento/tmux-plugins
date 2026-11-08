---
name: go-tooling
description: Go linting (golangci-lint), CLI patterns, dependency management, Go version modernization, and CI/CD. TRIGGER when setting up Go projects, configuring linters, writing CLI tools, or managing dependencies.
---

# Go Tooling

Linting, CLI patterns, dependency management, Go version modernization, and CI/CD.

## Linting (golangci-lint)

`golangci-lint` is the standard Go linting tool — aggregates 100+ linters into a single binary. Every Go project MUST have a `.golangci.yml`.

### Quick Reference

```bash
golangci-lint run ./...           # run all configured linters
golangci-lint run --fix ./...     # auto-fix where possible
golangci-lint fmt ./...           # format code (v2+)
golangci-lint run --enable-only govet ./...  # single linter
golangci-lint linters             # list available linters
```

### Recommended Linter Categories

| Domain | Linters | Catches |
| --- | --- | --- |
| Correctness | govet, staticcheck, unused, errcheck, errorlint, nilerr, forcetypeassert, copyloopvar, exhaustive | Bugs, unchecked errors, `==` on wrapped errors, non-exhaustive enum switches, stdlib misuse |
| Style | gocritic, revive, wsl_v5, whitespace, godot, misspell, predeclared, errname, sloglint | Readability, naming, consistency |
| Complexity | gocyclo, nestif, funlen, dupl | Overly complex or duplicated code |
| Performance | perfsprint, unconvert, ineffassign, goconst | Unnecessary conversions, dead assigns |
| Security & resources | gosec, bodyclose, sqlclosecheck, rowserrcheck, noctx, containedctx, fatcontext | Vulnerabilities, leaks (HTTP, SQL), context misuse |
| Testing | thelper, paralleltest, testifylint, usetesting | Test hygiene |
| Modernization | modernize, exptostd, intrange, usestdlibvars, nolintlint | Modern Go idioms, lint hygiene |

### Key Linters Explained

- **errcheck** — ensures all error returns are checked. It flags `defer f.Close()` too: either write `defer func() { _ = f.Close() }()` or set `linters.exclusions.presets: [std-error-handling]`
- **forcetypeassert** — flags `v := x.(T)` without comma-ok
- **nilerr** — detects returning nil error when `err` is non-nil
- **bodyclose** — ensures HTTP response bodies are closed
- **sqlclosecheck** — ensures `sql.Rows` and `sql.Stmt` are closed. It misses pgx rows entirely
- **rowserrcheck** — ensures `sql.Rows.Err()` is checked after iteration. On pgx (`pgx.Conn` and `pgxpool.Pool`) only with `rowserrcheck.packages: [github.com/jackc/pgx/v5]`. With pgx, prefer `pgx.CollectRows` / `pgx.ForEachRow`: they close the rows and return `rows.Err()`, so neither linter is needed
- **modernize** — detects code rewritable with newer Go features (golangci-lint v2.6.0+)
- **nolintlint** — enforces `//nolint` directives specify linter name + justification

### Suppressing Lint Warnings

```go
//nolint:errcheck // fire-and-forget logging, error is not actionable
_ = logger.Sync()
```

Rules:
1. MUST specify the linter name: `//nolint:errcheck` not `//nolint`
2. MUST include a justification comment
3. NEVER suppress security linters without strong reason

**When to fix vs suppress:**

| Fix always | OK to suppress with justification |
| --- | --- |
| errcheck, govet, staticcheck, gosec, bodyclose, sqlclosecheck | funlen (orchestration functions), gocyclo (complex but clear) |
| nilerr, forcetypeassert, rowserrcheck | dupl (intentional parallel structure), goconst (context-specific) |

### Recommended Config Thresholds

Key settings for `.golangci.yml`:
- `gocyclo` max complexity: 13
- `funlen` max lines: 120, max statements: 80
- `dupl` threshold: 100 tokens
- `goconst` min length: 3 chars, min occurrences: 4
- `nolintlint` with `require-explanation: true`, `require-specific: true`
- `rowserrcheck` with `packages: [github.com/jackc/pgx/v5]` on pgx projects
- `formatters.settings.gofumpt.extra: {group-params: true, clothe-returns: true}` (golangci-lint v2.13+; the older `extra-rules: true` is deprecated)

### Makefile Targets

```makefile
lint:
	golangci-lint run ./...
lint-fix:
	golangci-lint run --fix ./...
fmt:
	golangci-lint fmt ./...
```

### Workflow

1. Run after every significant change: `golangci-lint run ./...`
2. Auto-fix: `golangci-lint run --fix ./...`
3. Incremental adoption: set `issues.new-from-rev` to lint only new code

### Common Issues

| Problem | Solution |
| --- | --- |
| Too many issues on legacy code | `issues.new-from-rev: HEAD~1` |

---

## CLI Patterns (Cobra + Viper)

Use Cobra + Viper as the default stack. Cobra provides command/subcommand/flag structure, Viper handles config from files, env vars, and flags.

### Project Structure

```
cmd/myapp/
    main.go       # package main, only calls Execute()
    root.go       # root command + Viper init
    serve.go      # "serve" subcommand
    version.go    # "version" subcommand
```

### Root Command

- `PersistentPreRunE` for config initialization before any subcommand

### Viper Config Precedence

1. Explicit `viper.Set` (highest)
2. CLI flags — only when set on the command line; an unset flag's default ranks below `viper.SetDefault`
3. Environment variables
4. Config file
5. Defaults (lowest)

Always bind flags to Viper: `viper.BindPFlag("port", cmd.Flags().Lookup("port"))`. Use `viper.SetEnvPrefix("MYAPP")` to namespace env vars.

### Argument Validation

| Validator | Description |
| --- | --- |
| `cobra.NoArgs` | Fails if any args |
| `cobra.ExactArgs(n)` | Requires exactly n |
| `cobra.MinimumNArgs(n)` | At least n |
| `cobra.RangeArgs(min, max)` | Between min and max |

### Exit Codes

| Code | Meaning |
| --- | --- |
| 0 | Success |
| 1 | General error |
| 2 | Usage error |
| 128+N | Terminated by signal N |

### I/O Rules

- Use `cmd.OutOrStdout()` / `cmd.ErrOrStderr()` so tests can capture output
- Support `--output` flag (json/table/plain) for machine-readable output

### Common CLI Mistakes

| Mistake | Fix |
| --- | --- |
| `os.Stdout` directly | Use `cmd.OutOrStdout()` for testability |
| `os.Exit()` inside `RunE` | Deferred functions and cleanup never run, and Cobra's error handling is skipped. Return the error, let `main()` decide |
| Flags not bound to Viper | Call `viper.BindPFlag` for every flag |
| Missing `viper.SetEnvPrefix` | Namespace env vars to avoid collisions |
| `AutomaticEnv` with a nested key | `db.host` looks up `MYAPP_DB.HOST`, so `MYAPP_DB_HOST` is never read. Set `viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_", "-", "_"))` |

---

## Dependency Management

### AI Agent Rule

Before adding ANY new dependency, agents MUST ask the user for confirmation. Present: package name, purpose, stdlib alternative, GitHub stars, license, alternatives.

### Key Rules

- `go.sum` MUST be committed — cryptographic checksums detect supply-chain tampering
- `govulncheck ./...` before every release
- `go mod tidy` before every commit that changes dependencies
- Prefer `go get -u=patch` for routine updates (safer than `-u`)

### Essential Commands

| Command | Purpose |
| --- | --- |
| `go mod tidy` | Add missing, remove unused |
| `go mod verify` | Verify checksums |
| `go mod why -m pkg` | Why is this dep needed? |
| `govulncheck ./...` | Vulnerability scan |
| `go get pkg@none` | Remove dependency |
| `go get -u=patch ./...` | Patch-level upgrades |

### Pinning Tools (Go 1.24+ `tool` directive)

Pin tool versions in `go.mod` instead of a `tools.go` file:

```bash
go get -tool golang.org/x/vuln/cmd/govulncheck@latest  # adds a `tool` line to go.mod
go tool govulncheck ./...                              # runs the pinned version
go get -u tool                                          # updates every pinned tool
go install tool                                         # installs them all into GOBIN
```

Keep a `//go:build tools` file with blank imports only for modules that must still build with Go < 1.24.

golangci-lint is the exception: its docs recommend the release binary and advise against `go install`, the tools pattern and `go tool` — partly because its dependencies merge with yours. If you must pin it, isolate it in its own modfile:

```bash
go mod init -modfile=tools/go.mod example.com/myapp/tools
go get -tool -modfile=tools/go.mod github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0
go tool -modfile=tools/go.mod golangci-lint run
```

---

## Go Modernization

Use modern Go features. Check `go.mod` version to determine available features.

### Prefer Instead

| Old | Replacement | Since |
| --- | --- | --- |
| `math/rand` | `math/rand/v2` | Go 1.22 |
| `runtime.SetFinalizer` | `runtime.AddCleanup` | Go 1.24 |
| `golang.org/x/crypto/sha3` | `crypto/sha3` | Go 1.24 |
| `golang.org/x/crypto/hkdf` | `crypto/hkdf` | Go 1.24 |
| `golang.org/x/crypto/pbkdf2` | `crypto/pbkdf2` | Go 1.24 |
| `httputil.ReverseProxy.Director` | `ReverseProxy.Rewrite` | Go 1.20 (Director deprecated in 1.26) |
| `github.com/google/uuid` (v4/v7) | stdlib `uuid` (`import "uuid"`) | Go 1.27 |
| `tls.Config.Rand` set to a custom reader | leave nil; in tests `testing/cryptotest.SetGlobalRandom` | Go 1.27 |

### Migration Priority

**Before building with the Go 1.27 toolchain (whatever the `go` line says):** grep `go.mod`/`go.work` `godebug` blocks and `//go:debug` lines for `asynctimerchan`, `gotypesalias`, `tls10server`, `tls3des`, `tlsrsakex`, `tlsunsafeekm`, `x509keypairleaf` — Go 1.27 removed these settings: set to an old value they fail the build, set to the final default they are accepted. After any toolchain upgrade, run `go fix ./...`.

**High (safety and correctness):**
1. Remove loop variable shadow copies (Go 1.22+)
2. Replace `math/rand` with `math/rand/v2` (Go 1.22+)
3. Run `govulncheck`
4. Use `errors.Is` / `errors.AsType` (Go 1.26+; `errors.As` before)

**Medium (readability):**
5. Replace `interface{}` with `any` (Go 1.18+)
6. Use `min`/`max` builtins (Go 1.21+)
7. Use `range` over int (Go 1.22+)
8. Use `slices`/`maps` packages (Go 1.21+)
9. Use `cmp.Or` for defaults (Go 1.22+)
10. Use `sync.OnceValue` (Go 1.21+)

**Lower (gradual):**
11. Adopt iterators (Go 1.23+)
12. Replace `sort.Slice` with `slices.SortFunc` (Go 1.21+)
13. Enable PGO for production builds (Go 1.21+)
14. Move tool pins from `tools.go` to `tool` directives (Go 1.24+)

### Key Modernizations by Version

**Go 1.21:** `min`/`max`/`clear` builtins, `slices`/`maps` packages, `slog` logging, `sync.OnceValue`/`OnceFunc`, `context.WithoutCancel`/`AfterFunc`

**Go 1.22:** `range` over int, loop variable per-iteration scoping (fixes closure bug), `math/rand/v2`, enhanced `net/http` routing with `{id}` patterns, `cmp.Or`

**Go 1.23:** iterators (`range` over functions), `iter` package, `slices.Chunk`/`slices.Sorted`, `unique` package, timer/ticker GC without Stop()

**Go 1.24:** generic type aliases, `strings.SplitSeq`/`FieldsSeq`/`Lines`, `runtime.AddCleanup`, `weak.Pointer`, `crypto/sha3`/`hkdf`/`pbkdf2` promoted to stdlib, tool directives in `go.mod`

**Go 1.25:** `runtime/trace.FlightRecorder`, `encoding/json/v2` (experimental)

**Go 1.26:** `errors.AsType[T]()`, `crypto/hpke`, `slog.NewMultiHandler`, `t.ArtifactDir()`, `httputil.ReverseProxy.Director` deprecated (use `Rewrite`), Green Tea GC on by default (re-check GC tuning with profiles), modernized `go fix`, `go tool doc` removed (use `go doc`)

**Go 1.27:** generic methods (methods with their own type parameters; they cannot satisfy interfaces), stdlib `uuid` (`import "uuid"`; `uuid.New`, `NewV4`, `NewV7`, `.String()` gives the 36-character form; no v3/v5, no `sql.Scanner`/`driver.Valuer`), `encoding/json/v2` + `encoding/json/jsontext` stable, and `encoding/json` now runs on the v2 engine while keeping v1 behaviour (error message text may differ; `GOEXPERIMENT=nojsonv2` opts out), `strings`/`bytes.CutLast`, `url.URL.Clone`, `synctest.Sleep`, `httptest.NewTestServer(t, h)` (in-memory network), `goroutineleak` pprof profile, `go test` runs the vet `stdversion` check

**Switching an import to `encoding/json/v2` changes defaults** (v1 `encoding/json` keeps its own):
- Field names match case-sensitively: use the `case:ignore` tag or `json.MatchCaseInsensitiveNames(true)`
- Nil slices and maps encode as `[]` and `{}`, not `null`
- Duplicate member names and invalid UTF-8 are errors
- `time.Duration` has no default encoding and rejects a `format` tag: store it as an int64 or a string

---

## CI/CD (GitHub Actions)

### Pipeline Stages

| Stage | Tool | Purpose |
| --- | --- | --- |
| Test | `go test -race -shuffle=on` | Unit + race detection |
| Coverage | `codecov/codecov-action` | Coverage reporting |
| Lint | `golangci-lint` | Comprehensive linting |
| SAST | `gosec`, `CodeQL`, `govulncheck` | Security analysis |
| Deps | Dependabot / Renovate | Automated updates |
| Release | GoReleaser | Binary releases |

### Test Configuration

- `-race` MUST be used in CI
- `-shuffle=on` to catch order-dependent tests
- `-count=1` for integration tests (disable caching)
- `fail-fast: false` so one Go version failure doesn't cancel others
- Check `go mod tidy && git diff --exit-code`

### Dependency Updates

**Dependabot:** group minor/patch updates, individual PRs for majors. Auto-merge with branch protection as safety net.

**Renovate (alternative):** native automerge, `gomodTidy`, better grouping, custom regex managers (`customManagers` with `customType: "regex"`) for Dockerfiles/Makefiles.

### GoReleaser

Automates cross-compiled binaries, checksums, GitHub Releases. Config varies by project type (CLI, library, monorepo). Always check latest action versions before writing workflow YAML.

### Common CI Mistakes

| Mistake | Fix |
| --- | --- |
| Missing `-race` | Always `go test -race` |
| No `-shuffle=on` | Randomize test order |
| Caching integration tests | `-count=1` |
| `go mod tidy` not checked | `go mod tidy && git diff --exit-code` |
| Third-party actions pinned to a tag (`@vN`) or `@master` | Pin a full commit SHA with the version as a comment (`@<sha> # v4.2.0`); Dependabot `github-actions` bumps both. Tags can be moved: CVE-2025-30066 repointed the tj-actions/changed-files tags to code that dumped secrets |
| `pull_request_target` that checks out the PR head | It runs fork code with the base repo's secrets and token — use `pull_request`, or never check out untrusted code there |
| No `permissions` block | Least-privilege per job |

---

## Dependency Injection

Below ~15 services, use manual constructor injection — no DI library needed. Wire everything in `main()`:

```go
func run(ctx context.Context) error {
    cfg, err := config.Load()
    if err != nil { return fmt.Errorf("loading config: %w", err) }
    logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
    db, err := store.Open(ctx, cfg.DatabaseURL)
    if err != nil { return fmt.Errorf("opening store: %w", err) }
    defer func() { _ = db.Close() }()
    users := user.NewService(db, logger)
    srv := httpapi.New(users, logger)
    return srv.Run(ctx)
}
```

Rules:
- Dependencies MUST be injected via constructors — never globals or `init()`
- Interfaces defined where consumed, not where implemented
- DI container only at composition root (`main()`) — never pass as dependency
- Consider a library at 15+ services with lifecycle management needs
