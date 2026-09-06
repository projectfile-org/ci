#!/bin/sh

# SPDX-FileCopyrightText: 2026 Damián Búho <damian.buho@proton.me>
#
# SPDX-License-Identifier: MIT

set -eu

# install-binary.sh — build pf-ci host-native and install it into ~/.local/bin.

dst="${HOME}/.local/bin"

log() { printf '[install-binary] %s\n' "$*" >&2; }

# Build host-native (no version arg → build-binaries derives one from git).
log "building host-native pf-ci"
asset="$(.scripts/build-binaries.sh)"

mkdir -p "${dst}"
install -m 0755 "${asset}" "${dst}/pf-ci"
log "installed ${asset} -> ${dst}/pf-ci"
