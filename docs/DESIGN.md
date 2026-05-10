# Library Design

## Architecture

Two artefacts ship from this repository:

1. **`runtime/`** — a Go package consumers import at runtime. Contains
   the helpers the generated commands call: output formatters
   (`PrintInt64`, `PrintJSON`, `PrintExecResult`), the y/N
   `ConfirmMutation` prompt, primitive parsers, `CmdContext`, and the
   `NullStringFromFlag` helper.
2. **`cmd/sqlc-cobra-gen/`** — a Go program consumers invoke from a
   `//go:generate` directive. Walks the sqlc-generated queries package
   with `go/ast`, classifies each method, and emits a single
   `zz_generated_commands.go` into the consumer's chosen package.

The classification logic and template rendering lives in
`internal/codegen/` so it can be reused by tests without going through
`main`.

```text
github.com/sat0yu/sqlc-cobra/
├── runtime/                   importable, ~250 LOC + tests
├── internal/codegen/          ~600 LOC + tests
├── cmd/sqlc-cobra-gen/        ~30 LOC; flag parsing + glue
├── examples/
│   ├── mysql/                 end-to-end demo using sqlc + mysql
│   └── postgres/              v0.2 target
└── docs/
```

## Generator CLI

```text
sqlc-cobra-gen \
  -src <queries-package-path>      # filesystem path (relative or absolute)
  -out <output-file-path>          # written by the generator
  -pkg <go-package-name>           # package name for the output file
  -queries-import <import-path>    # import path of the sqlc package
  -parent <var-name>               # the parent cobra.Command variable (e.g. SqlcCmd)
  -adapter <fn-name>               # consumer-side openQueries fn name
  -runtime-import <import-path>    # e.g. github.com/sat0yu/sqlc-cobra/runtime
  -mutating-prefixes <csv>         # default: Create,Update,Delete,Insert,Prune
  -engine <name>                   # mysql | postgres ; selects the type registry
```

Defaults are chosen so that the typical sqlc consumer can wire it up with
five or six flags. Every flag has a sensible default except the three that
encode consumer-specific identifiers (`-pkg`, `-queries-import`,
`-parent`).

## Runtime API (target shape)

```go
package runtime

// Output formatters.
func PrintInt64(w io.Writer, n int64) error
func PrintOK(w io.Writer) error
func PrintJSON(w io.Writer, v any) error
func PrintExecResult(w io.Writer, r sql.Result) error

// Mutation prompt.
type Prompt struct {
    In  io.Reader  // typically os.Stdin
    Out io.Writer  // typically os.Stderr (for prompt + preview)
}
func (p Prompt) ConfirmMutation(cmd *cobra.Command, sqlText string, args []any) error
var ErrAborted = errors.New("aborted by user")
func IsAborted(err error) bool

// Context plumbing.
func CmdContext(cmd *cobra.Command) context.Context

// Primitive parsers (used by generated commands for positional args).
func ParseInt32(s string) (int32, error)
func ParseInt64(s string) (int64, error)
func ParseInt(s string) (int, error)
func ParseBool(s string) (bool, error)
func ParseTime(s string) (time.Time, error)  // RFC3339

// Nullable flag helpers.
func NullStringFromFlag(cmd *cobra.Command, flag string) sql.NullString
```

The runtime package has no transitive dependency on any database driver.
It imports `database/sql`, `github.com/spf13/cobra`, and stdlib only.
`encoding/json` does the marshalling; `reflect` does the
`sql.NullString` flattening.

### A note on the Prompt struct

The seed implementation used package-level variables (`promptIn`,
`promptOut`, `stdoutW`) for IO so tests could swap them. For a public
library this is unfriendly — concurrent tests would race on shared
state. Make the IO sinks parameters: `Prompt{In, Out}` and a separate
`Stdout io.Writer` passed into the print helpers.

The generator template then becomes:

