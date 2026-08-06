#!/usr/bin/env bash

# SPDX-FileCopyrightText: 2026 Damián Búho <damian.buho@proton.me>
#
# SPDX-License-Identifier: MIT

  set -euo pipefail

  # shellcheck source=/dev/null
  . b19-i18n

  LDFLAGS="-s -w -X main.version=${M6E_VERSION:-dev}"

  b19-run "CI" "$(_ 'Download modules')" --     \
    go mod download

  b19-run "CI" "$(_ 'Build pf-ci')" --      \
    go build -ldflags="${LDFLAGS}" -o pf-ci .

  b19-strip "CI" pf-ci

  b19-run "CI" "$(_ 'Copy binary to export')" --      \
    mkdir -p /export/usr/local/bin

  b19-run "CI" "$(_p 'Copy %s to %s' "pf-ci" "/export/usr/local/bin")" --     \
    cp pf-ci /export/usr/local/bin
