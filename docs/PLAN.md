# Plan & Open Questions

## v0.1 scope (MySQL-only)

The first release should be a faithful library extraction of the seed. No
new features beyond what the seed has. Concrete deliverables:

1. **`runtime/` package** — public versions of the seed's helpers:
   `PrintInt64`, `PrintOK`, `PrintJSON`, `PrintExecResult`,
   `ConfirmMutation` (as a `Prompt` value, not package-globals),
   `CmdContext`, `ParseInt32/64/Bool/Time`, `NullStringFromFlag`,
   `ErrAborted`, `IsAborted`. Plus tests.
2. **`internal/codegen/` package** — AST walker and template rendering.
   Adapted directly from the seed's `internal/gen/sqlccli/main.go` with
   the three seed-isms parameterised (see
   [REFERENCE.md](REFERENCE.md) §1).
3. **`cmd/sqlc-cobra-gen/`** — thin `main()` that wires CLI flags into
   `internal/codegen`.
4. **`examples/mysql/`** — a complete consumer setup with sqlc config,
   schema, generated `*Queries`, and a CLI that uses `sqlc-cobra-gen`.
   Doubles as an integration test target.
5. **README.md** — install, basic usage, the `cmd/sqlc/sqlc.go` +
   `adapter.go` pattern, link to examples.
6. **`.github/workflows/ci.yml`** — `go test ./...`, `go vet ./...`,
   `gofmt -l` enforcement.
7. **`CHANGELOG.md`** — start it with `v0.1.0` and document the API.

Non-goals for v0.1:

- PostgreSQL support (v0.2)
- Plugable type registry beyond the built-in `EngineMySQL` (v0.2)
- Custom output formats (v0.3)

## First steps (suggested order)

1. **Bootstrap repo.** `go mod init github.com/sat0yu/sqlc-cobra`. Add
   `runtime/`, `internal/codegen/`, `cmd/sqlc-cobra-gen/`, `examples/`
   as empty directories with `.gitkeep`.
2. **Port `runtime/` first.** It's standalone. Public API per
   [DESIGN.md](DESIGN.md) §"Runtime API". Move the IO sinks from
   package globals to method receivers / parameters. Lift the helper
   tests from the seed verbatim.
3. **Port `internal/codegen/`.** Start by copying the seed's
   `main.go` into `internal/codegen/codegen.go` minus `main()`. Then:
   - Extract `mutatingPrefixes`, the import path, the parent var name,
     the output package name into a `Config` struct.
   - Replace unqualified helper calls in the template (`printInt64`,
     `confirmMutation`, etc.) with `{{.RuntimeAlias}}.PrintInt64`,
     etc., where `RuntimeAlias` is derived from the runtime-import flag.
   - Carry over every shape test from the seed (`TestParseRealQueries`,
     `TestRenderRealQueriesIsValidGoAndIdempotent`, `TestKebabCase`,
     `TestEvalStringExpr_BinaryConcat`). Adapt them to read from a
     fixture queries package shipped as testdata.
4. **Wire `cmd/sqlc-cobra-gen/`.** ~30 lines: flags → `Config` →
   `codegen.Generate(cfg)`.
5. **Build `examples/mysql/`.** A small schema (homes table, devices
   table). Run sqlc. Run `sqlc-cobra-gen`. Write an integration test
   that invokes `examples/mysql sqlc CountProjects` etc. against a
   dockertest MySQL.
6. **Smoke test against the seed's queries package.** Vendor a copy of
   the seed `internal/db/queries/` into `testdata/seed/` and
   assert the generator produces parseable Go for all 45 methods. This
   is the strongest "doesn't regress vs. seed" check.
7. **Cut `v0.1.0`** with a CHANGELOG entry and a release tag.

## Open questions (decide during impl)

1. **PostgreSQL types in v0.1?**
   *Recommendation: no.* Ship MySQL-only with a registered `EngineMySQL`.
   Make the registry pluggable in the codebase so v0.2 only adds an
   `EnginePostgres` entry. Why: pgtype is a moving target (v4 → v5
   breaking changes), and trying to support it from day one will
   double the scope.

2. **Runtime IO sinks: globals vs. parameters?**
   The seed uses package-globals (`promptIn`, `promptOut`, `stdoutW`).
   That's fine for a single-app CLI but bad for a public library — any
   test that mutates them must serialise. **Choose parameters.**
   Concrete shape:

   ```go
   type Prompt struct { In io.Reader; Out io.Writer }
   func (p Prompt) ConfirmMutation(cmd *cobra.Command, sql string, args []any) error
   func PrintInt64(w io.Writer, n int64) error
   ```

   Generated code constructs them fresh each invocation:
   `runtime.Prompt{In: os.Stdin, Out: os.Stderr}.ConfirmMutation(...)`.

