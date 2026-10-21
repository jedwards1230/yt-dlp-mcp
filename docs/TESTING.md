# Testing strategy

<TODO(template): one or two sentences on the overall testing philosophy for
this project — what gets tested first, and what's deliberately deprioritized
and why.>

## Layers

| Layer | Type | CI | Approach |
|---|---|---|---|
| <TODO(template): e.g. "core logic"> | <TODO(template): unit / integ> | <TODO(template): every push / manual / OS-gated> | <TODO(template): what it tests and how> |
| <TODO(template): e.g. "persistence"> | <TODO(template)> | <TODO(template)> | <TODO(template)> |

## Explicitly avoid

- Testing implementation details instead of behavior — a refactor that
  keeps behavior identical shouldn't break tests.
- Skipping a layer's tests because the code "looks thin" — thin wrappers
  around external systems (HTTP, DB, subprocess) are exactly where
  wire-format bugs hide.
- <TODO(template): project-specific anti-patterns you've already been
  burned by; add them here so the next reviewer doesn't reintroduce them.>
