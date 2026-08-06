<!--
SPDX-FileCopyrightText: 2026 Damián Búho <damian.buho@proton.me>

SPDX-License-Identifier: MIT
-->

# CI DAG to workflow lowering

- Reads the CI declaration (a DAG of nodes, needs, tools and matrix) from a projectfile document and lowers it into concrete forge workflow files.
- Renders the resolved DAG, or emits Forgejo Actions and GitHub Actions workflow files per target.
- A drift gate verifies committed workflows match the declared DAG without writing.
