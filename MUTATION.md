# MUTATION.md — mutants gremlins reports that tests cannot kill

Measured 2026-11-15 with gremlins v0.6.0 (`make mutation-full`, then `make mutation`).
Nothing here is silenced by configuration; each entry is a mutant that stays LIVED.
Because of the one below, `catalog` measures 98.95% efficacy, so the efficacy gate in
`.gremlins.yaml` is 98 (gremlins fails at or below the threshold) instead of 99.

## Equivalent (LIVED)

| Package | File:line | Mutator | Why it is equivalent |
|---|---|---|---|
| catalog | packaging.go:195:8 | CONDITIONALS_NEGATION (`d > 0` -> `d <= 0` in `checkDims`) | The loop counts the non-zero dimensions in `set` and the next check is `set != 0 && set != len(dims)`. The mutant counts the zero dimensions instead (negative values already returned above), which is `len(dims) - set`; that condition is symmetric under `set -> len(dims) - set`, so every input gets the same verdict and the same error. |
