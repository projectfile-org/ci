#!/bin/sh

# SPDX-FileCopyrightText: 2026 Damián Búho <damian.buho@proton.me>
#
# SPDX-License-Identifier: MIT

set -eu

# build-binaries.sh — cross-compile pf-ci for one GOOS/GOARCH, inject the release
# version, and write the stripped binary to dist/pf-ci-<goos>-<goarch>. Called once
# per matrix axis by the build-binaries CI tool, which supplies the Go SDK (b19/go
# image, or host go on a prefer-local run). core is a pinned module, so its SPDX
# texts arrive in the module zip — nothing to fetch before the build.

# Version resolution, portable across planes: the forge runner exports the tag as
# $GITHUB_REF_NAME (forwarded via the build-binaries tool's env: list); the make
# plane has neither an arg nor that env, so fall back to a git-derived / dev version.
# Keeps a local build honestly stamped, mirroring m6e-version.sh (tag → short sha → dev).
version="${1:-${GITHUB_REF_NAME:-}}"
case "${version}" in
	'' | *['{}']*) version="$(git describe --tags --always --dirty 2>/dev/null || echo dev)" ;;
esac

# GOHOSTOS/GOHOSTARCH is the real host even under a cross-compile (GOOS/GOARCH set
# only the TARGET). Default the target to the host when the matrix bound no cell —
# the make-plane host build; the forge matrix sets GOOS/GOARCH per cell.
hostos="$(go env GOHOSTOS)"
hostarch="$(go env GOHOSTARCH)"
goos="${GOOS:-${hostos}}"
goarch="${GOARCH:-${hostarch}}"
out="dist/pf-ci-${goos}-${goarch}"

log() { printf '[build-binaries] %s\n' "$*" >&2; }
log "building pf-ci ${version} for ${goos}/${goarch}"

mkdir -p dist
go build -ldflags="-s -w -X main.version=${version}"      \
         -o "${out}" .

# Stable unsuffixed copy (dist/pf-ci) referenced by a fixed path from both the
# matrixed gsa (it analyzes ${org.projectfile.artifacts.go-binary.path} in every
# cell) and the local install. Every cell drops it so gsa always finds this cell's
# binary; releases attach only the suffixed asset, and the make plane runs the host
# cell alone, so the install stays host-native.
log "copying ${out} -> dist/pf-ci"
cp "${out}" dist/pf-ci
