// SPDX-FileCopyrightText: 2026 Damián Búho <damian.buho@proton.me>
//
// SPDX-License-Identifier: MIT

package ci

import (
	"fmt"
	"strings"

	"kiota.ch/projectfile/core/v2/pkg/interp"
	"kiota.ch/projectfile/core/v2/pkg/projectfile"
)

// imageScope is the subtree a reference is resolved against before the document
// root: the project's DECLARED image parts. It is the same scope the readme
// bridge and the m6e reader bind, which is what keeps one template meaning one
// thing in all three planes.
const imageScope = "org.projectfile.image"

// interpolator resolves BRACED `${<pf-path>}` generation-time references inside a
// tool INVOCATION (a `run` command, a positional-args string, a `set-env` value,
// or a string-form build-arg) against the merged projectfile document. It is the
// single home of the `${…}` mechanic the resolver applies before render — the
// forge-plane twin of the m6e reader's `pf-cli get` pass (Law 3: one neutral rule,
// two engine spellings).
type interpolator struct {
	doc *projectfile.Document
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
// A reference naming SEVERAL values (a `{kind=binary}` selector over a map) is
// left VERBATIM by core rather than collapsed to the first, so it reaches this
// plane unresolved and lands on the empty rule above. Fanning one reference out
// to several positional args is a render concern nothing declares yet.
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
			// Brace-BALANCED, not first-`}`. The address grammar spells a map selector
			// in curly braces (`artifacts{kind=binary}.path`), so a first-`}` scan ends
			// the reference INSIDE the selector and asks core to resolve the truncated
			// `artifacts{kind=binary`. Core leaves that verbatim, so the `${…}` reaches
			// the runner and the shell answers `bad substitution` — a failure that names
			// the workflow, never the fragment that wrote the address.
			expr, next, ok := braced(s, i)
			if !ok {
				return "", fmt.Errorf("unterminated ${…} reference at %q", s[i:])
			}
			val, err := ip.resolve(expr)
			if err != nil {
				return "", err
			}
			b.WriteString(val)
			i = next
		default:
			// bare `$` (not `$$` or `${`) — a runtime shell var, untouched.
			b.WriteByte('$')
			i++
		}
	}
	return b.String(), nil
}

// braced reads the `${…}` opening at i and returns the inner address plus the
// index just past its closing brace, counting nested braces so a `{k=v}`
// selector inside the address does not end the scan early. Quoted spans are
// skipped for the same reason fieldpath's own splitter skips them: a predicate
// value may carry a brace (`{label="a{b}"}`) and must not move the count. ok is
// false when the brace is never closed, which the caller reports as an error —
// core's twin instead emits the `$` as an ordinary character, because a badge
// URL may legitimately carry one.
func braced(s string, i int) (expr string, next int, ok bool) {
	depth, inQuote := 0, false
	for j := i + 1; j < len(s); j++ {
		switch {
		case inQuote:
			if s[j] == '"' {
				inQuote = false
			}
		case s[j] == '"':
			inQuote = true
		case s[j] == '{':
			depth++
		case s[j] == '}':
			depth--
			if depth == 0 {
				return s[i+2 : j], j + 1, true
			}
		}
	}
	return "", 0, false
}

// resolve computes one `${expr}` reference, through core's `interp` — the SAME
// engine the readme bridge and pf-cli use, so the three planes can never disagree
// about what an address means. It is called per reference rather than over the
// whole string because the D4 empty-on-miss rule below is this plane's own: core
// leaves an unresolved reference VERBATIM (right for a badge URL, which is then
// dropped whole), while a tool argument here has to collapse to nothing.
//
// The scope is `org.projectfile.image`, so a template naming the image's own
// declared parts (`${org}`, `${name}`, `${path}`, `${tag}`) reads them without
// spelling the address, exactly as a sink `ref` template does. A part whose value
// is itself a reference — `name: ${identity.name}` — resolves recursively.
//
// A miss resolves to EMPTY (D4, reversed): an absent artifact or a typo leaves the
// arg UNSET, never a hard error. The old `[k=v]` bracket spelling is still refused
// LOUDLY, because it is not a miss: the selector grammar is `{k=v}`, and silently
// dropping the argument would publish a well-formed command with a hole in it.
func (ip interpolator) resolve(expr string) (string, error) {
	if strings.ContainsAny(expr, "[]") {
		return "", fmt.Errorf("reference ${%s}: `[…]` is not the selector spelling — "+
			"write `{k=v}` for a selector and `{}` for a projection", expr)
	}
	out, ok := interp.ExpandIn(ip.doc, "${"+expr+"}", imageScope)
	if !ok {
		return "", nil // unresolved → empty (D4 reversed): leave the arg unset
	}
	return out, nil
}

// interpolateRefs resolves every `${<pf-path>}` reference in the subtree's tool
// invocations (run, set-env values, string-form build-args) and node positional
// args against the merged doc, IN PLACE. Called once by Load after the image
// basename is finalized. An unresolved reference now resolves to EMPTY (D4, reversed
// 2026-07-10): a shared-preset ref to an artifact THIS project omits leaves the arg
// unset rather than failing the generate. Only a malformed ref (an unterminated
// `${`, or the retired `[k=v]` selector) still errors.
func (r *Reader) interpolateRefs(st *Subtree) error {
	ip := interpolator{doc: r.doc}
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
// interpolateRefs walks, references and all — and stashed on the manifest for
// render to thread as a `with:` input. The action then suffixes it per cell
// (→ <path>-<os>-<arch>).
//
// Fail-closed: a forgejo-release tool with zero or more than one kind=binary
// artifact is an ambiguous attach target, not a silent miss — the action would
// either attach nothing or the wrong binary. `kind` is advisory vocabulary, so
// the match is on the literal `binary` value only.
func (r *Reader) resolveReleaseAssetPaths(st *Subtree) error {
	ip := interpolator{doc: r.doc}
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
		// The path is a string scalar like any other (§3.8), so a shared language
		// fragment writes dist/${identity.name} and every consumer resolves its own
		// name. interpolateRefs cannot reach it — that walks tool invocations, and this
		// value arrives from the artifacts subtree — so the same interpolator runs here.
		if path, err = ip.interpolate(path); err != nil {
			return fmt.Errorf("tool %q release asset path: %w", name, err)
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
