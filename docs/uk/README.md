<!--
SPDX-FileCopyrightText: 2026 Damián Búho <damian.buho@proton.me>
SPDX-License-Identifier: MIT
pf-cli-managed: yes
-->

<!-- textlint-disable terminology,common-misspellings -->

[English](../../README.md) · [Español](../es/README.md)

# Projectfile CI Resolver

Перетворює DAG org.projectfile.ci на CI-робочі процеси

[![Stand with Ukraine](https://raw.githubusercontent.com/vshymanskyy/StandWithUkraine/main/badges/StandWithUkraine.svg)](https://damian-buho.github.io/support-ukraine/) [![Projectfile inside](https://badges.kiota.ch/static/v1?label=projectfile&message=inside&labelColor=0d0d0d&color=8c6723&style=flat-square)](https://projectfile.org) [![License](https://badges.kiota.ch/static/v1?label=license&message=MIT&color=1e5913&style=flat-square)](LICENSE) [![Commit style](https://badges.kiota.ch/static/v1?label=commits&message=conventional%20v1.0.0&color=1877aa&style=flat-square)](https://www.conventionalcommits.org/uk/v1.0.0/) ![Workflow](https://badges.kiota.ch/static/v1?label=workflow&message=git-flow&color=1877aa&style=flat-square) [![Versioning](https://badges.kiota.ch/static/v1?label=versioning&message=semantic%20v2.0.0&color=1877aa&style=flat-square)](https://semver.org/lang/uk/) [![PRs welcome](https://badges.kiota.ch/static/v1?label=PRs&message=welcome&color=1e5913&style=flat-square)](CONTRIBUTING.md) [![Cosign](https://badges.kiota.ch/static/v1?label=cosign&message=enabled&color=1e5913&style=flat-square)](https://docs.sigstore.dev/cosign/verifying/verify/) [![Citation](https://badges.kiota.ch/static/v1?label=citation&message=cff&color=1877aa&style=flat-square)](CITATION.cff) [![REUSE compliance](https://api.reuse.software/badge/github.com/projectfile-org/ci)](https://api.reuse.software/info/github.com/projectfile-org/ci)

[![Publish pipeline on GitHub](https://github.com/projectfile-org/ci/actions/workflows/published.yaml/badge.svg?style=flat-square)](https://github.com/projectfile-org/ci/actions) [![Vulnerability audit on GitHub](https://github.com/projectfile-org/ci/actions/workflows/audited.yaml/badge.svg?style=flat-square)](https://github.com/projectfile-org/ci/actions) [![Dependency freshness on GitHub](https://github.com/projectfile-org/ci/actions/workflows/check-outdated.yaml/badge.svg?style=flat-square)](https://github.com/projectfile-org/ci/actions) [![Analysis sweep on GitHub](https://github.com/projectfile-org/ci/actions/workflows/analyze.yaml/badge.svg?style=flat-square)](https://github.com/projectfile-org/ci/actions)

[![Last commit on GitHub](https://badges.kiota.ch/github/last-commit/projectfile-org/ci?label=last%20commit%20on%20GitHub&style=flat-square)](https://github.com/projectfile-org/ci) [![Last commit on kiota.ch](https://badges.kiota.ch/gitea/last-commit/projectfile/ci?gitea_url=https://kiota.ch&label=last%20commit%20on%20kiota.ch&style=flat-square)](https://kiota.ch/projectfile/ci)

[![Publish pipeline on kiota.ch](https://kiota.ch/projectfile/ci/badges/workflows/published.yaml/badge.svg?style=flat-square)](https://kiota.ch/projectfile/ci/actions) [![Vulnerability audit on kiota.ch](https://kiota.ch/projectfile/ci/badges/workflows/audited.yaml/badge.svg?style=flat-square)](https://kiota.ch/projectfile/ci/actions) [![Dependency freshness on kiota.ch](https://kiota.ch/projectfile/ci/badges/workflows/check-outdated.yaml/badge.svg?style=flat-square)](https://kiota.ch/projectfile/ci/actions) [![Analysis sweep on kiota.ch](https://kiota.ch/projectfile/ci/badges/workflows/analyze.yaml/badge.svg?style=flat-square)](https://kiota.ch/projectfile/ci/actions)

## Можливості

- Пониження DAG CI до робочих процесів

Див. [FEATURES.md](FEATURES.md), щоб переглянути повний перелік.

## Що надає цей проєкт

- **Виконуваний файл** `pf-ci`
- **Образ контейнера** `ghcr.io/projectfile-org/ci:latest`

## Підтримувані платформи

- `linux/amd64`
- `linux/arm64`
- `linux/riscv64`

## Встановлення

Завантажте опублікований образ контейнера:

### Завантажити з GHCR

```sh
docker pull ghcr.io/projectfile-org/ci:latest
```

Стабільні випуски також публікують теґи `X.Y.Z`, `X.Y` і `X` — завантажте той рівень точності, який хочете зафіксувати.

Якщо наведені вище реєстри недоступні, завантажте з джерела:

### Завантажити з Kiota

```sh
docker pull kiota.ch/projectfile/ci:latest
```

## Використання

Lower the CI DAG to a forge’s workflow files:

```sh
pf-ci resolve
pf-ci generate -target forgejo
pf-ci generate -target gha -check
```

## Збирання

Виконайте `make` без аргументів для типової цілі; виконайте `make help`, щоб переглянути всі цілі.

Для локального циклу розробки `make dev-container` піднімає dev-container.

Точки входу конвеєра:

- `make analyze` — Run the heavy analysis sweep (mutation testing, benchmarks)
- `make audited` — Re-scan the pinned dependencies and published artifacts for new vulnerabilities
- `make check-outdated` — Report every pinned dependency that lags upstream
- `make ready-to-publish` — Run the pseudo-CI pipeline locally — build, test and scan, without publishing

## Політики

- [Як зробити внесок](CONTRIBUTING.md)
- [Політика безпеки](SECURITY.md)
- [Як отримати підтримку](SUPPORT.md)
- [Кодекс поведінки](CODE_OF_CONDUCT.md)
- [Політика щодо ШІ та LLM](AI_POLICY.md)

## Посилання

- [Специфікація Projectfile](https://projectfile.org)

## Ліцензія

Цей проєкт ліцензовано на умовах MIT — див. файл [LICENSE](LICENSE) для подробиць.

<!-- textlint-enable -->
