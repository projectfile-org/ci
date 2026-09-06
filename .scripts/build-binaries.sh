#!/bin/sh

# SPDX-FileCopyrightText: 2026 Damián Búho <damian.buho@proton.me>
#
# SPDX-License-Identifier: MIT

set -eu

# build-binaries.sh — cross-compile pf-ci for one GOOS/GOARCH, writing dist/pf-ci-<goos>-<goarch>.

version="${1:-${GITHUB_REF_NAME:-}}"
case "${version}" in
	'' | *['{}']*) version="$(git describe --tags --always --dirty 2>/dev/null || echo dev)" ;; # empty or an unexpanded template placeholder
esac

hostos="$(go env GOHOSTOS)"   # the real host even under a cross-compile
hostarch="$(go env GOHOSTARCH)"
goos="${GOOS:-${hostos}}"     # the matrix sets these per cell; unset means a host-native build
goarch="${GOARCH:-${hostarch}}"
out="dist/pf-ci-${goos}-${goarch}"

log() { printf '[build-binaries] %s\n' "$*" >&2; }
log "building pf-ci ${version} for ${goos}/${goarch}"

mkdir -p dist
go build -ldflags="-s -w -X main.version=${version}"      \
         -o "${out}" .

printf '%s\n' "${out}"