```go
RunE: func(cmd *cobra.Command, args []string) error {
    ctx := runtime.CmdContext(cmd)
    q, cleanup, err := openQueries()
    if err != nil { return err }
    defer cleanup()
    // ...
    if err := runtime.Prompt{In: os.Stdin, Out: os.Stderr}.
        ConfirmMutation(cmd, sqlText, []any{...}); err != nil {
        return err
    }
    // ...
    return runtime.PrintInt64(os.Stdout, n)
},
```

The IO sinks are hardcoded to `os.*` in the generated code; tests
construct their own commands and call `RunE` directly with stubbed IO.

## Consumer-side integration

Two consumer-written files plus the generator output. See
[PROJECT.md](PROJECT.md) for the directory layout and code samples. The
consumer's responsibilities are exactly:

1. Declare a parent `cobra.Command` (e.g. `SqlcCmd`).
2. Write an `openQueries() (*queries.Queries, func(), error)` adapter
   that knows how to read the consumer's DSN config and build a
   `*Queries`.
3. Add a `//go:generate` directive with the right import paths.

The consumer does **not** write any other Go code. They get every query
as a CLI subcommand for free.

## Type registry

The generator must support consumer-specific Go types in `Params`
structs and single-arg methods. The seed's hardcoded type table was
fine for MySQL but is the main extensibility point for PostgreSQL.

Design:

```go
package codegen

// TypeHandler knows how to render the CLI flag/positional handling
// for a particular Go type used in a sqlc-generated method.
type TypeHandler struct {
    // GoExpr is the Go type expression as it appears in the queries
    // package, e.g. "string", "sql.NullString", "pgtype.Text".
    GoExpr string

    // FlagDecl is the Go code that registers the cobra flag.
    // Template variables: {{.Flag}} (kebab-case flag name),
    // {{.Field}} (PascalCase field name), {{.Required}} (bool).
    FlagDecl string

    // FlagRead is the Go code that reads the flag into a local var.
    // Template variables: same as FlagDecl plus {{.Local}}.
    FlagRead string

    // PositionalParse is the Go code that turns args[N] into a local
    // var. Empty if the type is not allowed as a positional arg.
    PositionalParse string
}

// Engine bundles a default type registry.
type Engine struct {
    Name     string
    Handlers []TypeHandler
}

var (
    EngineMySQL    = Engine{...}  // string, int32, int64, sql.NullString, time.Time, ...
    EnginePostgres = Engine{...}  // pgtype.Text, pgtype.UUID, pgtype.Timestamptz, ...
)
```

The `-engine` flag selects which `Engine` to start from. v0.2 can add
`-extra-handlers <yaml>` to layer consumer-defined handlers on top of
the engine defaults.

## Mutation classifier

The seed classifies methods as mutating by name prefix:
`Create | Update | Delete | Insert | Prune`. This works because sqlc's
`-- name: …` directives use English verbs by convention.

Make the prefix list a flag (`-mutating-prefixes`) with the seed's list
as default. A `-no-classifier` flag (or `-mutating-prefixes=""`) treats
every method as read-only — useful for projects where every operation
should bypass the prompt.

## Output format

v0.1: JSON only. The runtime's `PrintJSON` flattens `sql.NullString` to
bare value-or-null (see [PROJECT.md](PROJECT.md) → Key learnings).

v0.2+: consider a `--format=table|csv|json` flag, implemented in the
runtime so generated code doesn't need to know.

## Decisions already taken (do not relitigate)

| Decision | Rationale |
|---|---|
| Code generation, not runtime reflection | Drift detection at compile time; per-command help text with real arg names; debuggable stack traces. |
| Named flags for struct-param methods | Self-documenting, order-independent, easier to script. |
| Positional for single-primitive methods | The shape `cmd <id>` is concise and unambiguous when there's exactly one arg. |
| SQL preview + `y/N` prompt for mutations | Layered safety: `--dsn` requires intent; `--yes` skips prompt for scripting. |
| JSON for structured returns | Standard, parseable downstream. v0.1 scope. |
| Library does not open DB connections | Consumers vary too much in DSN resolution, pool tuning, driver choice. The adapter pattern keeps the library driver-agnostic. |

## Decisions outstanding

See [PLAN.md](PLAN.md) → "Open questions".
