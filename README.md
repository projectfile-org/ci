<!--
SPDX-FileCopyrightText: 2026 Damián Búho <damian.buho@proton.me>
SPDX-License-Identifier: MIT
-->

<!-- pf-cli-managed: yes -->
# Projectfile CI Resolver

Lowers org.projectfile.ci DAG to CI workflows

[![License](https://img.shields.io/badge/license-MIT-4c1?style=flat-square)](LICENSE) [![PRs welcome](https://img.shields.io/badge/PRs-welcome-4c1?style=flat-square)](CONTRIBUTING.md) [![REUSE compliance](https://api.reuse.software/badge/codeberg.org/projectfile/ci-resolver)](https://api.reuse.software/info/codeberg.org/projectfile/ci-resolver)

[![Last commit](https://img.shields.io/gitea/last-commit/projectfile/ci-resolver?gitea_url=https://codeberg.org&style=flat-square)](https://codeberg.org/projectfile/ci-resolver)

[![Build status on kiota.ch](https://kiota.ch/projectfile/ci-resolver/badges/workflows/published.yaml/badge.svg)](https://kiota.ch/projectfile/ci-resolver/actions)

## Features

- CI DAG to workflow lowering

See [Features](FEATURES.md) for the full list.

## What this provides

- **Executable** `pf-ci`
- **Container image** `kiota.ch/projectfile/ci-resolver:latest`

## Installation

Pull the published container image:

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

- [Makefile reference](docs/MAKEFILE.md)

Pipeline entry points:

- `make analyze` — Run the heavy analysis sweep (mutation testing, benchmarks)
- `make audited` — Re-scan the pinned dependencies and published artifacts for new vulnerabilities
- `make check-outdated` — Report every pinned dependency that lags upstream
- `make published` — Build, test, scan and publish the release artifacts

## Policies

- [How to contribute](CONTRIBUTING.md)
- [Security policy](SECURITY.md)
- [Getting support](SUPPORT.md)
- [Code of Conduct](CODE_OF_CONDUCT.md)

## Links

### Project

- [Projectfile CI Resolver on Codeberg](https://codeberg.org/projectfile/ci-resolver)
- [Projectfile CI Resolver on GitHub](https://github.com/damian-buho/projectfile-ci-resolver)
- [Projectfile CI Resolver on kiota.ch](https://kiota.ch/projectfile/ci-resolver)
- [Issues on Codeberg](https://codeberg.org/projectfile/ci-resolver/issues)
- [Issues on GitHub](https://github.com/damian-buho/projectfile-ci-resolver/issues)

## License

This project is licensed under MIT — see the [LICENSE](LICENSE) file for details.
