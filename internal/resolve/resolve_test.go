// SPDX-FileCopyrightText: 2026 Damián Búho <damian.buho@proton.me>
//
// SPDX-License-Identifier: MIT

package resolve

import (
	"testing"

	"projectfile.org/projectfile/ci-resolver/internal/ci"
)

// TestPerNodeMatrixIsolatesFanOut proves a per-node matrix fans a CELL job over the
// NODE's own axes, independent of the global subtree matrix: in one pipeline the
// binaries fan over {GOOS,GOARCH} (4 cells) while the image build fans over the
// global SERIES (2 cells). The two artifact classes never inherit each other's
// dimensions — the property the §9 publish accumulator needs (an image must not be
// built for GOOS=darwin).
func TestPerNodeMatrixIsolatesFanOut(t *testing.T) {
	st, err := ci.Parse([]byte(`{
	  "matrix": {"axes": {"SERIES": ["resolute", "noble"]}},
	  "tools": {"container-build": {"action": "container-build"}, "build-binaries": {"run": "go build"}},
	  "nodes": {
	    "image-built": {"matrix": true, "needs": {"container-build": true}},
	    "bins-built":  {"matrix": {"axes": {"GOOS": ["linux", "darwin"], "GOARCH": ["amd64", "arm64"]}}, "needs": {"build-binaries": true}},
	    "ready":       {"goal": true, "needs": {"image-built": true, "bins-built": true}}
	  }
	}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rm, err := Resolve(st)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	by := map[string]Job{}
	for _, j := range rm.Jobs {
		by[j.Name] = j
	}

	// The image build inherits the GLOBAL matrix: 2 SERIES cells.
	img := by["container-build"]
	if img.Cells() != 2 {
		t.Errorf("container-build: want 2 cells (global SERIES), got %d (axes %v)", img.Cells(), img.Axes)
	}
	if len(img.Axes) != 1 || img.Axes[0].Key != "SERIES" {
		t.Errorf("container-build: want global SERIES axis, got %v", img.Axes)
	}

	// The binaries fan over the NODE's own {GOOS,GOARCH}: 2*2 = 4 cells, never SERIES.
	bins := by["build-binaries"]
	if bins.Cells() != 4 {
		t.Errorf("build-binaries: want 4 cells (own GOOS*GOARCH), got %d (axes %v)", bins.Cells(), bins.Axes)
	}
	keys := []string{}
	for _, a := range bins.Axes {
		keys = append(keys, a.Key)
	}
	if len(keys) != 2 || keys[0] != "GOARCH" || keys[1] != "GOOS" {
		t.Errorf("build-binaries: want own [GOARCH GOOS] axes, got %v", keys)
	}
}

// TestMatrixExcludeSubtractsCells pins the multiplicity `matrix.exclude` produces on
// BOTH matrices at once, and their isolation: the global cut applies to the node on
// the global axes and NEVER to the node fanning its own, whose own cut applies
// instead. A JOIN downstream of both still runs once — subtraction changes how many
// cells exist, never the timing classes.
func TestMatrixExcludeSubtractsCells(t *testing.T) {
	st, err := ci.Parse([]byte(`{
	  "matrix": {"axes": {"SERIES": ["resolute", "noble"], "ARCH": ["amd64", "arm64"]},
	             "exclude": [{"SERIES": "noble", "ARCH": "arm64"}]},
	  "tools": {"container-build": {"action": "container-build"}, "build-binaries": {"run": "go build"}},
	  "nodes": {
	    "image-built": {"matrix": true, "needs": {"container-build": true}},
	    "bins-built":  {"matrix": {"axes": {"GOOS": ["linux", "darwin"], "GOARCH": ["amd64", "arm64", "riscv64"]},
	                               "exclude": [{"GOOS": "darwin", "GOARCH": "riscv64"}]},
	                    "needs": {"build-binaries": true}},
	    "ready":       {"goal": true, "needs": {"image-built": true, "bins-built": true, "manifest-create": true}}
	  }
	}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rm, err := Resolve(st)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	by := map[string]Job{}
	for _, j := range rm.Jobs {
		by[j.Name] = j
	}

	// Global axes: 2*2 = 4 cells, minus noble/arm64.
	if got := by["container-build"].Cells(); got != 3 {
		t.Errorf("container-build: want 3 cells (global 2x2 minus one), got %d", got)
	}
	// Own axes: 2*3 = 6 cells, minus darwin/riscv64 — the global cut names axes this
	// grid does not even have, so inheriting it would subtract nothing or everything.
	if got := by["build-binaries"].Cells(); got != 5 {
		t.Errorf("build-binaries: want 5 cells (own 2x3 minus one), got %d", got)
	}
	// The fan-in is still once, whatever the cells number.
	if got := by["manifest-create"].Cells(); got != 1 {
		t.Errorf("manifest-create: JOIN must run once, got %d", got)
	}
}

// TestNodeModelMaterialisesReachableNodes pins NodeModel: every reachable node
// becomes a NodeView (its own tool members + reachable upstream node-deps), and a
// tool's ToolDeps is the union of its owning nodes' upstream node names — the
// node-materialised edges a grouping-less target (GHA/Forgejo) renders as gates. An
// UNREACHABLE node (out of the goal's closure) is absent. The conformance-pinned
// Model is unaffected; this view is purely additive.
func TestNodeModelMaterialisesReachableNodes(t *testing.T) {
	st, err := ci.Parse([]byte(`{
  "tools": {"shellcheck": {}, "container-build": {}, "grype-scan-tar": {}},
  "nodes": {
    "source-is-valid": {"needs": {"shellcheck": true}},
    "image-built":     {"needs": {"source-is-valid": true, "container-build": true}},
    "image-secure":    {"goal": true, "needs": {"image-built": true, "grype-scan-tar": true}},
    "cleaned":         {"needs": {"dc-down": true}}
  }
}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	nodes := NodeModel(st)
	if nodes == nil {
		t.Fatal("NodeModel returned nil for a non-empty subtree")
	}

	views := map[string]NodeView{}
	for _, v := range nodes.Views {
		views[v.Name] = v
	}
	// The unreachable teardown node never materialises (a flagged goal suppresses the
	// all-sinks fallback, so `cleaned` is outside the closure).
	if _, ok := views["cleaned"]; ok {
		t.Errorf("unreachable node `cleaned` must not materialise, got %v", nodes.Views)
	}
	// image-built owns container-build and depends on source-is-valid (a NODE).
	ib := views["image-built"]
	if len(ib.Tools) != 1 || ib.Tools[0] != "container-build" {
		t.Errorf("image-built tools = %v, want [container-build]", ib.Tools)
	}
	if len(ib.NodeDeps) != 1 || ib.NodeDeps[0] != "source-is-valid" {
		t.Errorf("image-built node-deps = %v, want [source-is-valid]", ib.NodeDeps)
	}
	// The goal node mixes a tool member (grype-scan-tar) and a node-dep (image-built).
	sec := views["image-secure"]
	if !sec.Goal {
		t.Errorf("image-secure should be flagged a goal")
	}
	if len(sec.Tools) != 1 || sec.Tools[0] != "grype-scan-tar" {
		t.Errorf("image-secure tools = %v, want [grype-scan-tar]", sec.Tools)
	}
	if len(sec.NodeDeps) != 1 || sec.NodeDeps[0] != "image-built" {
		t.Errorf("image-secure node-deps = %v, want [image-built]", sec.NodeDeps)
	}
	// Per-tool upstream node-gates: the scan waits on image-built (its node's dep);
	// container-build on source-is-valid; the root lint on nothing.
	if got := nodes.ToolDeps["grype-scan-tar"]; len(got) != 1 || got[0] != "image-built" {
		t.Errorf("grype-scan-tar ToolDeps = %v, want [image-built]", got)
	}
	if got := nodes.ToolDeps["container-build"]; len(got) != 1 || got[0] != "source-is-valid" {
		t.Errorf("container-build ToolDeps = %v, want [source-is-valid]", got)
	}
	if got := nodes.ToolDeps["shellcheck"]; len(got) != 0 {
		t.Errorf("shellcheck (root tool) ToolDeps = %v, want none", got)
	}
}
