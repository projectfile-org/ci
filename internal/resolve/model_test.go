// SPDX-FileCopyrightText: 2026 Damián Búho <damian.buho@proton.me>
//
// SPDX-License-Identifier: MIT

package resolve

import (
	"strings"
	"testing"

	"projectfile.org/projectfile/ci/v3/internal/ci"
)

// mustParse decodes a CI subtree JSON document, failing the test on error.
func mustParse(t *testing.T, doc string) *ci.Subtree {
	t.Helper()
	st, err := ci.Parse([]byte(doc))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return st
}

// cycleSubtree is the two-node loop preflight refuses.
func cycleSubtree(t *testing.T) *ci.Subtree {
	t.Helper()
	return mustParse(t, `{
		"tools": {"build": {"action": "container-build"}},
		"nodes": {"a": {"needs": {"b": true}}, "b": {"needs": {"a": true}}}}`)
}

// TestResolveNilSubtreeIsNoOp pins the graceful no-CI case: a project with no
// org.projectfile.ci resolves to no model and no error, so a missing subtree never
// fails a run.
func TestResolveNilSubtreeIsNoOp(t *testing.T) {
	m, err := Resolve(nil)
	if m != nil || err != nil {
		t.Fatalf("Resolve(nil) = %+v, %v, want nil, nil", m, err)
	}
}

// TestRejectErrorNamesTheReason pins the message contract the conformance vectors
// assert on: the vendor-neutral reason, prefixed by the namespace it belongs to.
func TestRejectErrorNamesTheReason(t *testing.T) {
	err := &RejectError{Reason: "cycle"}
	if got := err.Error(); got != "org.projectfile.ci: cycle" {
		t.Fatalf("Error() = %q, want %q", got, "org.projectfile.ci: cycle")
	}
	var re *RejectError
	m, got := Resolve(cycleSubtree(t))
	if got == nil {
		t.Fatalf("Resolve(cycle) = %+v, want a rejection", m)
	}
	if !asReject(got, &re) || re.Reason != "cycle" {
		t.Fatalf("err = %v (%T), want a *RejectError with reason cycle", got, got)
	}
}

// asReject is errors.As specialised to *RejectError, so the assertion above reads
// as one line.
func asReject(err error, target **RejectError) bool {
	re, ok := err.(*RejectError)
	if ok {
		*target = re
	}
	return ok
}

// TestCycleAcrossThreeNodes pins Kahn's algorithm on a loop no two-node cycle
// exposes: every node keeps a non-zero in-degree, so the whole graph is refused.
func TestCycleAcrossThreeNodes(t *testing.T) {
	doc := `{
		"tools": {"build": {"action": "container-build"}},
		"nodes": {
			"a": {"needs": {"c": true}},
			"b": {"needs": {"a": true}},
			"c": {"needs": {"b": true}}
		}}`
	_, err := Resolve(mustParse(t, doc))
	if err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("err=%v, want the cycle rejection", err)
	}
}

// TestSelfCycleIsRejected pins the degenerate loop: a node needing itself never
// reaches in-degree zero.
func TestSelfCycleIsRejected(t *testing.T) {
	doc := `{
		"tools": {"build": {"action": "container-build"}},
		"nodes": {"a": {"goal": true, "needs": {"a": true}}}}`
	_, err := Resolve(mustParse(t, doc))
	if err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("err=%v, want the self-cycle rejection", err)
	}
}

// TestDedupeCollapsesOnlyAdjacentRuns pins the need-list compaction: a repeated
// name collapses, a shorter slice passes through untouched (the caller copies
// before appending, so the helper must not write into its input), and only
// consecutive repeats go — a, b, a is three entries.
func TestDedupe(t *testing.T) {
	if got := dedupe(nil); len(got) != 0 {
		t.Fatalf("dedupe(nil) = %v, want empty", got)
	}
	if got := dedupe([]string{"a"}); len(got) != 1 || got[0] != "a" {
		t.Fatalf("dedupe([a]) = %v, want [a]", got)
	}
	if got := dedupe([]string{"a", "a", "a"}); len(got) != 1 || got[0] != "a" {
		t.Fatalf("dedupe([a a a]) = %v, want [a]", got)
	}
	if got := dedupe([]string{"a", "b", "a"}); len(got) != 3 {
		t.Fatalf("dedupe([a b a]) = %v, want all three (only adjacent runs collapse)", got)
	}
}

