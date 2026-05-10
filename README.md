# sqlc-cobra

Turn [sqlc](https://sqlc.dev)-generated `*Queries` methods into a tree of
[cobra](https://github.com/spf13/cobra) CLI subcommands at code-generation
time. Get an admin CLI surface for every query in your data layer, kept in
sync with the schema by re-running alongside `sqlc generate`.

```sh
mytool sqlc CountProjects
mytool sqlc GetDevice dev-123
mytool sqlc DeleteProject proj-1
mytool sqlc CreateProject --id=proj-2 --name="Acme" --owner-id=user-7
```

## Install

```sh
go install github.com/sat0yu/sqlc-cobra/cmd/sqlc-cobra-gen@latest
```

## Quick start

### 1. Declare the parent command (`cmd/sqlc/sqlc.go`)

```go
//go:generate sqlc-cobra-gen \
//   -src ../../internal/db/queries \
//   -pkg sqlc \
//   -queries-import github.com/me/myapp/internal/db/queries \
//   -parent SqlcCmd \
//   -out zz_generated_commands.go

package sqlc

import "github.com/spf13/cobra"

var SqlcCmd = &cobra.Command{Use: "sqlc", Short: "Run sqlc queries from the CLI"}

func init() {
    SqlcCmd.PersistentFlags().StringP("dsn", "d", "", "Database DSN")
    SqlcCmd.PersistentFlags().BoolP("yes", "y", false, "Skip the y/N prompt")
    _ = SqlcCmd.MarkPersistentFlagRequired("dsn")
}
```

### 2. Write the adapter (`cmd/sqlc/adapter.go`)

```go
package sqlc

import (
    "database/sql"
    _ "github.com/go-sql-driver/mysql"
    "github.com/me/myapp/internal/db/queries"
)

func openQueries() (*queries.Queries, func(), error) {
    dsn, _ := SqlcCmd.PersistentFlags().GetString("dsn")
    db, err := sql.Open("mysql", dsn+"?parseTime=true")
    if err != nil {
        return nil, func() {}, err
    }
    return queries.New(db), func() { _ = db.Close() }, nil
}
```

### 3. Generate

```sh
go generate ./cmd/sqlc/...
```

This writes `cmd/sqlc/zz_generated_commands.go` — one cobra command per
`*Queries` method, sorted alphabetically, with:
- Named flags for struct-param methods (e.g. `--project-id`)
- Positional arg for single-primitive methods (e.g. `GetDevice <id>`)
- SQL preview + `y/N` confirmation for mutating methods

## Generator flags

| Flag | Default | Required? |
|---|---|---|
| `-src` | `internal/db/queries` | — |
| `-out` | `zz_generated_commands.go` | — |
| `-pkg` | `sqlc` | — |
| `-queries-import` | *(none)* | **yes** |
| `-parent` | `SqlcCmd` | — |
| `-adapter` | `openQueries` | — |
| `-runtime-import` | `github.com/sat0yu/sqlc-cobra/runtime` | — |
| `-mutating-prefixes` | `Create,Update,Delete,Insert,Prune` | — |
| `-engine` | `mysql` | — |

Set `-mutating-prefixes=""` to treat every method as read-only.

## Runtime package

Generated commands import `github.com/sat0yu/sqlc-cobra/runtime`.  You can
also import it directly if you write hand-crafted cobra commands that need the
same helpers:

```go
import "github.com/sat0yu/sqlc-cobra/runtime"

runtime.PrintJSON(runtime.Stdout, row)
runtime.Prompt{In: runtime.Stdin, Out: runtime.Stderr}.ConfirmMutation(cmd, sql, args)
ns := runtime.NullStringFromFlag(cmd, "name")
```

### IO sinks

The generated commands write through three package-level variables:

- `runtime.Stdin` — defaults to `os.Stdin`, used for prompt input
- `runtime.Stdout` — defaults to `os.Stdout`, used for normal output
- `runtime.Stderr` — defaults to `os.Stderr`, used for prompt preview

Tests can reassign them to capture or feed content in-process without
spawning a subprocess:

```go
oldStdout := runtime.Stdout
runtime.Stdout = &buf
defer func() { runtime.Stdout = oldStdout }()
```

These are package globals — tests that swap them must not run in
parallel with other tests that swap them.

## Example

See [`examples/mysql/`](examples/mysql/) for a complete end-to-end demo with
schema, sqlc config, generated queries, and a working CLI.

## Documents

- [AGENTS.md](AGENTS.md) — operating rules + reading order
- [docs/PROJECT.md](docs/PROJECT.md) — what we're building and why
- [docs/DESIGN.md](docs/DESIGN.md) — library architecture and public API
- [docs/REFERENCE.md](docs/REFERENCE.md) — full reference implementation
- [docs/PLAN.md](docs/PLAN.md) — v0.1 scope, first steps, open questions
- [CHANGELOG.md](CHANGELOG.md) — version history

## License

MIT — see [LICENSE](LICENSE).

