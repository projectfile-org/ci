<!--
SPDX-FileCopyrightText: 2026 Damián Búho <damian.buho@proton.me>
SPDX-License-Identifier: MIT
pf-cli-managed: yes
-->

<!-- textlint-disable terminology,common-misspellings -->

[English](../../README.md) · [Українська](../uk/README.md)

# Projectfile CI Resolver

Convierte el DAG de org.projectfile.ci en flujos de trabajo de CI

[![Stand with Ukraine](https://raw.githubusercontent.com/vshymanskyy/StandWithUkraine/main/badges/StandWithUkraine.svg)](https://damian-buho.github.io/support-ukraine/) [![License](https://img.shields.io/static/v1?label=license&message=MIT&color=4c1&style=flat-square)](LICENSE) ![Commit style](https://img.shields.io/static/v1?label=commits&message=conventional&color=blue&style=flat-square) ![Workflow](https://img.shields.io/static/v1?label=workflow&message=git-flow&color=blue&style=flat-square) ![Versioning](https://img.shields.io/static/v1?label=versioning&message=semantic&color=blue&style=flat-square) [![PRs welcome](https://img.shields.io/static/v1?label=PRs&message=welcome&color=4c1&style=flat-square)](CONTRIBUTING.md) [![Citation](https://img.shields.io/static/v1?label=citation&message=cff&color=blue&style=flat-square)](CITATION.cff) [![REUSE compliance](https://api.reuse.software/badge/codeberg.org/projectfile/ci-resolver)](https://api.reuse.software/info/codeberg.org/projectfile/ci-resolver)

[![Last commit](https://img.shields.io/gitea/last-commit/projectfile/ci-resolver?gitea_url=https://codeberg.org&style=flat-square)](https://codeberg.org/projectfile/ci-resolver)

[![Build status on kiota.ch](https://kiota.ch/projectfile/ci-resolver/badges/workflows/published.yaml/badge.svg)](https://kiota.ch/projectfile/ci-resolver/actions)

## Características

- Reducción del DAG de CI a flujos de trabajo

Consulta [FEATURES.md](FEATURES.md) para ver la lista completa.

## Qué entrega este proyecto

- **Ejecutable** `pf-ci`
- **Imagen de contenedor** `ghcr.io/damian-buho/projectfile/ci-resolver:latest`
- **Imagen de contenedor** `docker.io/damianbuho/projectfile-ci-resolver:latest`

## Instalación

Descarga la imagen de contenedor publicada:

```sh
docker pull ghcr.io/damian-buho/projectfile/ci-resolver:latest
docker pull docker.io/damianbuho/projectfile-ci-resolver:latest
```

Si los registros anteriores no están disponibles, descarga desde el origen:

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

- [Referencia del Makefile](../MAKEFILE.md)

Puntos de entrada de la canalización:

- `make analyze` — Run the heavy analysis sweep (mutation testing, benchmarks)
- `make audited` — Re-scan the pinned dependencies and published artifacts for new vulnerabilities
- `make check-outdated` — Report every pinned dependency that lags upstream
- `make ready-to-publish` — Run the pseudo-CI pipeline locally — build, test and scan, without publishing

Ejecuta `make` sin argumentos para el destino predeterminado; ejecuta `make help` para listar todos los destinos.

Para el bucle de desarrollo local, `make dev-container` levanta el dev-container.

## Políticas

- [Cómo contribuir](CONTRIBUTING.md)
- [Política de seguridad](SECURITY.md)
- [Cómo obtener ayuda](SUPPORT.md)
- [Código de conducta](CODE_OF_CONDUCT.md)

## Enlaces

### Proyecto

- [Especificación de Projectfile](https://projectfile.org)
- [Projectfile CI Resolver en Codeberg](https://codeberg.org/projectfile/ci-resolver)
- [Projectfile CI Resolver en GitHub](https://github.com/damian-buho/projectfile-ci-resolver)
- [Projectfile CI Resolver en kiota.ch](https://kiota.ch/projectfile/ci-resolver)
- [Incidencias en Codeberg](https://codeberg.org/projectfile/ci-resolver/issues)
- [Incidencias en GitHub](https://github.com/damian-buho/projectfile-ci-resolver/issues)

### Otros

- [Del autor](https://dbuho.me)

## Licencia

Este proyecto se publica bajo la licencia MIT — consulta el archivo [LICENSE](LICENSE) para más detalles.

*Generado desde projectfile ([saber cómo](https://projectfile.org/how-to/readme))*
<!-- textlint-enable -->