// TestJobStringNamesClassAndCells pins the diagnostic rendering, which is what a
// reader pastes into a bug report: the name, its class, its cell count and its
// needs.
func TestJobString(t *testing.T) {
	j := Job{Name: "container-build", Class: ClassCell, Axes: []ci.Axis{{Key: axisSeries, Values: []string{seriesResolute, seriesNoble}}}, Needs: []string{"source-is-valid"}}
	want := `container-build(class=` + string(ClassCell) + ` cells=2 needs=[source-is-valid])`
	if got := j.String(); got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}

// TestJobCellsCountOnlyForCells pins that Cells() reports the matrix fan-out of a
// CELL job and 1 for every other class — a SOURCE or JOIN job runs once no matter
// how many axes the project declares.
func TestJobCellsCountOnlyForCells(t *testing.T) {
	axes := []ci.Axis{{Key: axisSeries, Values: []string{seriesResolute, seriesNoble}}}
	for _, tc := range []struct {
		name string
		job  Job
		want int
	}{
		{"cell-fans", Job{Class: ClassCell, Axes: axes}, 2},
		{"cell-without-axes", Job{Class: ClassCell}, 1},
		{"source", Job{Class: ClassSource, Axes: axes}, 1},
		{"join", Job{Class: ClassJoin, Axes: axes}, 1},
		{"unknown-class", Job{Class: Class("other"), Axes: axes}, 1},
	} {
		if got := tc.job.Cells(); got != tc.want {
			t.Errorf("%s: Cells() = %d, want %d", tc.name, got, tc.want)
		}
	}
}

// TestJobCellsHonoursExclusions pins that an exclusion really subtracts: two axes
// minus the two cells one exclusion names leaves the rest.
func TestJobCellsHonoursExclusions(t *testing.T) {
	j := Job{
		Class:    ClassCell,
		Axes:     []ci.Axis{{Key: axisSeries, Values: []string{seriesResolute, seriesNoble}}, {Key: axisArch, Values: []string{archAMD64, "arm64"}}},
		Excludes: []ci.Exclusion{{{Key: axisSeries, Value: seriesResolute}, {Key: axisArch, Value: archAMD64}}},
	}
	if got := j.Cells(); got != 3 {
		t.Fatalf("Cells() = %d, want 3 (4 minus the one excluded cell)", got)
	}
}

// TestGoalsFallBackToSinks pins the inferred-goal rule: with no goal:true node,
// every node nothing else needs becomes a goal — which is how a project that
// predates `goal` keeps rendering one workflow per terminal milestone.
func TestGoalsFallBackToSinks(t *testing.T) {
	doc := `{
		"tools": {"build": {"action": "container-build"}, "test": {"action": "container-test"}},
		"nodes": {
			"source-is-valid": {"needs": {"lint": true}},
			"image-built": {"needs": {"source-is-valid": true, "build": true}},
			"image-tested": {"needs": {"image-built": true, "test": true}},
			"left-out": {"needs": {"lint": true}}
		}}`
	m, err := Resolve(mustParse(t, doc))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if strings.Join(m.Goals, ",") != "image-tested,left-out" {
		t.Fatalf("goals = %v, want the two sinks (image-tested, left-out)", m.Goals)
	}
}

// TestGoalsExplicitSortsTheDeclaredSet pins the explicit form: the declared goals
// are taken verbatim and sorted, never inferred from the sinks — which is what
// keeps a mutating `*-fix` sink out of the closure.
func TestGoalsExplicitSortsTheDeclaredSet(t *testing.T) {
	doc := `{
		"tools": {"build": {"action": "container-build"}},
		"nodes": {
			"built": {"goal": true, "needs": {"build": true}},
			"formatted": {"needs": {"build": true}},
			"checked": {"goal": true, "needs": {"built": true}}
		}}`
	m, err := Resolve(mustParse(t, doc))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if strings.Join(m.Goals, ",") != "built,checked" {
		t.Fatalf("goals = %v, want built,checked (the unflagged sink excluded)", m.Goals)
	}
	// The mutating sink's tool must not appear: it is outside every closure.
	for _, j := range m.Jobs {
		if j.Name == "format" {
			t.Fatal("the unflagged sink's tool ran, want it outside the closure")
		}
	}
}

