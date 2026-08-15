<!--
SPDX-FileCopyrightText: 2026 Damián Búho <damian.buho@proton.me>

SPDX-License-Identifier: MIT
-->

# Reducción del DAG de CI a flujos de trabajo

- Lee la declaración de CI (un DAG de nodos, needs, herramientas y matriz) desde un documento projectfile y la reduce a archivos concretos de flujo de trabajo para la forja.
- Muestra el DAG resuelto, o emite archivos de flujo de trabajo de Forgejo Actions y GitHub Actions por objetivo.
- Una puerta de control de deriva verifica que los flujos de trabajo confirmados coincidan con el DAG declarado, sin escribir nada.
