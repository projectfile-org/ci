#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Damián Búho <damian.buho@proton.me>
#
# SPDX-License-Identifier: MIT
#
# What we are doing: one-shot migration for the per-manifest `registry:` field.
# A registry-RELATIVE image (`d9t/go-tools`, no `:tag`) used to be prefixed with a
# single global registry token; now each workspace tool row names its OWN registry
# var (the agnostic default is Docker Hub — no prefix). The var follows the
# namespace of the image: b19/* -> B19_DOCKER_REGISTRY, d9t/* -> D9T_DOCKER_REGISTRY,
# projectfile/* -> PF_DOCKER_REGISTRY. A `:tag`-bearing image (hadolint/hadolint:
# v2.14.0) is a complete external ref and gets NO registry. Inserts `registry: <VAR>`
# on the line right after the matching `image:` (same indent). Idempotent.
import re, sys

NS_VAR = {
    "b19": "B19_DOCKER_REGISTRY",
    "d9t": "D9T_DOCKER_REGISTRY",
    "projectfile": "PF_DOCKER_REGISTRY",
}

IMAGE = re.compile(r"^(\s*)image:\s*(\S+)\s*$")

def migrate(text: str) -> str:
    lines = text.splitlines(keepends=True)
    out = []
    for i, line in enumerate(lines):
        out.append(line)
        m = IMAGE.match(line)
        if not m:
            continue
        indent, img = m.group(1), m.group(2)
        if ":" in img or "/" not in img:
            continue                       # verbatim external ref / single-name
        ns = img.split("/")[0]
        var = NS_VAR.get(ns)
        if var is None:
            continue                       # unknown namespace => Docker Hub default
        nxt = lines[i + 1] if i + 1 < len(lines) else ""
        if re.match(rf"^{indent}registry:\s", nxt):
            continue                       # already migrated (idempotent)
        out.append(f"{indent}registry: {var}\n")
    return "".join(out)

if __name__ == "__main__":
    changed = 0
    for path in sys.argv[1:]:
        with open(path, encoding="utf-8") as fh:
            src = fh.read()
        dst = migrate(src)
        if dst != src:
            with open(path, "w", encoding="utf-8") as fh:
                fh.write(dst)
            changed += 1
            print(f"migrated {path}")
        else:
            print(f"unchanged {path}")
    print(f"-- {changed} file(s) changed")
