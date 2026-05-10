# sqlc-cobra

Turn [sqlc](https://sqlc.dev)-generated `*Queries` methods into a tree of
[cobra](https://github.com/spf13/cobra) CLI subcommands at code-generation
time. Get an admin CLI surface for every query in your data layer, kept in
sync with the schema by re-running alongside `sqlc generate`.

> **Status:** Pre-implementation. The library does not yet exist.
> Handoff documentation describing the design and the reference
> implementation it will be extracted from lives under [`docs/`](docs/).
> Start with [`AGENTS.md`](AGENTS.md).

## Documents

- [AGENTS.md](AGENTS.md) — operating rules + reading order
- [docs/PROJECT.md](docs/PROJECT.md) — what we're building and why
- [docs/DESIGN.md](docs/DESIGN.md) — library architecture and public API
- [docs/REFERENCE.md](docs/REFERENCE.md) — full reference implementation
- [docs/PLAN.md](docs/PLAN.md) — v0.1 scope, first steps, open questions