3. **NullString JSON form: bare value vs. wrapper object?**
   Seed flattens to bare value/null. Library should do the same — the
   object form (`{"String":"x","Valid":true}`) leaks an
   implementation-detail to operators. Decide once whether other
   nullable types (e.g. `sql.NullInt64`) get the same treatment.
   *Recommendation: yes, flatten all `sql.Null*`*.

4. **`go generate` integration vs. standalone CLI?**
   Both work today (the seed uses `go:generate`). The library should
   document the `go:generate` flow as primary; standalone usage
   `sqlc-cobra-gen -src ... -out ...` should just work too. No
   binary distribution needed for v0.1; `go install` is enough.

5. **Help-text quality.**
   Seed's `Short` is just `"Calls Queries.<Name>"`. The library could
   parse the SQL comment line (the `-- name: …` directive often has
   no description, but consumers sometimes add a second line). Stretch
   goal for v0.1; otherwise punt to v0.2.

6. **Versioning policy for the runtime/generator pair.**
   The generated file embeds calls into `runtime/`. If the runtime API
   changes, regeneration is required. **Lock the runtime API for the
   `v0.x` series after v0.1 ships**: minor versions can add but not
   remove or rename. Major bumps (`v1.0`) allow breakage.

7. **What goes in the generated file's header?**
   Seed has a minimal `// Code generated …; DO NOT EDIT.` comment.
   Add the library version that generated it so debugging stale
   generation becomes trivial:

   ```go
   // Code generated by sqlc-cobra v0.1.0 (https://github.com/sat0yu/sqlc-cobra)
   // DO NOT EDIT.
   ```

## Gotchas to handle in code

Anything below already bit the seed. Don't relearn them.

| Item | Where it shows up |
|---|---|
| sqlc SQL constants as `BinaryExpr` (backtick embed) | `internal/codegen` `evalStringExpr` |
| Reserved Go keywords in struct field names (`Type`) | `internal/codegen` `safeIdent` |
| `cmd.Context()` is nil under `Execute()` | `runtime` `CmdContext` |
| JSON tag vs Go field name in cleaner | `runtime` `PrintExecResult` uses map, not tagged struct |
| `go generate` working directory | Document in README + example's `//go:generate` line |
| TestMain monopolises the test binary | Integration tests in sibling sub-package |
| Persistent flag binding to viper happens once | Document in README; do not re-bind in subcommand `init()`s |

## Test strategy

### Unit-level (no Docker)

- `runtime/` — every public function has a table-driven test. Critical:
  `ConfirmMutation` y/Y/yes/no/empty/EOF cases. JSON flattening for
  `sql.NullString` and `sql.NullInt64` (once added).
- `internal/codegen/` — shape classification, kebab-case conversion,
  string-concat const evaluation, render idempotency. Output must
  parse as valid Go.

### Fixture-based (no Docker)

- A fixture queries package under `internal/codegen/testdata/mysql/`
  with one method per shape (8 methods covering all combinations).
  The generator runs against it; assertions check both classification
  and rendered output.
- A larger fixture under `internal/codegen/testdata/seed/`
  (vendored copy of the seed's queries package) — assert all 45
  methods render to valid Go. Acts as a regression backstop.

### Integration (Docker)

- `examples/mysql/` has a real schema + dockertest test that:
  - Runs `sqlc generate` then `sqlc-cobra-gen`
  - Builds the example CLI
  - Exec's it as a subprocess with various args
  - Asserts stdout JSON / exit codes
- This is the only place that needs a database; everything else is
  pure Go.

### CI matrix

- `linux/amd64`, Go `1.22`, `1.23`, `1.24` (drop older as Go EOLs).
- macOS optional; the library has no platform-specific code.
- Docker required for the `examples/mysql/` integration test; run on
  GitHub Actions' `ubuntu-latest` which provides Docker out of the box.

## Future enhancements (not v0.1)

- `EnginePostgres` (`pgtype.Text`, `pgtype.UUID`, `pgtype.Timestamptz`,
  `pgtype.Numeric`, …)
- `EngineSQLite`
- Custom output formats (`--format=table|csv|json`)
- `--dry-run` flag that prints the SQL and bound args without executing
- `--explain` flag that runs `EXPLAIN <sql>` and prints the plan
- A `--list` subcommand on the parent that prints a one-line summary
  per generated command, grouped by table
- Optional consumer-supplied descriptions, parsed from a sidecar `.toml`
  next to the queries package
