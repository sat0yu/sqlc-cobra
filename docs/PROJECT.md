# Project Context

## What we're building

A Go library that, given a sqlc-generated `*Queries` package, emits a tree
of [cobra](https://github.com/spf13/cobra) CLI subcommands — one per query
method — at code-generation time. Consumers get an instant CLI surface for
every query in their data layer; the generator re-runs alongside
`sqlc generate` so the CLI tracks the schema.

The shape we are mimicking:

```sh
mytool sqlc CountProjects
mytool sqlc GetDevice <id>
mytool sqlc DeleteProjectMember --project-id=PROJ --member-id=USER --yes
mytool sqlc CreateProject --id=proj-1 --name="Acme" --owner-id=user-7
```

## Why a library

The use case originated in a private Go service where 45+ sqlc queries
needed admin CLI exposure. Hand-writing 45 cobra commands is tedious;
worse, every `sqlc generate` invalidates them. The pattern is generic —
any sqlc consumer hits the same problem — so we are extracting the
implementation from the originating service into a reusable library.

The reference implementation is documented in full at
[REFERENCE.md](REFERENCE.md). This library is a clean extraction with
the host-specific assumptions parameterised.

## sqlc background — what we exploit

These are sqlc-emitted conventions that the generator relies on. They have
held stable across sqlc versions for years (verified at v1.22.0 in the
seed implementation) but each one is technically an assumption.

1. **`*Queries` receiver.** sqlc generates a `type Queries struct` and
   attaches one method per `-- name: …` directive in your `.sql` files.
2. **Context first.** Every method has the signature
   `(q *Queries) Foo(ctx context.Context, …) (…, error)`. The single
   exception is `WithTx`, which the generator skips.
3. **Param structs.** Methods that bind more than one column take a single
   `FooParams` struct argument whose field order matches the SQL parameter
   order (positional `?` / `$N` binding). Single-column methods take a
   plain primitive.
4. **SQL string constants.** Each method's SQL text is held in a top-level
   `const` named `lowerFirst(MethodName)` — `DeleteProjectMember` →
   `deleteProjectMember`. When the SQL contains backticks, sqlc emits
   the constant as a string-concatenation expression rather than a single
   literal (we must evaluate it). See [REFERENCE.md](REFERENCE.md) for the
   AST handling.
5. **Return shapes (7 in total):**
   - `(int64, error)` — count or rows-affected
   - `(error)` — exec without rows-affected
   - `(SomeRow, error)` — single row
   - `([]SomeRow, error)` — list
   - `([]string, error)` — primitive list (a real case in the seed)
   - `(sql.Result, error)` — insert returning result
   - We have not seen multi-value returns beyond these.

## Engine awareness

The seed implementation targets **MySQL**. Field types observed in the
seed are: `string`, `int32`, `int64`, `sql.NullString`, `time.Time`. The
generator's type table currently handles those plus `int`, `bool`.

**PostgreSQL is the next supported engine** (see [PLAN.md](PLAN.md)). It
will introduce `pgtype.Text`, `pgtype.UUID`, `pgtype.Timestamptz`, etc.
The library's type registry must be pluggable to absorb these — the
design must not bake MySQL types into the core.

## Public surface (target)

A consumer's repository, after adopting `sqlc-cobra`, looks like:

```text
cmd/sqlc/
  sqlc.go                  # parent cobra.Command + //go:generate directive
  adapter.go               # consumer-defined openQueries() (15 lines)
  zz_generated_commands.go # generated; do not edit
internal/db/queries/       # sqlc output (unchanged)
```

`sqlc.go` (consumer-written, ~15 lines):

```go
//go:generate sqlc-cobra-gen \
//   -src ../../internal/db/queries \
//   -pkg sqlc \
//   -queries-import github.com/me/app/internal/db/queries \
//   -parent SqlcCmd \
//   -out zz_generated_commands.go

package sqlc

import "github.com/spf13/cobra"

var SqlcCmd = &cobra.Command{Use: "sqlc", Short: "…"}
```

`adapter.go` (consumer-written, ~15 lines):

```go
package sqlc

import (
    "github.com/me/app/internal/db"
    "github.com/me/app/internal/db/queries"
)

func openQueries() (*queries.Queries, func(), error) {
    // Consumer's own DSN resolution + connection setup.
    conn := db.NewDB(...)
    return queries.New(conn), func() { conn.Close() }, nil
}
```

That's it. The generated file uses both — calls `openQueries()` for the
DB handle and calls into `sqlccobra.Runtime` for output formatting and
the y/N confirmation prompt.

## Key learnings from the seed implementation

These are non-obvious things the reference impl had to handle. Anything
you re-implement must handle them, or document why they don't apply.

- **SQL const AST shape varies.** Constants with backticks in the SQL
  text are emitted as `BinaryExpr` (string concatenation), not
  `BasicLit`. Recursive evaluation required. See `evalStringExpr` in
  [REFERENCE.md](REFERENCE.md).
- **Reserved-word collision.** A struct field named `Type` lowercases to
  `type`, which is a Go reserved word. Local variables in generated
  code need a `safeIdent` escape (e.g. `type_`).
- **`cmd.Context()` may be nil.** `cobra.Command.Execute()` does not
  inject a context; only `ExecuteContext()` does. The runtime exposes
  `CmdContext(cmd)` that falls back to `context.Background()`.
- **JSON output and Go field names vs. tags.** sqlc by default does not
  emit JSON struct tags. Our cleaner walks the value with `reflect` and
  emits a `map[string]any` keyed on Go field names. **Side-effect:**
  helpers that build their own JSON output (e.g. exec-result formatting)
  must use a map literal, not a tagged anonymous struct — the cleaner
  ignores tags.
- **`sql.NullString` JSON flattening.** Default Go encoding produces
  `{"String":"x","Valid":true}` which is ugly. The cleaner flattens it
  to bare `"x"` (when Valid) or `null` (when not).
- **Persistent flags + viper.** A single `--dsn` flag at the parent
  command level is inherited by all subcommands. Binding to viper works,
  but only if you do it from the parent's `init()` and do not re-bind in
  children.
- **`go generate` working directory.** It runs in the directive's source
  file directory, not the repo root. Paths in the directive must be
  relative-from-that-dir.
- **Integration tests need a sub-package.** A TestMain that spins up
  Docker via `testutils.IntegrationTestRunner` gates every test in its
  binary. If you want unit tests for the helpers to run without Docker,
  keep the integration tests in a sibling sub-package (the seed uses
  `cmd/sqlc/integration/`).

## Out of scope (for now)

- Auth / authorisation / audit logging beyond the y/N prompt
- Multi-DSN / multi-database invocations in a single command
- Streaming output / pagination for large result sets
- Custom output formats (CSV, table). v0.1 is JSON-only.
- Transaction batching across CLI invocations.

## About the reference implementation

The seed implementation lives in a private Go service that cannot be
shared as a remote reference. **All the code you need to mirror is
embedded in [REFERENCE.md](REFERENCE.md)** — full files, not summaries.
That service runs the implementation against a real production MySQL.
