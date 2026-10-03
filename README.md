<!--
SPDX-FileCopyrightText: 2026 Damián Búho <damian.buho@proton.me>
SPDX-License-Identifier: MIT
pf-cli-managed: yes
-->

[Español](docs/es/README.md) · [Українська](docs/uk/README.md)

# Projectfile CI Resolver

pf-ci lowers the vendor-neutral org.projectfile.ci signal DAG to concrete CI workflow files for Forgejo Actions and GitHub Actions, dispatching each provider leaf to its recipe in projectfile/actions. Companion binary to pf-cli for the projectfile.org tooling.

[![Stand with Ukraine](https://raw.githubusercontent.com/vshymanskyy/StandWithUkraine/main/badges/StandWithUkraine.svg)](https://damian-buho.github.io/support-ukraine/) [![Projectfile inside](https://badges.kiota.ch/static/v1?label=projectfile&message=inside&labelColor=0d0d0d&color=8c6723&style=flat-square)](https://projectfile.org) [![License](https://badges.kiota.ch/static/v1?label=license&message=MIT&color=1e5913&style=flat-square)](LICENSE) [![Cosign](https://badges.kiota.ch/static/v1?label=cosign&message=enabled&color=1e5913&style=flat-square)](https://docs.sigstore.dev/cosign/verifying/verify/) [![PRs welcome](https://badges.kiota.ch/static/v1?label=PRs&message=welcome&color=1e5913&style=flat-square)](CONTRIBUTING.md) [![REUSE compliance](https://api.reuse.software/badge/codeberg.org/projectfile/ci)](https://api.reuse.software/info/codeberg.org/projectfile/ci)

[![Last commit on kiota.ch](https://badges.kiota.ch/gitea/last-commit/projectfile/ci?gitea_url=https://kiota.ch&label=last%20commit%20on%20kiota.ch&style=flat-square)](https://kiota.ch/projectfile/ci) [![Last commit on Codeberg](https://badges.kiota.ch/gitea/last-commit/projectfile/ci?gitea_url=https://codeberg.org&label=last%20commit%20on%20Codeberg&style=flat-square)](https://codeberg.org/projectfile/ci) [![Last commit on GitHub](https://badges.kiota.ch/github/last-commit/projectfile-org/ci?label=last%20commit%20on%20GitHub&style=flat-square)](https://github.com/projectfile-org/ci)

[![Publish pipeline on GitHub](https://github.com/projectfile-org/ci/actions/workflows/published.yaml/badge.svg?style=flat-square)](https://github.com/projectfile-org/ci/actions) [![Vulnerability audit on GitHub](https://github.com/projectfile-org/ci/actions/workflows/audited.yaml/badge.svg?style=flat-square)](https://github.com/projectfile-org/ci/actions) [![Dependency freshness on GitHub](https://github.com/projectfile-org/ci/actions/workflows/check-outdated.yaml/badge.svg?style=flat-square)](https://github.com/projectfile-org/ci/actions) [![Analysis sweep on GitHub](https://github.com/projectfile-org/ci/actions/workflows/analyzed.yaml/badge.svg?style=flat-square)](https://github.com/projectfile-org/ci/actions)

[![Publish pipeline on kiota.ch](https://kiota.ch/projectfile/ci/badges/workflows/published.yaml/badge.svg?style=flat-square)](https://kiota.ch/projectfile/ci/actions) [![Vulnerability audit on kiota.ch](https://kiota.ch/projectfile/ci/badges/workflows/audited.yaml/badge.svg?style=flat-square)](https://kiota.ch/projectfile/ci/actions) [![Dependency freshness on kiota.ch](https://kiota.ch/projectfile/ci/badges/workflows/check-outdated.yaml/badge.svg?style=flat-square)](https://kiota.ch/projectfile/ci/actions) [![Analysis sweep on kiota.ch](https://kiota.ch/projectfile/ci/badges/workflows/analyzed.yaml/badge.svg?style=flat-square)](https://kiota.ch/projectfile/ci/actions)

## Features

- CI DAG to workflow lowering

See [Features](docs/FEATURES.md) for the full list.

## What this provides

- **Executable** `pf-ci` — command `pf-ci`
- **Container image** `ghcr.io/projectfile-org/ci:latest`
- **Container image** `damianbuho/projectfile-ci:latest`

## Installation

### Container image

Pull the published container image:

#### Pull from GHCR — linux/amd64, linux/arm64, linux/riscv64

```sh
docker pull ghcr.io/projectfile-org/ci:latest
```

#### Pull from DockerHub — linux/amd64

```sh
docker pull damianbuho/projectfile-ci:latest
```

Stable releases also publish `X.Y.Z`, `X.Y` and `X` tags — pull the precision you want to pin.

If the registries above are unreachable, pull from the origin instead:

#### Pull from Kiota — linux/amd64

```sh
docker pull kiota.ch/projectfile/ci:latest
```

### Prebuilt binary

Download the prebuilt binary for your platform from the latest GitHub release:

```sh
curl --fail --location --output pf-ci https://github.com/projectfile-org/ci/releases/latest/download/pf-ci-$(uname -s | tr A-Z a-z)-$(uname -m | sed -e s/x86_64/amd64/ -e s/aarch64/arm64/) && chmod +x pf-ci
./pf-ci --help
```

Published for: `linux/amd64`, `linux/arm64`, `linux/riscv64`, `darwin/amd64`, `darwin/arm64`

## Usage

Lower the CI DAG to a forge’s workflow files:

```sh
pf-ci resolve
pf-ci generate -target forgejo
pf-ci generate -target gha -check
```

## Building

Clone the repository with its submodules:

```sh
git clone --recurse-submodules https://codeberg.org/projectfile/ci ci && cd ci
```

Build the container image locally:

```sh
make container-build
```

- [Makefile reference](docs/how-to/MAKEFILE.md)

Run `make` with no arguments for the default target; run `make help` to list every target.

For the local dev loop, `make dev-container` brings up the dev-container.

Pipeline entry points:

- `make analyzed` — Run the heavy analysis sweep (mutation testing, benchmarks)
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
