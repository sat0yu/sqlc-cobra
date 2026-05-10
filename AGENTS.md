# `sqlc-cobra` Agent Rules

You are an autonomous agent working on `sqlc-cobra`, a Go library that turns
[sqlc](https://sqlc.dev)-generated `*Queries` methods into a tree of
[cobra](https://github.com/spf13/cobra) CLI subcommands at code-generation
time.

## Read first

Before touching any code, read these in order:

1. [docs/PROJECT.md](docs/PROJECT.md) — what we're building and why
2. [docs/DESIGN.md](docs/DESIGN.md) — library architecture and public API
3. [docs/REFERENCE.md](docs/REFERENCE.md) — the working reference
   implementation from a Go service (the seed for this extraction)
4. [docs/PLAN.md](docs/PLAN.md) — v0.1 scope, first steps, open questions,
   gotchas

The reference implementation in `docs/REFERENCE.md` is already production-
running in another repository. This library is a clean extraction. **Do not
redesign from scratch** — adapt the reference and document deviations.

## Core principles

1. The library exists to spare consumers from hand-writing 30–100 cobra
   commands that mirror their sqlc queries. Optimise for: zero-config
   regeneration, helpful per-command help text, safety against accidental
   mutations.
2. The library does **not** open database connections. Consumers provide an
   `openQueries()` adapter (or whatever they name it); the generated code
   calls that adapter by name.
3. The generator and the runtime helpers are separable. A consumer who only
   wants the helpers (e.g. their own hand-written commands) should be able
   to import `runtime/` without dragging in the generator.

## When modifying code

- All non-trivial changes follow a tiny spec-driven flow: open a doc under
  `docs/decisions/` (or extend the relevant existing doc) before changing
  the API surface. Bug fixes need no spec.
- Run `go test ./...` after every change.
- Run `go fmt ./...` after every change.
- Run `go vet ./...` before commit.
- Each commit must keep `go build ./...` green.

## Commit boundaries

| Boundary | What to include |
|----------|-----------------|
| **Docs** | `docs/**.md` (specs, decision records) |
| **Generator core** | `internal/codegen/` + tests |
| **Runtime helpers** | `runtime/` + tests |
| **CLI binary** | `cmd/sqlc-cobra-gen/` |
| **Examples** | `examples/<engine>/` end-to-end demos |

Never mix boundaries in a single commit.

## API stability

This is pre-v1.0 software. Breaking changes are allowed at minor versions
during the `v0.x` series, but every breaking change must be called out in
`CHANGELOG.md` with a migration note.
