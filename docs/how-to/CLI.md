<!--
SPDX-FileCopyrightText: 2026 Damián Búho <damian.buho@proton.me>
SPDX-License-Identifier: MIT
-->

# pf-ci Command Reference

`pf-ci` lowers an `org.projectfile.ci` signal DAG into vendor CI workflows.

## Commands

### `pf-ci resolve`

Emit the vendor-neutral job model as JSON (the template input).

```
pf-ci resolve [-pf PATH] [-verbose|-q]
```

- `-pf PATH` — projectfile path, or `-` for a YAML/JSON document on stdin (default: auto-discover).
- `-verbose` — show operational log lines.
- `-q`, `-quiet` — mute progress lines.

### `pf-ci generate`

Render the job model to a target’s workflow YAML, or (`-check`) verify the committed workflow still matches the source DAG.

```
pf-ci generate -target gha|forgejo|lefthook [-pf PATH] [-o PATH] [-check|-n] [-force=false] [-verbose|-q]
```

- `-target` — render target: `gha`, `forgejo`, or `lefthook`.
- `-o PATH` — output directory for per-goal workflow files (default: the target’s vendor dir); a single path for lefthook.
- `-check` — freshness gate: exit non-zero if the committed workflow drifted.
- `-n`, `-dry-run` — print the write/remove plan and change nothing.
- `-f`, `-force` — remove orphaned generated workflows (default true).
- `-pf PATH` — projectfile path, or `-` for stdin.

### `pf-ci version`

Print the resolver version (the codegen provenance stamp).

### `pf-ci help [COMMAND]`

Print top-level help, or one command’s flags.

## Environment

- `PF_CI_WORKFLOW_EXT` — committed-workflow file extension, `.yaml` or `.yml` (default `.yaml`).
- `PF_CLI_VERBOSE` — same effect as `-verbose`.

## Common errors

- `pf-ci: no projectfile found in .` — run from a project root or pass `-pf PATH`.
- `pf-ci: freshness check: … is stale — regenerate with pf-ci generate` — regenerate and commit.
