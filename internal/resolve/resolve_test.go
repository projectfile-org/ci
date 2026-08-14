// SPDX-FileCopyrightText: 2026 Damián Búho <damian.buho@proton.me>
//
// SPDX-License-Identifier: MIT

package resolve

import (
	"testing"

	"projectfile.org/projectfile/ci-resolver/internal/ci"
)

// axisSeries is the global axis the matrix tests fan over (goconst).
const axisSeries = "SERIES"

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
	if len(img.Axes) != 1 || img.Axes[0].Key != axisSeries {
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

// withoutSubtree builds the shape Wave 4 and Wave 6 both need: a global matrix of
// SERIES × M6E_ARCH, a build cell fanning over both, and one node that subtracts the
// arch axis. `drop` is spliced into that node's matrix so one helper covers the
// present-axis, absent-axis and total-drop cases.
func withoutSubtree(t *testing.T, axes, drop string) *Model {
	t.Helper()
	st, err := ci.Parse([]byte(`{
	  "matrix": {"axes": ` + axes + `},
	  "tools": {"container-build": {"action": "container-build"}, "oci-manifest": {"action": "oci-manifest"}},
	  "nodes": {
	    "image-built": {"matrix": true, "needs": {"container-build": true}},
	    "manifest":    {"matrix": {"without": ` + drop + `}, "needs": {"oci-manifest": true, "image-built": true}},
	    "ready":       {"goal": true, "needs": {"manifest": true}}
	  }
	}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rm, err := Resolve(st)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	return rm
}

func jobsByName(rm *Model) map[string]Job {
	by := map[string]Job{}
	for _, j := range rm.Jobs {
		by[j.Name] = j
	}
	return by
}

// TestMatrixWithoutDropsOneAxis is the primitive Wave 4's assembly node runs on: the
// build fans over SERIES × ARCH while the manifest node fans over SERIES only, so one
// manifest job indexes the per-arch pushes of its series instead of one per arch.
func TestMatrixWithoutDropsOneAxis(t *testing.T) {
	by := jobsByName(withoutSubtree(t,
		`{"SERIES": ["resolute", "noble"], "M6E_ARCH": ["amd64", "arm64", "riscv64"]}`,
		`["M6E_ARCH"]`))

	if got := by["container-build"].Cells(); got != 6 {
		t.Errorf("container-build: want 6 cells (2 SERIES × 3 ARCH), got %d (axes %v)", got, by["container-build"].Axes)
	}
	man := by["oci-manifest"]
	if got := man.Cells(); got != 2 {
		t.Errorf("oci-manifest: want 2 cells (SERIES only), got %d (axes %v)", got, man.Axes)
	}
	if len(man.Axes) != 1 || man.Axes[0].Key != axisSeries {
		t.Errorf("oci-manifest: want the SERIES axis alone, got %v", man.Axes)
	}
}

// TestMatrixWithoutAbsentAxisIsNoOp is the zero-diff property the fleet rollout rests
// on. M6E_ARCH is DERIVED, so it is absent on the ~110 projects that declare no
// architecture — the same node declaration must there render exactly as it does today.
func TestMatrixWithoutAbsentAxisIsNoOp(t *testing.T) {
	by := jobsByName(withoutSubtree(t, `{"SERIES": ["resolute", "noble"]}`, `["M6E_ARCH"]`))

	man := by["oci-manifest"]
	if got := man.Cells(); got != 2 {
		t.Errorf("oci-manifest: want the untouched 2 SERIES cells, got %d (axes %v)", got, man.Axes)
	}
	if len(man.Axes) != 1 || man.Axes[0].Key != axisSeries {
		t.Errorf("oci-manifest: want SERIES kept, got %v", man.Axes)
	}
}

// TestMatrixWithoutEveryAxisLeavesOneJob covers the un-fanned assembly shape: with no
// axis left the node stops being a cell, which is what makes the render omit the
// strategy block entirely rather than emit an empty matrix the forge would reject.
func TestMatrixWithoutEveryAxisLeavesOneJob(t *testing.T) {
	by := jobsByName(withoutSubtree(t, `{"SERIES": ["resolute", "noble"]}`, `["SERIES"]`))

	man := by["oci-manifest"]
	if len(man.Axes) != 0 {
		t.Errorf("oci-manifest: want no axes left, got %v", man.Axes)
	}
	if got := man.Cells(); got != 1 {
		t.Errorf("oci-manifest: want a single un-fanned job, got %d cells", got)
	}
}

// TestMatrixWithoutDropsDependentExclusions pins that an exclusion naming a dropped
// axis goes with it. Kept, it would subtract a surviving cell that merely shares the
// rest of its coordinates — here it would delete the whole noble manifest job.
func TestMatrixWithoutDropsDependentExclusions(t *testing.T) {
	st, err := ci.Parse([]byte(`{
	  "matrix": {
	    "axes": {"SERIES": ["resolute", "noble"], "M6E_ARCH": ["amd64", "riscv64"]},
	    "exclude": [{"SERIES": "noble", "M6E_ARCH": "riscv64"}]
	  },
	  "tools": {"container-build": {"action": "container-build"}, "oci-manifest": {"action": "oci-manifest"}},
	  "nodes": {
	    "image-built": {"matrix": true, "needs": {"container-build": true}},
	    "manifest":    {"matrix": {"without": ["M6E_ARCH"]}, "needs": {"oci-manifest": true, "image-built": true}},
	    "ready":       {"goal": true, "needs": {"manifest": true}}
	  }
	}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rm, err := Resolve(st)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	by := jobsByName(rm)

	if got := by["container-build"].Cells(); got != 3 {
		t.Errorf("container-build: want 3 cells (4 minus the excluded one), got %d", got)
	}
	if got := by["oci-manifest"].Cells(); got != 2 {
		t.Errorf("oci-manifest: want both SERIES to keep a manifest job, got %d cells", got)
	}
}

// pinSubtree is withoutSubtree's twin for matrix.pin: the same SERIES × M6E_ARCH grid,
// with the live-test node narrowing the arch axis instead of dropping it. `pin` is
// spliced in so one helper covers the hit, absent-axis and absent-value cases.
func pinSubtree(t *testing.T, axes, pin string) *Model {
	t.Helper()
	st, err := ci.Parse([]byte(`{
	  "matrix": {"axes": ` + axes + `},
	  "tools": {"container-build": {"action": "container-build"}, "container-test": {"run": "test.d"}},
	  "nodes": {
	    "image-built": {"matrix": true, "needs": {"container-build": true}},
	    "verified":    {"matrix": {"pin": ` + pin + `}, "needs": {"container-test": true, "image-built": true}},
	    "ready":       {"goal": true, "needs": {"verified": true}}
	  }
	}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rm, err := Resolve(st)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	return rm
}

// TestMatrixPinNarrowsOneAxis is Wave 6's policy: the image builds on every arch while
// the live test runs on one. The axis SURVIVES with a single value — that is what keeps
// the per-arch artifact name computable in a job that fans over one arch.
func TestMatrixPinNarrowsOneAxis(t *testing.T) {
	by := jobsByName(pinSubtree(t,
		`{"SERIES": ["resolute", "noble"], "M6E_ARCH": ["amd64", "arm64", "riscv64"]}`,
		`{"M6E_ARCH": "amd64"}`))

	if got := by["container-build"].Cells(); got != 6 {
		t.Errorf("container-build: want 6 cells (2 SERIES × 3 ARCH), got %d (axes %v)", got, by["container-build"].Axes)
	}
	test := by["container-test"]
	if got := test.Cells(); got != 2 {
		t.Errorf("container-test: want 2 cells (SERIES × the pinned arch), got %d (axes %v)", got, test.Axes)
	}
	var arch *ci.Axis
	for i, a := range test.Axes {
		if a.Key == ci.ArchAxis {
			arch = &test.Axes[i]
		}
	}
	if arch == nil {
		t.Fatalf("container-test: the pinned axis must survive so the artifact stem keeps naming it, got %v", test.Axes)
	}
	if len(arch.Values) != 1 || arch.Values[0] != "amd64" {
		t.Errorf("container-test: want the arch axis narrowed to [amd64], got %v", arch.Values)
	}
}

// TestMatrixPinAbsentAxisIsNoOp is the zero-diff property, same as its `without` twin:
// M6E_ARCH is DERIVED, so the ~110 projects declaring no architecture must render the
// identical live-test job they render today.
func TestMatrixPinAbsentAxisIsNoOp(t *testing.T) {
	by := jobsByName(pinSubtree(t, `{"SERIES": ["resolute", "noble"]}`, `{"M6E_ARCH": "amd64"}`))

	test := by["container-test"]
	if got := test.Cells(); got != 2 {
		t.Errorf("container-test: want the untouched 2 SERIES cells, got %d (axes %v)", got, test.Axes)
	}
	if len(test.Axes) != 1 || test.Axes[0].Key != axisSeries {
		t.Errorf("container-test: want SERIES kept, got %v", test.Axes)
	}
}

// TestMatrixPinUnknownValueKeepsFanOut pins the fail-open rule. A project declaring no
// amd64 would otherwise get a live-test cell the build grid never minted, and the job
// would ask for an artifact no sibling uploaded — a 404, not a skipped test.
func TestMatrixPinUnknownValueKeepsFanOut(t *testing.T) {
	by := jobsByName(pinSubtree(t,
		`{"SERIES": ["resolute"], "M6E_ARCH": ["arm64", "riscv64"]}`,
		`{"M6E_ARCH": "amd64"}`))

	if got := by["container-test"].Cells(); got != 2 {
		t.Errorf("container-test: want the full 2-arch fan-out kept, got %d (axes %v)", got, by["container-test"].Axes)
	}
}

// TestMatrixPinDropsUnreachableExclusions covers the exclusion half: one naming a
// non-pinned value can never match a surviving cell, and keeping it would subtract
// nothing — but one naming the pinned value still has to bite.
func TestMatrixPinDropsUnreachableExclusions(t *testing.T) {
	st, err := ci.Parse([]byte(`{
	  "matrix": {
	    "axes": {"SERIES": ["resolute", "noble"], "M6E_ARCH": ["amd64", "riscv64"]},
	    "exclude": [{"SERIES": "noble", "M6E_ARCH": "riscv64"}, {"SERIES": "resolute", "M6E_ARCH": "amd64"}]
	  },
	  "tools": {"container-build": {"action": "container-build"}, "container-test": {"run": "test.d"}},
	  "nodes": {
	    "image-built": {"matrix": true, "needs": {"container-build": true}},
	    "verified":    {"matrix": {"pin": {"M6E_ARCH": "amd64"}}, "needs": {"container-test": true, "image-built": true}},
	    "ready":       {"goal": true, "needs": {"verified": true}}
	  }
	}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rm, err := Resolve(st)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	by := jobsByName(rm)

	if got := by["container-build"].Cells(); got != 2 {
		t.Errorf("container-build: want 2 cells (4 minus the two excluded), got %d", got)
	}
	// resolute/amd64 is excluded and noble/riscv64 cannot be reached from a pinned
	// amd64, so exactly the noble cell survives.
	if got := by["container-test"].Cells(); got != 1 {
		t.Errorf("container-test: want only the noble/amd64 cell, got %d (axes %v)", got, by["container-test"].Axes)
	}
}
