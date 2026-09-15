// SPDX-FileCopyrightText: 2026 Damián Búho <damian.buho@proton.me>
//
// SPDX-License-Identifier: MIT

package ci

import (
	"encoding/json"
	"fmt"
	"strings"

	"kiota.ch/projectfile/core/v2/pkg/interp"
	"kiota.ch/projectfile/core/v2/pkg/projectfile"
)

// archNamespace / archKey address the declared CPU architecture set. The field is
// a plain list one level inside the shared org.projectfile namespace, so it is
// addressed as namespace + key rather than as a subtree of its own.
const (
	archNamespace    = "org.projectfile"
	archKey          = "architecture"
	ciNamespace      = "org.projectfile.ci"
	maxFeaturesLevel = 1
)

// Reader is the library seam onto core. It holds ONE includes-merged projectfile
// document and serves the extension subtrees the resolver needs — replacing the
// former `pf-cli get … --format json` subprocess dance (six execs per run). Read
// once, query many. The merge (root `includes:`, deep-merge per spec §4.9a) is
// core's, same as production; pf-ci never re-implements it.
type Reader struct{ doc *projectfile.Document }

// newReader loads the includes-merged document: pfPath explicit, else the newest
// sibling in cwd (core's production auto-discovery). A read error propagates — a
// project driving pf-ci is expected to have a projectfile; an ABSENT SUBTREE is
// the graceful "no CI" case and is handled per-lookup by subtree.
func newReader(pfPath string) (*Reader, error) {
	var doc *projectfile.Document
	var err error
	if pfPath != "" {
		doc, err = projectfile.Read(pfPath)
	} else {
		doc, _, err = projectfile.ReadWithOptions(".", projectfile.ReadOptions{})
	}
	if err != nil {
		return nil, err
	}
	if err := projectfile.CheckFeaturesLevel(doc, ciNamespace, maxFeaturesLevel); err != nil {
		return nil, fmt.Errorf("%w: run make install-binary to refresh pf-ci", err)
	}
	return &Reader{doc: doc}, nil
}

// subtree returns the extension value at a reverse-DNS namespace plus optional
// trailing map keys (org.projectfile.ci, …ci.images, …ci.secrets, …build,
// …events) marshalled to JSON — the exact shape the former `pf-cli get <path>
// --format json` produced, so Parse / provision.sh see identical bytes. An
// absent namespace or key yields (nil, nil): a missing subtree is graceful, never
// an error (mirrors pf-cli's "null" → nil).
func (r *Reader) subtree(path string) ([]byte, error) {
	ns, keys := splitExtPath(path)
	m, ok := projectfile.Extension(r.doc, ns)
	if !ok {
		return nil, nil
	}
	var v any = m
	for _, k := range keys {
		mm, ok := v.(map[string]any)
		if !ok {
			return nil, nil
		}
		if v, ok = mm[k]; !ok {
			return nil, nil
		}
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("marshal %s: %w", path, err)
	}
	return raw, nil
}

// architectures returns the CPU architectures the project DECLARES
// (org.projectfile.architecture). Absent or empty yields nil, which is the whole
// opt-in: spec §4.8a reads an absent set as unconstrained (a consumer assumes the
// build host), so a project that names none mints no arch axis and renders the
// workflow it rendered before the field existed.
//
// The values are read VERBATIM — bare arch names (`amd64`), never platform refs
// (`linux/amd64`). The same token is a matrix value, an image-tag segment and a
// deps-file stem, so composing `linux/` is the caller's job at the one place a
// platform ref is actually needed.
// It cannot go through subtree: that helper ends at core's Extension, which only
// yields MAP-valued subtrees (org.projectfile.ci, …build). `architecture` is a
// LIST, so the same call would fail its type assertion and report the field as
// absent — a silent miss, not an error. Read the enclosing namespace and index it.
func (r *Reader) architectures() ([]string, error) {
	m, ok := projectfile.Extension(r.doc, archNamespace)
	if !ok {
		return nil, nil
	}
	raw, ok := m[archKey]
	if !ok {
		return nil, nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("%s.%s: expected a list of architectures, got %T", archNamespace, archKey, raw)
	}
	arches := make([]string, 0, len(list))
	for _, v := range list {
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("%s.%s: expected string entries, got %T", archNamespace, archKey, v)
		}
		arches = append(arches, s)
	}
	return arches, nil
}

// imageBasename returns the container-image basename the project DECLARES, as
// `org.projectfile.image.path` composed under its own scope ("" on a miss — the
// caller treats empty as "no naming step").
//
// It is a document read, not a rule: the path is a template over parts the
// project states (`${org}/${name}`, or whatever grammar it invents), so the
// readme bridge, the m6e reader and this plane all name one image identically
// without any of them carrying a copy of the formula.
func (r *Reader) imageBasename() string {
	v, ok := interp.ExpandIn(r.doc, "${"+imageScope+".path}", imageScope)
	if !ok {
		return ""
	}
	return v
}

// splitExtPath splits a dotted address into its reverse-DNS extension namespace
// (org.projectfile.<x> — three labels, the shape every org.projectfile.* subtree
// takes here) and any trailing map keys: `org.projectfile.ci.images` →
// ("org.projectfile.ci", ["images"]).
func splitExtPath(path string) (ns string, keys []string) {
	parts := strings.Split(path, ".")
	if len(parts) <= 3 {
		return path, nil
	}
	return strings.Join(parts[:3], "."), parts[3:]
}
