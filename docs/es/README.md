<!--
SPDX-FileCopyrightText: 2026 Damián Búho <damian.buho@proton.me>
SPDX-License-Identifier: MIT
pf-cli-managed: yes
-->

<!-- textlint-disable terminology -->

[English](README.md) · [Українська](docs/uk/README.md)

# Projectfile CI Resolver

Lowers org.projectfile.ci DAG to CI workflows

[![Stand with Ukraine](https://raw.githubusercontent.com/vshymanskyy/StandWithUkraine/main/badges/StandWithUkraine.svg)](https://damian-buho.github.io/support-ukraine/) [![License](https://img.shields.io/static/v1?label=license&message=MIT&color=4c1&style=flat-square)](LICENSE) ![Commit style](https://img.shields.io/static/v1?label=commits&message=conventional&color=blue&style=flat-square) ![Workflow](https://img.shields.io/static/v1?label=workflow&message=git-flow&color=blue&style=flat-square) ![Versioning](https://img.shields.io/static/v1?label=versioning&message=semantic&color=blue&style=flat-square) [![PRs welcome](https://img.shields.io/static/v1?label=PRs&message=welcome&color=4c1&style=flat-square)](CONTRIBUTING.md) [![Citation](https://img.shields.io/static/v1?label=citation&message=cff&color=blue&style=flat-square)](CITATION.cff) [![REUSE compliance](https://api.reuse.software/badge/codeberg.org/projectfile/ci-resolver)](https://api.reuse.software/info/codeberg.org/projectfile/ci-resolver)

[![Last commit](https://img.shields.io/gitea/last-commit/projectfile/ci-resolver?gitea_url=https://codeberg.org&style=flat-square)](https://codeberg.org/projectfile/ci-resolver)

[![Build status on kiota.ch](https://kiota.ch/projectfile/ci-resolver/badges/workflows/published.yaml/badge.svg)](https://kiota.ch/projectfile/ci-resolver/actions)

## Características

- CI DAG to workflow lowering

Consulta [Características](FEATURES.md) para ver la lista completa.

## Qué entrega este proyecto

- **Ejecutable** `pf-ci`
- **Imagen de contenedor** `kiota.ch/projectfile/ci-resolver:latest`

## Instalación

Pull the published container image:

```sh
docker pull kiota.ch/projectfile/ci-resolver:latest
```

## Uso

Lower the CI DAG to a forge’s workflow files:

```sh
pf-ci resolve
pf-ci generate -target forgejo
pf-ci generate -target gha -check
```

## Compilación

- [Referencia del Makefile](docs/MAKEFILE.md)

Puntos de entrada de la canalización:

- `make analyze` — Run the heavy analysis sweep (mutation testing, benchmarks)
- `make audited` — Re-scan the pinned dependencies and published artifacts for new vulnerabilities
- `make check-outdated` — Report every pinned dependency that lags upstream
- `make ready-to-publish` — Run the pseudo-CI pipeline locally — build, test and scan, without publishing

Ejecuta `make` sin argumentos para el destino predeterminado; ejecuta `make help` para listar todos los destinos.

Para el bucle de desarrollo local, `make dev-container` levanta el dev-container.

## Políticas

- [Cómo contribuir](docs/es/CONTRIBUTING.md)
- [Política de seguridad](docs/es/SECURITY.md)
- [Cómo obtener ayuda](docs/es/SUPPORT.md)
- [Código de conducta](docs/es/CODE_OF_CONDUCT.md)

## Enlaces

- [especificación de projectfile](https://projectfile.org)
- [Projectfile CI Resolver on Codeberg](https://codeberg.org/projectfile/ci-resolver)
- [Projectfile CI Resolver on GitHub](https://github.com/damian-buho/projectfile-ci-resolver)
- [Projectfile CI Resolver on kiota.ch](https://kiota.ch/projectfile/ci-resolver)
- [Issues on Codeberg](https://codeberg.org/projectfile/ci-resolver/issues)
- [Issues on GitHub](https://github.com/damian-buho/projectfile-ci-resolver/issues)

## Licencia

Este proyecto se publica bajo la licencia MIT — consulta el archivo [LICENSE](LICENSE) para más detalles.

<!-- textlint-enable -->