// TestReachableSkipsOffClosureTools pins that a tool of an off-closure node never
// enters the model, even when the node itself is declared.
func TestReachableSkipsOffClosureTools(t *testing.T) {
	doc := `{
		"tools": {"container-build": {"action": "container-build"}, "oci-push": {"action": "oci-push"}},
		"nodes": {
			"built": {"goal": true, "needs": {"build": true}},
			"pushed": {"goal": true, "needs": {"publish": true}}
		}}`
	m, err := Resolve(mustParse(t, doc))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(m.Jobs) != 2 {
		t.Fatalf("jobs = %d, want 2 (both goals are closures)", len(m.Jobs))
	}
}

// TestFrontierSkipsRecipeLessNodes pins the abstract-node contraction: a pure-join
// node has no recipe, so a downstream tool depends on the tools the node's
// upstreams gate — never on the node itself, and never on nothing.
func TestFrontierSkipsRecipeLessNodes(t *testing.T) {
	doc := `{
		"tools": {"build": {"action": "container-build"}, "test": {"action": "container-test"}},
		"nodes": {
			"built": {"needs": {"build": true}},
			"mid": {"needs": {"built": true}},
			"upper": {"needs": {"mid": true}},
			"ready": {"goal": true, "needs": {"upper": true, "test": true}}
		}}`
	m, err := Resolve(mustParse(t, doc))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	by := map[string]Job{}
	for _, j := range m.Jobs {
		by[j.Name] = j
	}
	// container-test runs in parallel with the build chain, so it waits on nothing
	// the chain produces — the join between them is the GOAL, not a tool edge.
	if got := by["container-test"].Needs; len(got) != 0 {
		t.Fatalf("container-test needs = %v, want none", got)
	}
	if got := by["container-build"].Needs; len(got) != 0 {
		t.Fatalf("container-build needs = %v, want none", got)
	}
}

// TestBuildJobsNeverSelfGate pins that a tool's own node never appears in its
// needs: the edge that matters runs tool-to-tool, and a self-edge would make the
// forge reject the job as circular.
func TestBuildJobsNeverSelfGate(t *testing.T) {
	doc := `{
		"tools": {"build": {"action": "container-build"}, "test": {"action": "container-test"}},
		"nodes": {
			"built": {"needs": {"build": true}},
			"tested": {"matrix": true, "needs": {"built": true, "test": true}},
			"ready": {"goal": true, "needs": {"tested": true}}
		}}`
	m, err := Resolve(mustParse(t, doc))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	for _, j := range m.Jobs {
		for _, n := range j.Needs {
			if n == j.Name {
				t.Fatalf("job %q needs itself", j.Name)
			}
		}
	}
}

// TestJobsAreNameSorted pins the deterministic output order: jobs sort by name
// regardless of declaration order, so two runs of the same document render
// byte-identically.
func TestJobsAreNameSorted(t *testing.T) {
	doc := `{
		"tools": {"zebra": {"action": "oci-push"}, "alpha": {"action": "container-build"}, "mid": {"action": "container-test"}},
		"nodes": {"ready": {"goal": true, "needs": {"zebra": true, "alpha": true, "mid": true}}}}`
	m, err := Resolve(mustParse(t, doc))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	var names []string
	for _, j := range m.Jobs {
		names = append(names, j.Name)
	}
	if strings.Join(names, ",") != "alpha,mid,zebra" {
		t.Fatalf("jobs = %v, want name-sorted", names)
	}
}

// TestClassOfNode pins the four job classes: a CELL is flagged matrix, a pure join
// is a JOIN, anything else with tools is a SOURCE, and a goal is a GATE.
func TestClassOfNode(t *testing.T) {
	doc := `{
		"matrix": {"axes": {"SERIES": ["resolute", "noble"]}},
		"tools": {"shellcheck": {"action": "shellcheck"}, "container-build": {"action": "container-build"}},
		"nodes": {
			"linted": {"needs": {"shellcheck": true}},
			"built": {"matrix": true, "needs": {"linted": true, "container-build": true}},
			"joined": {"needs": {"built": true}},
			"ready": {"goal": true, "needs": {"joined": true}}
		}}`
	m, err := Resolve(mustParse(t, doc))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	got := map[string]Class{}
	for _, j := range m.Jobs {
		got[j.Name] = j.Class
	}
	if got["shellcheck"] != ClassSource {
		t.Errorf("shellcheck class = %q, want SOURCE", got["shellcheck"])
	}
	if got["container-build"] != ClassCell {
		t.Errorf("container-build class = %q, want CELL", got["container-build"])
	}
	if _, ok := got["joined"]; ok {
		t.Error("the recipe-less join node became a job, want it contracted away")
	}
}

