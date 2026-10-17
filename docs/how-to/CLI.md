<!--
SPDX-FileCopyrightText: 2026 Damián Búho <damian.buho@proton.me>
SPDX-License-Identifier: MIT
-->

# pf-ci Command Reference

`pf-ci` lowers an `org.projectfile.ci` signal DAG into vendor CI workflows.

## Commands

### `pf-ci resolve`

Emit the vendor-neutral job model as JSON (the template input).

```text
pf-ci resolve [-pf PATH] [-verbose|-q]
```

- `-pf PATH` — projectfile path, or `-` for a YAML/JSON document on stdin (default: auto-discover).
- `-verbose` — show operational log lines.
- `-q`, `-quiet` — mute progress lines.

### `pf-ci generate`

Render the job model to a target’s workflow YAML, or (`-check`) verify the committed workflow still matches the source DAG.

```text
pf-ci generate -target gha|forgejo|lefthook [-pf PATH] [-o PATH] [-check|-n] [-force=false] [-verbose|-q]
```

- `-target` — render target: `gha`, `forgejo`, or `lefthook`.
- `-o PATH` — output directory for per-goal workflow files (default: the target’s vendor dir); a single path for lefthook.
- `-check` — freshness gate: exit non-zero if the committed workflow drifted.
- `-n`, `-dry-run` — print the write/remove plan and change nothing.
- `-f`, `-force` — remove orphaned generated workflows (default true).
- `-pf PATH` — projectfile path, or `-` for stdin.

### Shared flags

Every command also takes these:

- `-offline` — refuse network; use cache and embedded data.
- `-timeout DURATION` — per-attempt network timeout (default `10s`).
- `-timing` — print elapsed time, request count and exit code at the end of the run.

Each flag also accepts its double-dash spelling (`--quiet`, `--out PATH`).

### `pf-ci version`

Print the resolver version (the codegen provenance stamp).

### `pf-ci help [COMMAND]`

Print top-level help, or one command’s flags.

## Environment

- `PF_CI_WORKFLOW_EXT` — committed-workflow file extension, `.yaml` or `.yml` (default `.yaml`).
- `PF_CLI_VERBOSE` — same effect as `-verbose`.

## Exit codes

- `0` — success.
- `1` — any other failure.
- `2` — bad invocation (unknown command, flag or target).
- `3` — `-check` found a stale, missing or orphaned workflow.
- `4` — the projectfile is absent, unreadable or invalid.

## Common errors

- `pf-ci: no projectfile found in .` — run from a project root or pass `-pf PATH`.
- `pf-ci: freshness check: … is stale — regenerate with pf-ci generate` — regenerate and commit.
