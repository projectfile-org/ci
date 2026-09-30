<!--
SPDX-FileCopyrightText: 2026 Damián Búho <damian.buho@proton.me>
SPDX-License-Identifier: MIT
pf-cli-managed: yes
-->

<!-- textlint-disable terminology,common-misspellings -->

[English](../../README.md) · [Español](../es/README.md)

# Projectfile CI Resolver

pf-ci перетворює незалежний від постачальників сигнальний DAG org.projectfile.ci на конкретні файли CI-робочих процесів для Forgejo Actions і GitHub Actions, диспетчеризуючи кожен лист провайдера до його рецепту в projectfile/actions. Бінарник-компаньйон pf-cli в інструментарії projectfile.org.

[![Stand with Ukraine](https://raw.githubusercontent.com/vshymanskyy/StandWithUkraine/main/badges/StandWithUkraine.svg)](https://damian-buho.github.io/support-ukraine/) [![Projectfile inside](https://badges.kiota.ch/static/v1?label=projectfile&message=inside&labelColor=0d0d0d&color=8c6723&style=flat-square)](https://projectfile.org) [![License](https://badges.kiota.ch/static/v1?label=license&message=MIT&color=1e5913&style=flat-square)](LICENSE) [![Commit style](https://badges.kiota.ch/static/v1?label=commits&message=conventional%20v1.0.0&color=1877aa&style=flat-square)](https://www.conventionalcommits.org/uk/v1.0.0/) ![Workflow](https://badges.kiota.ch/static/v1?label=workflow&message=git-flow&color=1877aa&style=flat-square) [![Versioning](https://badges.kiota.ch/static/v1?label=versioning&message=semantic%20v2.0.0&color=1877aa&style=flat-square)](https://semver.org/lang/uk/) [![Cosign](https://badges.kiota.ch/static/v1?label=cosign&message=enabled&color=1e5913&style=flat-square)](https://docs.sigstore.dev/cosign/verifying/verify/) [![PRs welcome](https://badges.kiota.ch/static/v1?label=PRs&message=welcome&color=1e5913&style=flat-square)](CONTRIBUTING.md) [![Citation](https://badges.kiota.ch/static/v1?label=citation&message=cff&color=1877aa&style=flat-square)](CITATION.cff) [![REUSE compliance](https://api.reuse.software/badge/codeberg.org/projectfile/ci)](https://api.reuse.software/info/codeberg.org/projectfile/ci)

[![Last commit on kiota.ch](https://badges.kiota.ch/gitea/last-commit/projectfile/ci?gitea_url=https://kiota.ch&label=last%20commit%20on%20kiota.ch&style=flat-square)](https://kiota.ch/projectfile/ci) [![Last commit on Codeberg](https://badges.kiota.ch/gitea/last-commit/projectfile/ci?gitea_url=https://codeberg.org&label=last%20commit%20on%20Codeberg&style=flat-square)](https://codeberg.org/projectfile/ci) [![Last commit on GitHub](https://badges.kiota.ch/github/last-commit/projectfile-org/ci?label=last%20commit%20on%20GitHub&style=flat-square)](https://github.com/projectfile-org/ci)

[![Publish pipeline on GitHub](https://github.com/projectfile-org/ci/actions/workflows/published.yaml/badge.svg?style=flat-square)](https://github.com/projectfile-org/ci/actions) [![Vulnerability audit on GitHub](https://github.com/projectfile-org/ci/actions/workflows/audited.yaml/badge.svg?style=flat-square)](https://github.com/projectfile-org/ci/actions) [![Dependency freshness on GitHub](https://github.com/projectfile-org/ci/actions/workflows/check-outdated.yaml/badge.svg?style=flat-square)](https://github.com/projectfile-org/ci/actions) [![Analysis sweep on GitHub](https://github.com/projectfile-org/ci/actions/workflows/analyze.yaml/badge.svg?style=flat-square)](https://github.com/projectfile-org/ci/actions)

[![Publish pipeline on kiota.ch](https://kiota.ch/projectfile/ci/badges/workflows/published.yaml/badge.svg?style=flat-square)](https://kiota.ch/projectfile/ci/actions) [![Vulnerability audit on kiota.ch](https://kiota.ch/projectfile/ci/badges/workflows/audited.yaml/badge.svg?style=flat-square)](https://kiota.ch/projectfile/ci/actions) [![Dependency freshness on kiota.ch](https://kiota.ch/projectfile/ci/badges/workflows/check-outdated.yaml/badge.svg?style=flat-square)](https://kiota.ch/projectfile/ci/actions) [![Analysis sweep on kiota.ch](https://kiota.ch/projectfile/ci/badges/workflows/analyze.yaml/badge.svg?style=flat-square)](https://kiota.ch/projectfile/ci/actions)

## Можливості

- Пониження DAG CI до робочих процесів

Див. [FEATURES.md](FEATURES.md), щоб переглянути повний перелік.

## Що надає цей проєкт

- **Виконуваний файл** `pf-ci` — команда `pf-ci`
- **Образ контейнера** `ghcr.io/projectfile-org/ci:latest`
- **Образ контейнера** `damianbuho/projectfile-ci:latest`

## Встановлення

### Образ контейнера

Завантажте опублікований образ контейнера:

#### Завантажити з GHCR — linux/amd64, linux/arm64, linux/riscv64

```sh
docker pull ghcr.io/projectfile-org/ci:latest
```

#### Завантажити з DockerHub — linux/amd64

```sh
docker pull damianbuho/projectfile-ci:latest
```

Стабільні випуски також публікують теґи `X.Y.Z`, `X.Y` і `X` — завантажте той рівень точності, який хочете зафіксувати.

Якщо наведені вище реєстри недоступні, завантажте з джерела:

#### Завантажити з Kiota — linux/amd64

```sh
docker pull kiota.ch/projectfile/ci:latest
```

### Готовий бінарний файл

Завантажте готовий бінарний файл для своєї платформи з останнього випуску на GitHub:

#### Завантажити для linux/amd64

```sh
curl --fail --location --output pf-ci https://github.com/projectfile-org/ci/releases/latest/download/pf-ci-linux-amd64 && chmod +x pf-ci
./pf-ci --help
```

#### Завантажити для linux/arm64

```sh
curl --fail --location --output pf-ci https://github.com/projectfile-org/ci/releases/latest/download/pf-ci-linux-arm64 && chmod +x pf-ci
./pf-ci --help
```

#### Завантажити для linux/riscv64

```sh
curl --fail --location --output pf-ci https://github.com/projectfile-org/ci/releases/latest/download/pf-ci-linux-riscv64 && chmod +x pf-ci
./pf-ci --help
```

#### Завантажити для darwin/amd64

```sh
curl --fail --location --output pf-ci https://github.com/projectfile-org/ci/releases/latest/download/pf-ci-darwin-amd64 && chmod +x pf-ci
./pf-ci --help
```

#### Завантажити для darwin/arm64

```sh
curl --fail --location --output pf-ci https://github.com/projectfile-org/ci/releases/latest/download/pf-ci-darwin-arm64 && chmod +x pf-ci
./pf-ci --help
```

## Використання

Lower the CI DAG to a forge’s workflow files:

```sh
pf-ci resolve
pf-ci generate -target forgejo
pf-ci generate -target gha -check
```

## Збирання

Клонуйте репозиторій разом із підмодулями:

```sh
git clone --recurse-submodules https://codeberg.org/projectfile/ci ci && cd ci
```

Зберіть образ контейнера локально:

```sh
make container-build
```

- [Довідник із Makefile](../how-to/MAKEFILE.md)

Виконайте `make` без аргументів для типової цілі; виконайте `make help`, щоб переглянути всі цілі.

Для локального циклу розробки `make dev-container` піднімає dev-container.

Точки входу конвеєра:

- `make analyze` — Запускає важкий аналіз (мутаційне тестування, бенчмарки)
- `make audited` — Повторно сканує закріплені залежності й опубліковані артефакти на нові вразливості
- `make check-outdated` — Звітує про кожну закріплену залежність, що відстає від upstream
- `make ready-to-publish` — Запускає псевдо-CI локально — збирає, тестує й сканує без публікації

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
