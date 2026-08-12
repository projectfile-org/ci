<!--
SPDX-FileCopyrightText: 2026 Damián Búho <damian.buho@proton.me>
SPDX-License-Identifier: MIT
pf-cli-managed: yes
-->

<!-- textlint-disable terminology -->

[English](README.md) · [Español](docs/es/README.md)

# Projectfile CI Resolver

Lowers org.projectfile.ci DAG to CI workflows

[![Stand with Ukraine](https://raw.githubusercontent.com/vshymanskyy/StandWithUkraine/main/badges/StandWithUkraine.svg)](https://damian-buho.github.io/support-ukraine/) [![License](https://img.shields.io/static/v1?label=license&message=MIT&color=4c1&style=flat-square)](LICENSE) ![Commit style](https://img.shields.io/static/v1?label=commits&message=conventional&color=blue&style=flat-square) ![Workflow](https://img.shields.io/static/v1?label=workflow&message=git-flow&color=blue&style=flat-square) ![Versioning](https://img.shields.io/static/v1?label=versioning&message=semantic&color=blue&style=flat-square) [![PRs welcome](https://img.shields.io/static/v1?label=PRs&message=welcome&color=4c1&style=flat-square)](CONTRIBUTING.md) [![Citation](https://img.shields.io/static/v1?label=citation&message=cff&color=blue&style=flat-square)](CITATION.cff) [![REUSE compliance](https://api.reuse.software/badge/codeberg.org/projectfile/ci-resolver)](https://api.reuse.software/info/codeberg.org/projectfile/ci-resolver)

[![Last commit](https://img.shields.io/gitea/last-commit/projectfile/ci-resolver?gitea_url=https://codeberg.org&style=flat-square)](https://codeberg.org/projectfile/ci-resolver)

[![Build status on kiota.ch](https://kiota.ch/projectfile/ci-resolver/badges/workflows/published.yaml/badge.svg)](https://kiota.ch/projectfile/ci-resolver/actions)

## Можливості

- CI DAG to workflow lowering

Див. [Можливості](FEATURES.md), щоб переглянути повний перелік.

## Що надає цей проєкт

- **Виконуваний файл** `pf-ci`
- **Образ контейнера** `kiota.ch/projectfile/ci-resolver:latest`

## Встановлення

Pull the published container image:

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

- [Довідник із Makefile](docs/MAKEFILE.md)

Точки входу конвеєра:

- `make analyze` — Run the heavy analysis sweep (mutation testing, benchmarks)
- `make audited` — Re-scan the pinned dependencies and published artifacts for new vulnerabilities
- `make check-outdated` — Report every pinned dependency that lags upstream
- `make ready-to-publish` — Run the pseudo-CI pipeline locally — build, test and scan, without publishing

Виконайте `make` без аргументів для типової цілі; виконайте `make help`, щоб переглянути всі цілі.

Для локального циклу розробки `make dev-container` піднімає dev-container.

## Політики

- [Як зробити внесок](docs/uk/CONTRIBUTING.md)
- [Політика безпеки](docs/uk/SECURITY.md)
- [Як отримати підтримку](docs/uk/SUPPORT.md)
- [Кодекс поведінки](docs/uk/CODE_OF_CONDUCT.md)

## Посилання

- [специфікація projectfile](https://projectfile.org)
- [Projectfile CI Resolver on Codeberg](https://codeberg.org/projectfile/ci-resolver)
- [Projectfile CI Resolver on GitHub](https://github.com/damian-buho/projectfile-ci-resolver)
- [Projectfile CI Resolver on kiota.ch](https://kiota.ch/projectfile/ci-resolver)
- [Issues on Codeberg](https://codeberg.org/projectfile/ci-resolver/issues)
- [Issues on GitHub](https://github.com/damian-buho/projectfile-ci-resolver/issues)

## Ліцензія

Цей проєкт ліцензовано на умовах MIT — див. файл [LICENSE](LICENSE) для подробиць.

<!-- textlint-enable -->
