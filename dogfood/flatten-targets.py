#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Damián Búho <damian.buho@proton.me>
#
# SPDX-License-Identifier: MIT
#
# What we are doing: one-shot grammar migration for the CI manifest membership
# key. The old shape nested per-target membership under a `targets:` wrapper:
#
#     m6e:
#       prefer-local: true
#     targets:
#       gha: false
#       forgejo: false
#
# The new shape RAISES the target keys to sit beside `m6e:` (target name is the
# key, single level — no `targets:` entity):
#
#     m6e:
#       prefer-local: true
#     gha: false
#     forgejo: false
#
# The transform finds each `^<indent>targets:` line and hoists its more-indented
# children up by exactly that one nesting step (2 spaces), dropping the wrapper.
# Byte-faithful elsewhere (minimise the diff). Idempotent: a file with no
# `targets:` block is rewritten unchanged.
import sys

def leading(s: str) -> int:
    return len(s) - len(s.lstrip(" "))

def flatten(text: str) -> str:
    lines = text.splitlines(keepends=True)
    out, i = [], 0
    while i < len(lines):
        line = lines[i]
        stripped = line.strip()
        if stripped == "targets:" and line.endswith(("targets:\n", "targets:")):
            indent = leading(line)
            i += 1  # drop the `targets:` wrapper line
            # Hoist its children (strictly more-indented, non-blank) up by 2.
            while i < len(lines) and lines[i].strip() != "" and leading(lines[i]) > indent:
                out.append(lines[i][2:])
                i += 1
            continue
        out.append(line)
        i += 1
    return "".join(out)

if __name__ == "__main__":
    changed = 0
    for path in sys.argv[1:]:
        with open(path, encoding="utf-8") as fh:
            src = fh.read()
        dst = flatten(src)
        if dst != src:
            with open(path, "w", encoding="utf-8") as fh:
                fh.write(dst)
            changed += 1
            print(f"flattened {path}")
        else:
            print(f"unchanged {path}")
    print(f"-- {changed} file(s) changed")
