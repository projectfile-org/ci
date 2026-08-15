<!--
SPDX-FileCopyrightText: 2026 Damián Búho <damian.buho@proton.me>
SPDX-License-Identifier: MIT
-->

<!-- textlint-disable terminology,common-misspellings -->

[English](../../FEATURES.md) · [Español](../es/FEATURES.md)

# Можливості

## Можливості проєкту

### Пониження DAG CI до робочих процесів

- Читає оголошення CI (DAG із вузлів, needs, інструментів та матриці) з документа projectfile і понижує його до конкретних файлів робочих процесів форжу.
- Виводить розв’язаний DAG або генерує файли робочих процесів Forgejo Actions і GitHub Actions для кожної цілі.
- Шлюз перевірки дрейфу підтверджує, що закомічені робочі процеси відповідають оголошеному DAG, без запису.
<!-- textlint-enable -->
