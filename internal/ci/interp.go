// SPDX-FileCopyrightText: 2026 Damián Búho <damian.buho@proton.me>
//
// SPDX-License-Identifier: MIT

package ci

import (
	"fmt"
	"strings"

	"kiota.ch/projectfile/core/v2/pkg/projectfile"
)

// interpolator resolves BRACED `${<pf-path>}` generation-time references inside a
// tool INVOCATION (a `run` command, a positional-args string, a `set-env` value,
// or a string-form build-arg) against the merged projectfile document. It is the
// single home of the `${…}` mechanic the resolver applies before render — the
// forge-plane twin of the m6e reader's `pf-cli get` pass (Law 3: one neutral rule,
// two engine spellings).
type interpolator struct {
	doc      *projectfile.Document
	basename string // finalized image basename, for the image.* synthetic split
}

// interpolate scans s and resolves every braced `${…}` reference at generation
// time. The rules (design D2/D4/D10):
//   - BRACED-ONLY: a bare `$VAR` is left untouched (a runtime shell variable);
//     only `${…}` is intercepted (the same discipline pf-cli's `get --expand-env`
//     applies, so `$schema` survives).
//   - `$${…}` renders a LITERAL `${…}`: the `$$`→`$` Make/compose idiom hands a
//     RUNTIME `${VAR}` through to the shell (the one `dc-up-d` timeout case).
//   - MISSING → EMPTY (D4, reversed 2026-07-10): an unresolvable `${…}` collapses
//     to the empty string — the tool runs with that argument UNSET, never a hard
//     error. A shared-preset tool carrying a ref (lighthouse's frontpage URL) stays
//     harmless when THIS project never declares that artifact, mirroring the m6e
//     reader's `pf get || true` twin (Law 3: one neutral rule, two engine spellings).
//     The cost: a genuine typo silently drops the arg instead of failing loudly.
//
// SELECTOR / PROJECTION (`${…[kind=binary]…}` / `${…[]…}`) fanning to multiple
// positional args is DEFERRED: it needs core's widened `pkg/fieldpath.Resolve`,
// which lands with the (currently blocked) core v1.0.2 publish. Until then a `[` in
// a reference is refused loudly (never silently mis-resolved). Every real fleet ref
// is a plain dotted path (`org.projectfile.artifacts.<name>.path|url`) or an
// image.* synthetic, all of which resolve on the pinned core v1.0.0 surface.
func (ip interpolator) interpolate(s string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] != '$' {
			b.WriteByte(s[i])
			i++
			continue
		}
		// s[i] == '$'
		switch {
		case i+1 < len(s) && s[i+1] == '$':
			// `$$` escape: collapse to a single literal '$'. The following `{…}`
			// (if any) is then emitted VERBATIM by the normal loop — the `${` was
			// split across the escape boundary, so it is never interpolated. A
			// `$${VAR}` thus passes a runtime `${VAR}` through untouched.
			b.WriteByte('$')
			i += 2
		case i+1 < len(s) && s[i+1] == '{':
			// A pf-path never contains `}` (keys, `[k=v]` selectors, `[]`/[N]
			// brackets — no braces), so the matching close is the next `}`.
			rel := strings.IndexByte(s[i+2:], '}')
			if rel < 0 {
				return "", fmt.Errorf("unterminated ${…} reference at %q", s[i:])
			}
			expr := s[i+2 : i+2+rel]
			val, err := ip.resolve(expr)
			if err != nil {
				return "", err
			}
			b.WriteString(val)
			i += 2 + rel + 1
		default:
			// bare `$` (not `$$` or `${`) — a runtime shell var, untouched.
			b.WriteByte('$')
			i++
		}
	}
	return b.String(), nil
}

// resolve computes one `${expr}` reference. The three `image.*` addresses are
// SYNTHETIC (derived from the image basename split, not real document paths — the
// same split the retired `{get:}` identity args used). Every other expr is a DOTTED
// extension path (`org.projectfile.<ns>.<key>…<leaf>`), resolved against the merged
// doc via the same Extension walk read.go's subtree uses (the pinned-core v1.0.0
// surface). A miss at any hop resolves to EMPTY (D4, reversed) — an absent artifact
// or a typo leaves the arg UNSET, never a hard error. Only the deferred selector
// `[k=v]` / projection `[]` (see interpolate) is still refused loudly here: it is a
// capability gap on pinned core v1.0.0, NOT a data miss, and refusing it keeps the
// forge plane from silently diverging from the m6e reader, which resolves the full
// grammar.
func (ip interpolator) resolve(expr string) (string, error) {
	switch expr {
	case "image.basename":
		return ip.basename, nil
	case "image.namespace":
		ns, _ := basenameParts(ip.basename)
		return ns, nil
	case "image.name":
		_, name := basenameParts(ip.basename)
		return name, nil
	}
	if strings.ContainsAny(expr, "[]") {
		return "", fmt.Errorf("reference ${%s}: selector/projection is not yet supported "+
			"(needs core pkg/fieldpath.Resolve, pending the core v1.0.2 publish)", expr)
	}
	ns, keys := splitExtPath(expr)
	m, ok := projectfile.Extension(ip.doc, ns)
	if !ok {
		return "", nil // unresolved → empty (D4 reversed): extension absent, leave the arg unset
	}
	var v any = m
	for _, k := range keys {
		mm, ok := v.(map[string]any)
		if !ok {
			return "", nil // a non-map hop cannot continue → unresolved → empty
		}
		if v, ok = mm[k]; !ok {
			return "", nil // key absent → unresolved → empty
		}
	}
	return formatScalar(v), nil
}

