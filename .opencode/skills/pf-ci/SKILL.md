---
name: pf-ci
description: Lower an org.projectfile.ci signal DAG into vendor CI workflows. Use when generating or checking CI workflows with pf-ci.
---

# pf-ci

Run `pf-ci resolve` to emit the job model, or `pf-ci generate -target gha|forgejo|lefthook` to render workflows.

- `-check` fails if committed workflows drifted.
- `-pf PATH` names a projectfile (default auto-discover).
- `PF_CI_WORKFLOW_EXT` controls `.yaml` vs `.yml`.

Install: download the latest release binary to `~/.local/bin/pf-ci` or run `make install-binary`.
