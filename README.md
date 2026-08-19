<!--
SPDX-FileCopyrightText: 2026 Damián Búho <damian.buho@proton.me>
SPDX-License-Identifier: MIT
pf-cli-managed: yes
-->

[Español](docs/es/README.md) · [Українська](docs/uk/README.md)

# Projectfile CI Resolver

Lowers the org.projectfile.ci DAG to CI workflows

[![Stand with Ukraine](https://raw.githubusercontent.com/vshymanskyy/StandWithUkraine/main/badges/StandWithUkraine.svg)](https://damian-buho.github.io/support-ukraine/) [![License](https://img.shields.io/static/v1?label=license&message=MIT&color=4c1&style=flat-square)](LICENSE) ![Commit style](https://img.shields.io/static/v1?label=commits&message=conventional&color=blue&style=flat-square) ![Workflow](https://img.shields.io/static/v1?label=workflow&message=git-flow&color=blue&style=flat-square) ![Versioning](https://img.shields.io/static/v1?label=versioning&message=semantic&color=blue&style=flat-square) [![PRs welcome](https://img.shields.io/static/v1?label=PRs&message=welcome&color=4c1&style=flat-square)](CONTRIBUTING.md) [![Citation](https://img.shields.io/static/v1?label=citation&message=cff&color=blue&style=flat-square)](CITATION.cff) [![REUSE compliance](https://api.reuse.software/badge/codeberg.org/projectfile/ci-resolver)](https://api.reuse.software/info/codeberg.org/projectfile/ci-resolver)

[![Last commit](https://img.shields.io/gitea/last-commit/projectfile/ci-resolver?gitea_url=https://codeberg.org&style=flat-square)](https://codeberg.org/projectfile/ci-resolver)

[![Build status on kiota.ch](https://kiota.ch/projectfile/ci-resolver/badges/workflows/published.yaml/badge.svg)](https://kiota.ch/projectfile/ci-resolver/actions)

## Features

- CI DAG to workflow lowering

See [FEATURES.md](FEATURES.md) for the full list.

## What this provides

- **Executable** `pf-ci`
- **Container image** `ghcr.io/damian-buho/projectfile/ci-resolver:latest`
- **Container image** `docker.io/damianbuho/projectfile-ci-resolver:latest`

## Installation

Pull the published container image:

### Pull from GHCR

```sh
docker pull ghcr.io/damian-buho/projectfile/ci-resolver:latest
```

### Pull from DockerHub

```sh
docker pull docker.io/damianbuho/projectfile-ci-resolver:latest
```

Stable releases also publish `X.Y.Z`, `X.Y` and `X` tags — pull the precision you want to pin.

If the registries above are unreachable, pull from the origin instead:

### Pull from Kiota

```sh
docker pull kiota.ch/projectfile/ci-resolver:latest
```

## Usage

Lower the CI DAG to a forge’s workflow files:

```sh
pf-ci resolve
pf-ci generate -target forgejo
pf-ci generate -target gha -check
```

## Building

Run `make` with no arguments for the default target; run `make help` to list every target.

For the local dev loop, `make dev-container` brings up the dev-container.

Pipeline entry points:

- `make analyze` — Run the heavy analysis sweep (mutation testing, benchmarks)
- `make audited` — Re-scan the pinned dependencies and published artifacts for new vulnerabilities
- `make check-outdated` — Report every pinned dependency that lags upstream
- `make ready-to-publish` — Run the pseudo-CI pipeline locally — build, test and scan, without publishing

## Policies

- [How to contribute](CONTRIBUTING.md)
- [Security policy](SECURITY.md)
- [Getting support](SUPPORT.md)
- [Code of Conduct](CODE_OF_CONDUCT.md)
- [AI and LLM Policy](AI_POLICY.md)

## Links

- [Projectfile Specification](https://projectfile.org)

## License

This project is licensed under MIT — see the [LICENSE](LICENSE) file for details.