// formatScalar renders a resolved leaf to the wire string a `run`/arg expects: a
// bare string verbatim, any other scalar via %v. A path or url (the only artifact
// leaves) is always a string.
func formatScalar(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}

// basenameParts splits an image basename into its (namespace, name) halves — the
// last `/`-separated label as the namespace, the rest (minus any `:tag`) as the
// name. Shared home of the split the `${image.namespace}` / `${image.name}`
// synthetics resolve (previously the render lowering's `{get:}` arm).
func basenameParts(image string) (namespace, name string) {
	name = image
	if i := strings.LastIndex(name, "/"); i >= 0 {
		namespace, name = name[:i], name[i+1:]
	}
	if i := strings.LastIndex(name, ":"); i >= 0 {
		name = name[:i]
	}
	return namespace, name
}

// interpolateRefs resolves every `${<pf-path>}` reference in the subtree's tool
// invocations (run, set-env values, string-form build-args) and node positional
// args against the merged doc, IN PLACE. Called once by Load after the image
// basename is finalized. An unresolved reference now resolves to EMPTY (D4, reversed
// 2026-07-10): a shared-preset ref to an artifact THIS project omits leaves the arg
// unset rather than failing the generate. Only a malformed ref (an unterminated
// `${`, or the deferred `[…]` selector) still errors.
func (r *Reader) interpolateRefs(st *Subtree, basename string) error {
	ip := interpolator{doc: r.doc, basename: basename}
	for name, man := range st.Tools {
		var err error
		if man.Run, err = ip.interpolate(man.Run); err != nil {
			return fmt.Errorf("tool %q run: %w", name, err)
		}
		for k, v := range man.EnvSet {
			if man.EnvSet[k], err = ip.interpolate(v); err != nil {
				return fmt.Errorf("tool %q set-env %s: %w", name, k, err)
			}
		}
		for i := range man.Args {
			if man.Args[i].Source != SourceLiteral {
				continue
			}
			if man.Args[i].Ref, err = ip.interpolate(man.Args[i].Ref); err != nil {
				return fmt.Errorf("tool %q arg %s: %w", name, man.Args[i].Name, err)
			}
		}
		st.Tools[name] = man // Manifest is a value in the map — write the Run change back
	}
	for nn, node := range st.Nodes {
		for i := range node.Needs {
			if !node.Needs[i].HasArgs {
				continue
			}
			v, err := ip.interpolate(node.Needs[i].Args)
			if err != nil {
				return fmt.Errorf("node %q needs %q args: %w", nn, node.Needs[i].Target, err)
			}
			node.Needs[i].Args = v
		}
	}
	return nil
}

// resolveReleaseAssetPaths populates Manifest.ReleaseAssetPath for every
// forgejo-release action tool from org.projectfile.artifacts: the single entry
// with kind=binary. The action can't take a `run:` (it owns its steps), so the
// path is resolved here — Load-time, against the same merged doc
// interpolateRefs walks — and stashed on the manifest for render to thread as a
// `with:` input. The action then suffixes it per cell (→ <path>-${GOOS}-${GOARCH}).
//
// Fail-closed: a forgejo-release tool with zero or more than one kind=binary
// artifact is an ambiguous attach target, not a silent miss — the action would
// either attach nothing or the wrong binary. `kind` is advisory vocabulary, so
// the match is on the literal `binary` value only.
func (r *Reader) resolveReleaseAssetPaths(st *Subtree) error {
	var releaseTools int
	for name, man := range st.Tools {
		if man.Action != ActionForgejoRelease {
			continue
		}
		releaseTools++
		path, count, err := r.singleBinaryArtifactPath()
		if err != nil {
			return fmt.Errorf("tool %q: %w", name, err)
		}
		if count == 0 {
			return fmt.Errorf("tool %q: no org.projectfile.artifacts entry with kind=binary — declare one to attach", name)
		}
		if count > 1 {
			return fmt.Errorf("tool %q: %d org.projectfile.artifacts entries have kind=binary — forgejo-release attaches exactly one; the multi-binary case needs the core v1.0.2 selector", name, count)
		}
		man.ReleaseAssetPath = path
		st.Tools[name] = man // value in map — write back
	}
	return nil
}

// singleBinaryArtifactPath walks org.projectfile.artifacts (a map of named
// artifacts) and returns the .path of the single kind=binary entry, the count
// of kind=binary entries found (0, 1, or >1), and any lookup error. The count
// lets the caller distinguish the three fail-closed cases without re-walking.
func (r *Reader) singleBinaryArtifactPath() (path string, count int, err error) {
	ext, ok := projectfile.Extension(r.doc, "org.projectfile")
	if !ok {
		return "", 0, nil // no extension → no artifacts → count 0 (caller fail-closes)
	}
	arts, ok := ext["artifacts"].(map[string]any)
	if !ok {
		return "", 0, nil // artifacts absent or not a map → count 0
	}
	for _, raw := range arts {
		art, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if fmt.Sprintf("%v", art["kind"]) != "binary" {
			continue
		}
		p, _ := art["path"].(string)
		count++
		if count == 1 {
			path = p
		}
	}
	return path, count, nil
}
