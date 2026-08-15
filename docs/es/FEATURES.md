<!--
SPDX-FileCopyrightText: 2026 Damián Búho <damian.buho@proton.me>
SPDX-License-Identifier: MIT
-->

<!-- textlint-disable terminology,common-misspellings -->

[English](../../FEATURES.md) · [Українська](../uk/FEATURES.md)

# Características

## Características del proyecto

### Reducción del DAG de CI a flujos de trabajo

- Lee la declaración de CI (un DAG de nodos, needs, herramientas y matriz) desde un documento projectfile y la reduce a archivos concretos de flujo de trabajo para la forja.
- Muestra el DAG resuelto, o emite archivos de flujo de trabajo de Forgejo Actions y GitHub Actions por objetivo.
- Una puerta de control de deriva verifica que los flujos de trabajo confirmados coincidan con el DAG declarado, sin escribir nada.
<!-- textlint-enable -->