// TestNodeModelMaterialisesOnlyTheClosure pins the node projection the renderer
// walks: both goals materialise, and neither one's tool gate leaks into the other
// goal's node list.
func TestNodeModelMaterialisesOnlyTheClosure(t *testing.T) {
	nodes := NodeModel(mustParse(t, `{
		"tools": {"container-build": {"action": "container-build"}, "oci-push": {"action": "oci-push"}},
		"nodes": {
			"built": {"goal": true, "needs": {"container-build": true}},
			"pushed": {"goal": true, "needs": {"oci-push": true}}
		}}`))
	if nodes == nil {
		t.Fatal("NodeModel returned nil for a non-empty subtree")
	}
	var names []string
	for _, v := range nodes.Views {
		names = append(names, v.Name)
	}
	if strings.Join(names, ",") != "built,pushed" {
		t.Fatalf("views = %v, want both goal nodes", names)
	}
	// Each tool gate hangs off its own goal node: ToolDeps names the NODE a tool's
	// gate waits for, and here neither has an upstream node, so both are empty.
	if deps, ok := nodes.ToolDeps["container-build"]; !ok || len(deps) != 0 {
		t.Fatalf("container-build deps = %v (present=%v), want an empty list", deps, ok)
	}
	if deps, ok := nodes.ToolDeps["oci-push"]; !ok || len(deps) != 0 {
		t.Fatalf("oci-push deps = %v (present=%v), want an empty list", deps, ok)
	}
}

// TestNodeModelNilSubtree pins the no-CI case on the node plane: no subtree is no
// node model, not an empty one.
func TestNodeModelNilSubtree(t *testing.T) {
	var st *ci.Subtree
	if nodes := NodeModel(st); nodes != nil {
		t.Fatalf("NodeModel(nil) = %+v, want nil", nodes)
	}
}

// TestDropAxesDropsDependentExclusions pins that an exclusion naming a DROPPED
// axis goes with it: the cells it named no longer exist, so keeping it would
// subtract survivors that merely share the rest of its coordinates.
func TestDropAxesDropsDependentExclusions(t *testing.T) {
	axes := []ci.Axis{{Key: axisSeries, Values: []string{seriesResolute, seriesNoble}}, {Key: axisArch, Values: []string{archAMD64}}}
	excludes := []ci.Exclusion{{{Key: axisSeries, Value: seriesResolute}, {Key: axisArch, Value: "arm64"}}}
	gotAxes, gotEx := dropAxes("n", axes, excludes, []string{axisSeries})
	if len(gotAxes) != 1 || gotAxes[0].Key != axisArch {
		t.Fatalf("axes = %+v, want M6E_ARCH alone", gotAxes)
	}
	if len(gotEx) != 0 {
		t.Fatalf("excludes = %+v, want the dropped-axis row to go with it", gotEx)
	}
}

// TestPinAxesDropsUnreachableExclusions pins the pin twin: an exclusion naming a
// DIFFERENT value of a pinned axis can never match a surviving cell, so it goes.
func TestPinAxesDropsUnreachableExclusions(t *testing.T) {
	axes := []ci.Axis{{Key: axisSeries, Values: []string{seriesResolute, seriesNoble}}}
	excludes := []ci.Exclusion{{{Key: axisSeries, Value: seriesNoble}}}
	gotAxes, gotEx := pinAxes("n", axes, excludes, map[string]string{axisSeries: seriesResolute})
	if len(gotAxes) != 1 || len(gotAxes[0].Values) != 1 || gotAxes[0].Values[0] != seriesResolute {
		t.Fatalf("axes = %+v, want narrowed to resolute", gotAxes)
	}
	if len(gotEx) != 0 {
		t.Fatalf("excludes = %+v, want the unreachable row dropped", gotEx)
	}
	// The SAME value keeps working: the row still matches the one surviving cell.
	keep := []ci.Exclusion{{{Key: axisSeries, Value: seriesResolute}}}
	_, kept := pinAxes("n", axes, keep, map[string]string{axisSeries: seriesResolute})
	if len(kept) != 1 {
		t.Fatalf("excludes = %+v, want the matching row kept", kept)
	}
}
