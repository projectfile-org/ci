<!--
SPDX-FileCopyrightText: 2026 Damián Búho <damian.buho@proton.me>
SPDX-License-Identifier: MIT
pf-cli-managed: yes
-->

<!-- textlint-disable terminology,common-misspellings -->

[English](../../README.md) · [Українська](../uk/README.md)

# Projectfile CI Resolver

pf-ci convierte el DAG de señales neutro respecto al proveedor org.projectfile.ci en archivos de flujo de trabajo de CI concretos para Forgejo Actions y GitHub Actions, despachando cada hoja de proveedor a su receta en projectfile/actions. Binario compañero de pf-cli para las herramientas de projectfile.org.

[![Stand with Ukraine](https://raw.githubusercontent.com/vshymanskyy/StandWithUkraine/main/badges/StandWithUkraine.svg)](https://damian-buho.github.io/support-ukraine/) [![Projectfile inside](https://badges.kiota.ch/static/v1?label=projectfile&message=inside&labelColor=0d0d0d&color=8c6723&style=flat-square)](https://projectfile.org) [![License](https://badges.kiota.ch/static/v1?label=license&message=MIT&color=1e5913&style=flat-square)](LICENSE) [![Commit style](https://badges.kiota.ch/static/v1?label=commits&message=conventional%20v1.0.0&color=1877aa&style=flat-square)](https://www.conventionalcommits.org/es/v1.0.0/) ![Workflow](https://badges.kiota.ch/static/v1?label=workflow&message=git-flow&color=1877aa&style=flat-square) [![Versioning](https://badges.kiota.ch/static/v1?label=versioning&message=semantic%20v2.0.0&color=1877aa&style=flat-square)](https://semver.org/lang/es/) [![Cosign](https://badges.kiota.ch/static/v1?label=cosign&message=enabled&color=1e5913&style=flat-square)](https://docs.sigstore.dev/cosign/verifying/verify/) [![PRs welcome](https://badges.kiota.ch/static/v1?label=PRs&message=welcome&color=1e5913&style=flat-square)](CONTRIBUTING.md) [![Citation](https://badges.kiota.ch/static/v1?label=citation&message=cff&color=1877aa&style=flat-square)](CITATION.cff) [![REUSE compliance](https://api.reuse.software/badge/codeberg.org/projectfile/ci)](https://api.reuse.software/info/codeberg.org/projectfile/ci)

[![Last commit on kiota.ch](https://badges.kiota.ch/gitea/last-commit/projectfile/ci?gitea_url=https://kiota.ch&label=last%20commit%20on%20kiota.ch&style=flat-square)](https://kiota.ch/projectfile/ci) [![Last commit on Codeberg](https://badges.kiota.ch/gitea/last-commit/projectfile/ci?gitea_url=https://codeberg.org&label=last%20commit%20on%20Codeberg&style=flat-square)](https://codeberg.org/projectfile/ci) [![Last commit on GitHub](https://badges.kiota.ch/github/last-commit/projectfile-org/ci?label=last%20commit%20on%20GitHub&style=flat-square)](https://github.com/projectfile-org/ci)

[![Publish pipeline on GitHub](https://github.com/projectfile-org/ci/actions/workflows/published.yaml/badge.svg?style=flat-square)](https://github.com/projectfile-org/ci/actions) [![Vulnerability audit on GitHub](https://github.com/projectfile-org/ci/actions/workflows/audited.yaml/badge.svg?style=flat-square)](https://github.com/projectfile-org/ci/actions) [![Dependency freshness on GitHub](https://github.com/projectfile-org/ci/actions/workflows/check-outdated.yaml/badge.svg?style=flat-square)](https://github.com/projectfile-org/ci/actions) [![Analysis sweep on GitHub](https://github.com/projectfile-org/ci/actions/workflows/analyze.yaml/badge.svg?style=flat-square)](https://github.com/projectfile-org/ci/actions)

[![Publish pipeline on kiota.ch](https://kiota.ch/projectfile/ci/badges/workflows/published.yaml/badge.svg?style=flat-square)](https://kiota.ch/projectfile/ci/actions) [![Vulnerability audit on kiota.ch](https://kiota.ch/projectfile/ci/badges/workflows/audited.yaml/badge.svg?style=flat-square)](https://kiota.ch/projectfile/ci/actions) [![Dependency freshness on kiota.ch](https://kiota.ch/projectfile/ci/badges/workflows/check-outdated.yaml/badge.svg?style=flat-square)](https://kiota.ch/projectfile/ci/actions) [![Analysis sweep on kiota.ch](https://kiota.ch/projectfile/ci/badges/workflows/analyze.yaml/badge.svg?style=flat-square)](https://kiota.ch/projectfile/ci/actions)

## Características

- Reducción del DAG de CI a flujos de trabajo

Consulta [FEATURES.md](FEATURES.md) para ver la lista completa.

## Qué entrega este proyecto

- **Ejecutable** `pf-ci` — comando `pf-ci`
- **Imagen de contenedor** `ghcr.io/projectfile-org/ci:latest`
- **Imagen de contenedor** `damianbuho/projectfile-ci:latest`

## Instalación

### Imagen de contenedor

Descarga la imagen de contenedor publicada:

#### Descargar de GHCR — linux/amd64, linux/arm64, linux/riscv64

```sh
docker pull ghcr.io/projectfile-org/ci:latest
```

#### Descargar de DockerHub — linux/amd64

```sh
docker pull damianbuho/projectfile-ci:latest
```

Las versiones estables también publican las etiquetas `X.Y.Z`, `X.Y` y `X`: descarga el nivel de precisión que quieras fijar.

Si los registros anteriores no están disponibles, descarga desde el origen:

#### Descargar de Kiota — linux/amd64

```sh
docker pull kiota.ch/projectfile/ci:latest
```

### Binario precompilado

Descarga el binario precompilado para tu plataforma desde la última versión en GitHub:

#### Descargar para linux/amd64

```sh
curl --fail --location --output pf-ci https://github.com/projectfile-org/ci/releases/latest/download/pf-ci-linux-amd64 && chmod +x pf-ci
./pf-ci --help
```

#### Descargar para linux/arm64

```sh
curl --fail --location --output pf-ci https://github.com/projectfile-org/ci/releases/latest/download/pf-ci-linux-arm64 && chmod +x pf-ci
./pf-ci --help
```

#### Descargar para linux/riscv64

```sh
curl --fail --location --output pf-ci https://github.com/projectfile-org/ci/releases/latest/download/pf-ci-linux-riscv64 && chmod +x pf-ci
./pf-ci --help
```

#### Descargar para darwin/amd64

```sh
curl --fail --location --output pf-ci https://github.com/projectfile-org/ci/releases/latest/download/pf-ci-darwin-amd64 && chmod +x pf-ci
./pf-ci --help
```

#### Descargar para darwin/arm64

```sh
curl --fail --location --output pf-ci https://github.com/projectfile-org/ci/releases/latest/download/pf-ci-darwin-arm64 && chmod +x pf-ci
./pf-ci --help
```

## Uso

Lower the CI DAG to a forge’s workflow files:

```sh
pf-ci resolve
pf-ci generate -target forgejo
pf-ci generate -target gha -check
```

## Compilación

Clona el repositorio con sus submódulos:

```sh
git clone --recurse-submodules https://codeberg.org/projectfile/ci ci && cd ci
```

Construye la imagen de contenedor en local:

```sh
make container-build
```

- [Referencia del Makefile](../how-to/MAKEFILE.md)

Ejecuta `make` sin argumentos para el destino predeterminado; ejecuta `make help` para listar todos los destinos.

Para el bucle de desarrollo local, `make dev-container` levanta el dev-container.

Puntos de entrada de la canalización:

- `make analyze` — Ejecuta el análisis pesado (pruebas de mutación, benchmarks)
- `make audited` — Vuelve a escanear las dependencias fijadas y los artefactos publicados en busca de vulnerabilidades nuevas
- `make check-outdated` — Informa de cada dependencia fijada que va por detrás de su versión upstream
- `make ready-to-publish` — Ejecuta localmente el pipeline pseudo-CI — compila, prueba y escanea, sin publicar

## Políticas

- [Cómo contribuir](CONTRIBUTING.md)
- [Política de seguridad](SECURITY.md)
- [Cómo obtener ayuda](SUPPORT.md)
- [Código de conducta](CODE_OF_CONDUCT.md)
- [Política sobre IA y LLM](AI_POLICY.md)

## Enlaces

- [Especificación de Projectfile](https://projectfile.org)

## Licencia

Este proyecto se publica bajo la licencia MIT — consulta el archivo [LICENSE](LICENSE) para más detalles.

<!-- textlint-enable -->
