---
name: go-database
description: Go with PostgreSQL through pgx (queries, transactions, connection pools). TRIGGER when Go code uses SQL or a database.
---

# Go Database

PostgreSQL with pgx for Go services.

## Database (PostgreSQL with pgx)

### Core Rules

1. Use pgx, not ORMs — ORMs hide SQL, generate unpredictable queries, and make debugging harder
2. Context MUST be passed to every database call — pgx takes it as the first argument: `pool.Query(ctx, …)`, `tx.Exec(ctx, …)`
3. Use transactions for multi-statement operations
4. Handle NULLable columns with pointer fields (`*string`, `*time.Time`)
5. Connection pool MUST be configured
6. Use external tools for migrations (golang-migrate, Atlas) — never AI-generated migration SQL
7. Never create or modify database schemas — requires human review with production context
8. Avoid triggers, views, stored procedures — keep SQL explicit in Go code

### Struct Scanning with pgx

```go
rows, err := pool.Query(ctx, "SELECT id, name, bio, deleted_at FROM users WHERE active = true")
if err != nil {
    return fmt.Errorf("querying users: %w", err)
}
users, err := pgx.CollectRows(rows, pgx.RowToStructByName[User]) // closes rows
```

`RowToStructByName` needs a column for every struct field and a field for every column, matched by `db` tag (untagged fields match by name, case-insensitively, ignoring underscores). For a partial select use `RowToStructByNameLax`.

### NULLable Columns

Use pointer fields (recommended) — clean, works with JSON marshaling:

```go
type User struct {
    ID        int64      `db:"id"         json:"id"`
    Name      string     `db:"name"       json:"name"`
    Bio       *string    `db:"bio"        json:"bio,omitempty"`  // nil when NULL
    DeletedAt *time.Time `db:"deleted_at" json:"deleted_at"`     // nil when NULL
}
```

### Error Handling

**Common database error patterns:**

| Error | Detection | Action |
| --- | --- | --- |
| Row not found | `errors.Is(err, pgx.ErrNoRows)` | Return domain error |
| Unique constraint | `*pgconn.PgError` with `Code == "23505"` | Return conflict error |
| Connection refused | `err != nil` on `pool.Ping` | Fail fast, retry with backoff |
| Serialization failure | `*pgconn.PgError` with `Code == "40001"` | Retry entire transaction |
| Context canceled | `errors.Is(err, context.Canceled)` | Stop, propagate |

```go
// Go 1.26+; before: var pgErr *pgconn.PgError; errors.As(err, &pgErr)
if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok && pgErr.Code == "23505" { // unique_violation
    return ErrConflict
}
```

### Transactions

**Isolation levels:**

| Level | Use when |
| --- | --- |
| Read Committed | Default — good for most operations |
| Repeatable Read | Need consistent reads within a transaction |
| Serializable | Financial ops, inventory, strict consistency |

Under Serializable, PostgreSQL aborts conflicting transactions with `40001`, so the caller MUST re-run the whole func — the snapshot it read is gone:

```go
for attempt := 1; ; attempt++ {
    err := pgx.BeginTxFunc(ctx, pool, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx pgx.Tx) error {
        return transfer(ctx, tx, from, to, amount) // commits on nil, rolls back on error
    })
    if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok && pgErr.Code == "40001" && attempt < 10 {
        continue // serialization_failure: retry the transaction, not the statement
    }
    return err
}
```

On a few hot rows, Serializable retries pile up — prefer SQL arithmetic or `FOR UPDATE` there. Serializable earns its retries when the invariant spans rows or predicates no single lock covers.

**Row locks:**

| Clause | Effect |
| --- | --- |
| `FOR UPDATE` | Locks rows — other UPDATE/DELETE and locking SELECTs wait; a plain SELECT is not blocked |
| `FOR UPDATE NOWAIT` | Fails immediately instead of waiting |
| `FOR SHARE` | Prevents writes, allows reads |

### Connection Pool

```go
config, err := pgxpool.ParseConfig(dsn)
if err != nil { return nil, fmt.Errorf("parsing dsn: %w", err) }
config.MaxConns = 25
config.MinConns = 5
config.MaxConnLifetime = 5 * time.Minute
config.MaxConnIdleTime = 1 * time.Minute
pool, err := pgxpool.NewWithConfig(ctx, config)
if err != nil { return nil, fmt.Errorf("creating pool: %w", err) }
if err := pool.Ping(ctx); err != nil { // the pool connects lazily — fail fast here
    pool.Close()
    return nil, fmt.Errorf("connecting: %w", err)
}
```

Monitor `pool.Stat()` in production — the pool is exhausted when `EmptyAcquireCount()` keeps rising while `AcquiredConns()` sits at `MaxConns()`; `EmptyAcquireWaitTime()` (cumulative) shows the time spent waiting on an empty pool.

### Batch Processing

Avoid row-by-row (N round trips) and one giant batch (locks, memory). Start with a few hundred rows per batch and measure.

**pgx COPY protocol** (fastest for bulk inserts):

```go
rows := make([][]any, len(users))
for i, u := range users {
    rows[i] = []any{u.Name, u.Bio}
}
_, err := pool.CopyFrom(ctx,
    pgx.Identifier{"users"},
    []string{"name", "bio"},
    pgx.CopyFromRows(rows),
)
```

### Indexing Guidelines

Never create or drop indexes yourself — suggest to the developer.

**When to suggest adding:** foreign key columns (PostgreSQL does NOT auto-index FKs), columns in `WHERE`/`JOIN`/`ORDER BY`, composite indexes (equality-filtered columns first, the range or `ORDER BY` column last), partial indexes for filtered queries.

**When to suggest removing:** near-zero `idx_scan` count, duplicates, write-heavy tables where indexes slow INSERT/UPDATE.

Check existing indexes: `SELECT indexname, indexdef FROM pg_indexes WHERE tablename = 'mytable';`

### Query Tips

- `EXPLAIN ANALYZE` before optimizing — measure, don't guess
- List columns explicitly — avoid `SELECT *`
- Prefer `EXISTS` over `COUNT` for existence checks
- Avoid N+1 queries — use `JOIN` or batch `WHERE id IN (...)`

---

## Common Mistakes

| Category | Mistake | Fix |
| --- | --- | --- |
| Database | `SELECT *` | List columns explicitly |
| Database | N+1 queries | Use `JOIN` or `WHERE id IN (...)` |
| Database | Unconfigured connection pool | Set MaxConns, MinConns, MaxConnLifetime |
