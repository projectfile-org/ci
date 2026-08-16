<!--
SPDX-FileCopyrightText: 2026 Damián Búho <damian.buho@proton.me>
SPDX-License-Identifier: MIT
pf-cli-managed: yes
-->

<!-- textlint-disable terminology,common-misspellings -->

[English](../../README.md) · [Español](../es/README.md)

# Projectfile CI Resolver

Перетворює DAG org.projectfile.ci на CI-робочі процеси

[![Stand with Ukraine](https://raw.githubusercontent.com/vshymanskyy/StandWithUkraine/main/badges/StandWithUkraine.svg)](https://damian-buho.github.io/support-ukraine/) [![License](https://img.shields.io/static/v1?label=license&message=MIT&color=4c1&style=flat-square)](LICENSE) ![Commit style](https://img.shields.io/static/v1?label=commits&message=conventional&color=blue&style=flat-square) ![Workflow](https://img.shields.io/static/v1?label=workflow&message=git-flow&color=blue&style=flat-square) ![Versioning](https://img.shields.io/static/v1?label=versioning&message=semantic&color=blue&style=flat-square) [![PRs welcome](https://img.shields.io/static/v1?label=PRs&message=welcome&color=4c1&style=flat-square)](CONTRIBUTING.md) [![Citation](https://img.shields.io/static/v1?label=citation&message=cff&color=blue&style=flat-square)](CITATION.cff) [![REUSE compliance](https://api.reuse.software/badge/codeberg.org/projectfile/ci-resolver)](https://api.reuse.software/info/codeberg.org/projectfile/ci-resolver)

[![Last commit](https://img.shields.io/gitea/last-commit/projectfile/ci-resolver?gitea_url=https://codeberg.org&style=flat-square)](https://codeberg.org/projectfile/ci-resolver)

[![Build status on kiota.ch](https://kiota.ch/projectfile/ci-resolver/badges/workflows/published.yaml/badge.svg)](https://kiota.ch/projectfile/ci-resolver/actions)

## Можливості

- Пониження DAG CI до робочих процесів

Див. [FEATURES.md](FEATURES.md), щоб переглянути повний перелік.

## Що надає цей проєкт

- **Виконуваний файл** `pf-ci`
- **Образ контейнера** `ghcr.io/damian-buho/projectfile/ci-resolver:latest`
- **Образ контейнера** `docker.io/damianbuho/projectfile-ci-resolver:latest`

## Встановлення

Завантажте опублікований образ контейнера:

```sh
docker pull ghcr.io/damian-buho/projectfile/ci-resolver:latest
docker pull docker.io/damianbuho/projectfile-ci-resolver:latest
```

Якщо наведені вище реєстри недоступні, завантажте з джерела:

```sh
docker pull kiota.ch/projectfile/ci-resolver:latest
```

## Використання

Lower the CI DAG to a forge’s workflow files:

```sh
pf-ci resolve
pf-ci generate -target forgejo
pf-ci generate -target gha -check
```

## Збирання

- [Довідник із Makefile](../MAKEFILE.md)

Точки входу конвеєра:

- `make analyze` — Run the heavy analysis sweep (mutation testing, benchmarks)
- `make audited` — Re-scan the pinned dependencies and published artifacts for new vulnerabilities
- `make check-outdated` — Report every pinned dependency that lags upstream
- `make ready-to-publish` — Run the pseudo-CI pipeline locally — build, test and scan, without publishing

Виконайте `make` без аргументів для типової цілі; виконайте `make help`, щоб переглянути всі цілі.

Для локального циклу розробки `make dev-container` піднімає dev-container.

## Політики

- [Як зробити внесок](CONTRIBUTING.md)
- [Політика безпеки](SECURITY.md)
- [Як отримати підтримку](SUPPORT.md)
- [Кодекс поведінки](CODE_OF_CONDUCT.md)

## Посилання

### Проєкт

- [Специфікація Projectfile](https://projectfile.org)
- [Projectfile CI Resolver на Codeberg](https://codeberg.org/projectfile/ci-resolver)
- [Projectfile CI Resolver на GitHub](https://github.com/damian-buho/projectfile-ci-resolver)
- [Projectfile CI Resolver на kiota.ch](https://kiota.ch/projectfile/ci-resolver)
- [Issues на Codeberg](https://codeberg.org/projectfile/ci-resolver/issues)
- [Issues на GitHub](https://github.com/damian-buho/projectfile-ci-resolver/issues)

### Інше

- [Від автора](https://dbuho.me)

## Ліцензія

Цей проєкт ліцензовано на умовах MIT — див. файл [LICENSE](LICENSE) для подробиць.

*Згенеровано з projectfile ([дізнатися як](https://projectfile.org/how-to/readme))*
<!-- textlint-enable -->
