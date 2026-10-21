# CLAUDE.md

@CONTRIBUTING.md

Guidance for Claude Code when working in this repository.

## What this is

<TODO(template): one-paragraph purpose — what this project is, who consumes
it, and the one architectural choice that most shapes the codebase.>

Full product requirements + design: [`docs/PRD.md`](docs/PRD.md). Read it
before structural changes. [`docs/TESTING.md`](docs/TESTING.md) holds the test
strategy; add further design docs under `docs/` as the project grows.

## Architecture invariants (violations are bugs)

1. **Example — state it as a rule, not a description.** "The CLI never writes
   to storage directly; all writes go through the `store` package" is an
   invariant. "The CLI has a store package" is not — it doesn't say what
   breaks if violated.
2. <TODO(template): a second invariant specific to this project, or delete
   this line if one example doesn't yet have a pair.>

## Design discipline

- **Opinions are config.** Before hardcoding a behavior, ask: config default,
  plugin, or genuinely core? A value a user might reasonably change becomes a
  default, never a literal.
- <TODO(template): project-specific discipline rules, if any.>

## Commands

```bash
<TODO(template): the CI gate command(s), exactly as CI runs them — e.g.
`go build ./... && go vet ./... && go test ./...`>
```

## Layout

| Path | Purpose |
|------|---------|
| <TODO(template): e.g. `cmd/`> | <TODO(template): one-line responsibility; point at a package doc for detail rather than repeating it here> |

## Hooks

Hooks are in `.claude/hooks/` — see the Hook Overview section in `README.md`
for the full table and output rules.
