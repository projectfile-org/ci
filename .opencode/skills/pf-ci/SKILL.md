---
name: pf-ci
description: Lower an org.projectfile.ci signal DAG into vendor CI workflows. Use when generating or checking CI workflows with pf-ci.
---

# pf-ci

Run `pf-ci resolve` to emit the job model, or `pf-ci generate -target gha|forgejo|lefthook` to render workflows.

- `-check` fails if committed workflows drifted, with exit code 3.
- `-pf PATH` names a projectfile (default auto-discover).
- `-offline` refuses network; `-timing` reports elapsed time and request count.
- `PF_CI_WORKFLOW_EXT` controls `.yaml` vs `.yml`.
- `docs/how-to/CLI.md` lists every flag and exit code.

Install: `go install projectfile.org/projectfile/ci/v2/cmd/pf-ci@latest`, or `make install-binary` in a checkout.
