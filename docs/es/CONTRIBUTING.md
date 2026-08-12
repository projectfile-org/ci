<!--
SPDX-FileCopyrightText: 2026 Damián Búho <damian.buho@proton.me>
SPDX-License-Identifier: MIT
pf-cli-managed: yes
-->

<!-- textlint-disable terminology -->
[English](CONTRIBUTING.md) · [Українська](docs/uk/CONTRIBUTING.md)

# Cómo contribuir a Projectfile CI Resolver

Ante todo, ¡gracias por dedicar tu tiempo a contribuir! ❤️

Toda forma de contribución se agradece y se valora. Consulta la [Tabla de contenidos](#tabla-de-contenidos) para conocer las distintas maneras de ayudar y cómo las gestiona este proyecto. Lee la sección correspondiente antes de hacer tu aportación: nos facilitará mucho el trabajo a quienes mantenemos el proyecto y hará la experiencia más agradable para todo el mundo.

> Y si el proyecto te gusta pero no tienes tiempo para contribuir, no pasa nada. Hay otras formas sencillas de apoyarlo y mostrar tu agradecimiento, que también nos harían mucha ilusión:
>
> - Dale una estrella al proyecto en [codeberg.org](https://codeberg.org/projectfile/ci-resolver)
> - Dale una estrella al proyecto en [github.com](https://github.com/damian-buho/projectfile-ci-resolver)
> - Menciona este proyecto en el readme del tuyo
> - Habla del proyecto en encuentros locales y cuéntaselo a tus amistades y colegas
> - Sigue [author (Damián Búho) on Mastodon](mastodon.social/@damianbuho)
> - Sigue [author (Damián Búho) on GitHub](damian-buho)
> - Sigue [author (Damián Búho) on Codeberg](damian-buho)
> - Sigue [author (Damián Búho) on LinkedIn](damian-buho)
> - Visit [author’s (Damián Búho) site](https://dbuho.me)

<!-- omit in toc -->
## Tabla de contenidos

- [Tengo una pregunta](#tengo-una-pregunta)
- [Cómo informar de fallos](#cómo-informar-de-fallos)
- [Cómo sugerir mejoras](#cómo-sugerir-mejoras)
- [Convenciones](#convenciones)
- [Mejorar la documentación](#mejorar-la-documentación)

## Tengo una pregunta

Antes de preguntar, revisa [docs/es/SUPPORT.md](docs/es/SUPPORT.md): explica dónde conseguir ayuda y cómo formular preguntas eficaces.

> ### Aviso legal
>
> Al contribuir a este proyecto, debes aceptar que eres la autoría del 100 % del contenido, que dispones de los derechos necesarios sobre él y que el contenido que aportas puede publicarse bajo la licencia del proyecto.

## Cómo informar de fallos

<!-- omit in toc -->
### Antes de enviar un informe de fallo

Un buen informe de fallo evita que otras personas tengan que perseguirte para obtener más información. Por eso te pedimos que investigues con cuidado, reúnas información y describas el problema con detalle. Completa estos pasos por adelantado para ayudarnos a corregir cualquier posible fallo lo antes posible.

- Asegúrate de estar usando la última versión.
- Comprueba que se trata realmente de un fallo y no de un error por tu parte, por ejemplo, componentes o versiones incompatibles del entorno (asegúrate de haber leído la [documentación](https://projectfile.org). Si lo que buscas es ayuda, consulta [docs/es/SUPPORT.md](docs/es/SUPPORT.md)).
- Para ver si otras personas han tenido (y quizá ya resuelto) el mismo problema, comprueba que no exista ya un informe de tu fallo o error en el [gestor de incidencias](https://codeberg.org/projectfile/ci-resolver/issues?q=label%3Abug).
- Busca también en internet (incluido Stack Overflow) por si el problema ya se ha comentado fuera de la comunidad.
- Reúne información sobre el fallo:
    - Traza de la pila (traceback)
    - Sistema operativo, plataforma y versión (Windows, Linux, macOS, x86, ARM)
    - Versión del intérprete, compilador, SDK, entorno de ejecución o gestor de paquetes, según lo que resulte relevante.
    - Si es posible, la entrada que usaste y la salida obtenida
    - ¿Puedes reproducir el problema de forma fiable? ¿Y con versiones anteriores?

<!-- omit in toc -->
### ¿Cómo envío un buen informe de fallo?

> Nunca informes de problemas de seguridad, vulnerabilidades o fallos que incluyan información sensible en el gestor de incidencias ni en ningún otro lugar público. Los fallos sensibles deben enviarse por correo a <damian.buho@proton.me>.

Usamos [incidencias](https://codeberg.org/projectfile/ci-resolver/issues) para seguir fallos y errores. Si te topas con un problema en el proyecto:

- Abre una [incidencia](https://codeberg.org/projectfile/ci-resolver/issues/new). (Como todavía no podemos saber si se trata de un fallo, te pedimos que no lo des por hecho ni etiquetes la incidencia.)
- Explica el comportamiento que esperabas y el que se produjo realmente.
- Aporta todo el contexto posible y describe los *pasos de reproducción* que otra persona pueda seguir para recrear el problema. Esto suele incluir tu código. En un buen informe conviene aislar el problema y crear un caso de prueba reducido.
- Incluye la información que reuniste en el apartado anterior.

Una vez enviada:

- El equipo del proyecto etiquetará la incidencia según corresponda.
- Alguien del equipo intentará reproducir el problema con los pasos que aportaste. Si no hay pasos de reproducción o no existe una forma evidente de reproducirlo, el equipo te los pedirá y marcará la incidencia como `needs-repro`. Los fallos con la etiqueta `needs-repro` no se abordarán hasta que puedan reproducirse.
- Si el equipo logra reproducir el problema, se marcará como `needs-fix`, posiblemente junto a otras etiquetas (como `critical`), y quedará a la espera de que [alguien lo implemente](#convenciones).

## Cómo sugerir mejoras

Esta sección te guía para enviar una propuesta de mejora para Projectfile CI Resolver, **tanto funcionalidades completamente nuevas como pequeñas mejoras de lo existente**. Seguir estas indicaciones ayudará a quienes mantienen el proyecto y a la comunidad a entender tu propuesta y a encontrar sugerencias relacionadas.

<!-- omit in toc -->
### Antes de enviar una propuesta de mejora

- Asegúrate de estar usando la última versión.
- Lee con atención la [documentación](https://projectfile.org) y averigua si la funcionalidad ya existe, quizá mediante alguna configuración concreta.
- Haz una [búsqueda](https://codeberg.org/projectfile/ci-resolver/issues) para ver si ya se ha propuesto. Si es así, comenta en la incidencia existente en lugar de abrir una nueva.
- Valora si tu idea encaja con el alcance y los objetivos del proyecto. Te corresponde a ti argumentar de forma convincente el valor de esta funcionalidad. Ten en cuenta que buscamos funcionalidades útiles para la mayoría de quienes usan el proyecto, no solo para un grupo reducido. Si tu propuesta se dirige a una minoría, plantéate escribir una extensión o biblioteca complementaria.

<!-- omit in toc -->
### ¿Cómo envío una buena propuesta de mejora?

Las propuestas de mejora se gestionan como [incidencias](https://codeberg.org/projectfile/ci-resolver/issues).

- Usa un **título claro y descriptivo** que identifique la propuesta.
- Describe la mejora sugerida **paso a paso** y con el mayor detalle posible.
- **Describe el comportamiento actual** y **explica qué comportamiento esperabas** y por qué. Aquí también puedes indicar qué alternativas no te sirven.
- **Explica por qué esta mejora sería útil** para la mayoría de quienes usan Projectfile CI Resolver. También puedes señalar otros proyectos que lo hayan resuelto mejor y que sirvan de inspiración.

## Convenciones

- **Workflow:** Git Flow — feature branches from `develop`, release branches from `develop`, hotfix branches from `main`.
- **Commits:** [Conventional Commits](https://www.conventionalcommits.org/)
- **Versioning:** [Semantic Versioning](https://semver.org/)

## Mejorar la documentación

La documentación está en [https://projectfile.org](https://projectfile.org). Correcciones, mejoras y secciones nuevas son bienvenidas: abre una solicitud de incorporación contra las fuentes de la documentación.

<!-- textlint-enable -->
