// SPDX-FileCopyrightText: 2026 Damián Búho <damian.buho@proton.me>
//
// SPDX-License-Identifier: MIT

package render

import (
	"reflect"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"projectfile.org/projectfile/ci/internal/ci"
	"projectfile.org/projectfile/ci/internal/resolve"
)

// Test-local constants for repeated string literals (goconst).
const (
	testShellcheck      = "shellcheck"
	testCell            = "cell"
	testContainerBuild  = "container-build"
	testOCIPush         = "oci-push"
	testImageBuilt      = "image-built"
	testImageMatrixStem = "image-${{ matrix.B19_UBUNTU_SERIES }}" + artifactScopeSuffix
	testUploadArtifact  = "upload-artifact"
	testCIActions       = "ci-actions"
	testUbuntuVersion   = "B19_UBUNTU_VERSION"
	testUbuntuSeries    = "B19_UBUNTU_SERIES"
	testUbuntuHash      = "B19_UBUNTU_HASH"
	testUbuntuBaseImage = "B19_UBUNTU_BASE_IMAGE"
	testResolute        = "resolute"
	testJsToolsImage    = "D9T_JS_TOOLS_IMAGE"
	testKiotaHead       = "kiota.ch"
	testContainerTest   = "container-test"
	testDCUp            = "dc-up-d"
	testDCDown          = "dc-down"
	testB19GoImage      = "${{ vars.B19_DOCKER_REGISTRY }}/b19/go"
	testDist            = "dist"
)

// matrixSubtree is the canonical exercise from the build order: a single axis,
// a SOURCE lint, two CELL nodes, and a JOIN. ci.Parse takes the same merged JSON
// pf-cli emits, so this is hermetic (no pf-cli, no network).
const matrixSubtree = `{
  "matrix": {"axes": {"B19_UBUNTU_SERIES": ["resolute", "noble"]}},
  "tools": {
    "container-build": {"run": "make container-build"},
    "grype-scan-image": {"image": "reg.example/go-tools:latest"}
  },
  "nodes": {
    "source-is-valid": {"needs": {"shellcheck": true}},
    "image-built": {"matrix": true, "needs": {"source-is-valid": true, "container-build": true}},
    "image-tested": {"matrix": true, "needs": {"image-built": true, "grype-scan-image": true}},
    "ready-to-publish": {"goal": true, "needs": {"source-is-valid": true, "image-tested": true, "manifest-create": true}}
  }
}`

func mustModel(t *testing.T) (Model, *ci.Subtree) {
	t.Helper()
	st, err := ci.Parse([]byte(matrixSubtree))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rm, err := resolve.Resolve(st)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	return Build(rm, st, nil), st
}

// steps flattens every node-job's member steps, keyed by tool name (Decision 2:
// tool = step). Tests that used to read a per-tool JobView read its StepView here.
func steps(m Model) map[string]StepView {
	out := map[string]StepView{}
	for _, j := range m.Jobs {
		for _, s := range j.Steps {
			out[s.Name] = s
		}
	}
	return out
}

// jobOf returns the node-job that HOSTS the named tool as a step (zero value if the
// tool runs in no reachable node — pruned or off-closure).
func jobOf(m Model, tool string) JobView {
	for _, j := range m.Jobs {
		for _, s := range j.Steps {
			if s.Name == tool {
				return j
			}
		}
	}
	return JobView{}
}

func TestBuildJoinsManifests(t *testing.T) {
	m, _ := mustModel(t)
	step := steps(m)
	// A tool with no manifest falls back to the BARE target name (the resolver
	// must NOT synthesise an `auto-` prefix — that is D9T manifest content).
	if got := step[testShellcheck].Run; got != testShellcheck {
		t.Errorf("shellcheck run: want bare name shellcheck, got %q", got)
	}
	// A manifest `run` overrides the convention.
	if got := step[testContainerBuild].Run; got != "make container-build" {
		t.Errorf("container-build run: want make container-build, got %q", got)
	}
	// A manifest `image` flows into the step (so the fragment emits run-tool).
	if got := step["grype-scan-image"].Image; got != "reg.example/go-tools:latest" {
		t.Errorf("grype image: want reg.example/go-tools:latest, got %q", got)
	}
	// Only CELL node-jobs carry matrix axes + the env binding; SOURCE jobs do not. The
	// container-build step's node (image-built) fans the axis; its host job carries it.
	cb := jobOf(m, testContainerBuild)
	if cb.Class != testCell || len(cb.Matrix) != 1 {
		t.Errorf("container-build's node should be a CELL with one axis: %+v", cb)
	}
	if len(cb.Env) != 1 || cb.Env[0].Value != "${{ matrix.B19_UBUNTU_SERIES }}" {
		t.Errorf("container-build node env binding wrong: %+v", cb.Env)
	}
	sc := jobOf(m, testShellcheck)
	if sc.Class != "source" || sc.Matrix != nil {
		t.Errorf("shellcheck's node should be a matrix-free SOURCE: %+v", sc)
	}
}

// TestImageRefSubstitutesPerCell pins the {AXIS} image templating: a templated
// basename lowers its placeholder to ${{ matrix.<axis> }} on BOTH the build PRODUCER
// (stamps the cell's own ref into the OCI archive) and the oci-push CONSUMER (re-tags
// it), so each matrix cell publishes to a distinct image — the m6e cell sub-make does
// the same substitution against its own var (Law 3: one template, two spellings).
func TestImageRefSubstitutesPerCell(t *testing.T) {
	st, err := ci.Parse([]byte(`{
	  "image": "b19/ubuntu-{B19_UBUNTU_SERIES}",
	  "matrix": {"axes": {"B19_UBUNTU_SERIES": ["resolute", "noble"]}},
	  "tools": {"container-build": {"action": "container-build"}, "oci-push": {"action": "oci-push"}},
	  "nodes": {
	    "image-built": {"matrix": true, "needs": {"container-build": true}},
	    "published": {"matrix": true, "goal": true, "needs": {"image-built": true, "oci-push": true}}
	  }
	}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rm, err := resolve.Resolve(st)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	step := steps(Build(rm, st, nil))
	want := "b19/ubuntu-${{ matrix.B19_UBUNTU_SERIES }}"
	for _, name := range []string{testContainerBuild, testOCIPush} {
		if got := step[name].ImageBasename; got != want {
			t.Errorf("%s ImageBasename: want %q, got %q", name, want, got)
		}
	}
}

// TestContainerBuildPfCliImage pins the pf-cli-image lowering: a container-build step
// takes the projectfile/cli ref (registry path from the ci.images PF_CLI_IMAGE var +
// the shared tag expr) so oci-labels.sh reads the projectfile through that image on a
// runner with no host pf-cli. Absent the var, the input is empty (omitted) and the
// oci-push consumer never carries it.
func TestContainerBuildPfCliImage(t *testing.T) {
	src := `{
	  "image": "b19/fd",
	  "tools": {"container-build": {"action": "container-build"}, "oci-push": {"action": "oci-push"}},
	  "nodes": {
	    "image-built": {"needs": {"container-build": true}},
	    "published": {"goal": true, "needs": {"image-built": true, "oci-push": true}}
	  }
	}`
	st, err := ci.Parse([]byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rm, err := resolve.Resolve(st)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	build := &ci.Build{Images: map[string]string{
		"PF_CLI_IMAGE": "${PF_DOCKER_REGISTRY}/projectfile/cli:${M6E_BASE_IMAGE_DEFAULT_VERSION}",
	}}
	step := steps(Build(rm, st, build))
	want := "${{ vars.PF_DOCKER_REGISTRY }}/projectfile/cli:${{ env.M6E_TAG }}"
	if got := step[testContainerBuild].PfCliImage; got != want {
		t.Errorf("container-build PfCliImage: want %q, got %q", want, got)
	}
	if got := step[testOCIPush].PfCliImage; got != "" {
		t.Errorf("oci-push must not carry pf-cli-image, got %q", got)
	}
	// No PF_CLI_IMAGE var => empty input (the action degrades to host pf-cli / skip).
	stepNoVar := steps(Build(rm, st, &ci.Build{}))
	if got := stepNoVar[testContainerBuild].PfCliImage; got != "" {
		t.Errorf("PfCliImage without ci.images var: want empty, got %q", got)
	}
}

func TestWorkflowRenders(t *testing.T) {
	m, _ := mustModel(t)
	gha, err := Workflow(m, Targets[TargetGHA], ci.Platform{})
	if err != nil {
		t.Fatal(err)
	}
	s := string(gha)
	for _, want := range []string{
		"name: ready-to-publish", // the file is named for its single goal node
		"image-built:",           // the NODE-job hosting the container-build step
		"strategy:",
		`B19_UBUNTU_SERIES: ["resolute", "noble"]`,
		"B19_UBUNTU_SERIES: ${{ matrix.B19_UBUNTU_SERIES }}",
		"runs-on: ubuntu-latest",
		"uses: actions/checkout@v7",
		"run: make container-build",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("GHA workflow missing %q\n---\n%s", want, s)
		}
	}
	// The lone SOURCE lint must render WITHOUT a matrix strategy block.
	if strings.Count(s, "strategy:") != 2 { // only the two CELL nodes
		t.Errorf("want exactly 2 strategy blocks (the CELL nodes), got %d", strings.Count(s, "strategy:"))
	}
	// INDENT GUARD: the dispatched step body must sit at 6 spaces under `steps:`
	// (a template-trim slip once flattened the first item to column 0, and the
	// Contains assertions above could not see it). Assert the exact block.
	if !strings.Contains(s, "\n    steps:\n      - uses: actions/checkout@v7\n        with:\n          submodules: true\n      - name: container-build\n        run: ") {
		t.Errorf("portable step body lost its 6-space indent under steps:\n%s", s)
	}
}

// TestMaxParallelRenders pins that a matrix node's max-parallel surfaces INSIDE that
// node's strategy block (throttling only its cells) and nowhere else — the sibling
// matrix job keeps the forge's full-parallel default.
func TestMaxParallelRenders(t *testing.T) {
	st, err := ci.Parse([]byte(`{
	  "image": "b19/node-{B19_UBUNTU_SERIES}",
	  "matrix": {"axes": {"B19_UBUNTU_SERIES": ["resolute", "noble"]}},
	  "tools": {"container-build": {"action": "container-build"}, "grype-scan-tar": {}},
	  "nodes": {
	    "image-built": {"matrix": true, "max-parallel": 1, "needs": {"container-build": true}},
	    "image-scanned": {"matrix": true, "goal": true, "needs": {"image-built": true, "grype-scan-tar": true}}
	  }
	}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rm, err := resolve.Resolve(st)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	out, err := Workflow(Build(rm, st, nil), Targets[TargetGHA], ci.Platform{})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	// The throttled node carries the cap directly above its matrix.
	if !strings.Contains(s, "image-built:\n    runs-on: ubuntu-latest\n    strategy:\n      max-parallel: 1\n      matrix:") {
		t.Errorf("image-built strategy missing max-parallel: 1\n---\n%s", s)
	}
	// Exactly one cap in the whole file — the un-annotated scanner must NOT inherit it.
	if got := strings.Count(s, "max-parallel:"); got != 1 {
		t.Errorf("want exactly 1 max-parallel (image-built only), got %d\n---\n%s", got, s)
	}
}

// TestSerialiseChainsCells pins the whole shape of `serialise: <AXIS>`: one job per
// axis value, each waiting on the one before, and a JOIN under the authored name so a
// consumer's `needs:` never learns the node was split. This is the portable spelling of
// max-parallel — Forgejo dispatches matrix cells with no regard for a strategy cap, but
// it always honours `needs`.
func TestSerialiseChainsCells(t *testing.T) {
	st, err := ci.Parse([]byte(`{
	  "image": "b19/node-{B19_NODE_SERIES}",
	  "matrix": {"axes": {"B19_NODE_SERIES": ["24", "26"]}},
	  "tools": {"container-build": {"action": "container-build"}, "grype-scan-tar": {}},
	  "nodes": {
	    "image-built": {"matrix": true, "serialise": "B19_NODE_SERIES", "needs": {"container-build": true}},
	    "image-scanned": {"matrix": true, "goal": true, "needs": {"image-built": true, "grype-scan-tar": true}}
	  }
	}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rm, err := resolve.Resolve(st)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	m := Build(rm, st, nil)
	if m.Err != nil {
		t.Fatalf("build: %v", m.Err)
	}
	byName := map[string]JobView{}
	for _, j := range m.Jobs {
		byName[j.Name] = j
	}

	// One link per value, each narrowed to its OWN cell — the axis stays DECLARED, so
	// every `${{ matrix.B19_NODE_SERIES }}` in the steps still resolves.
	for _, tc := range []struct{ job, value string }{{"image-built-24", "24"}, {"image-built-26", "26"}} {
		j, ok := byName[tc.job]
		if !ok {
			t.Fatalf("missing chain link %q; jobs: %v", tc.job, names(m.Jobs))
		}
		if len(j.Matrix) != 1 || len(j.Matrix[0].Values) != 1 || j.Matrix[0].Values[0] != tc.value {
			t.Errorf("%s should fan over exactly %q, got %+v", tc.job, tc.value, j.Matrix)
		}
		if len(j.Steps) == 0 {
			t.Errorf("%s rendered hollow — a chain link must carry the node's steps", tc.job)
		}
	}
	// The ORDER: 26 waits on 24, and 24 does not wait on 26 (that would deadlock).
	if got := byName["image-built-26"].Needs; !contains(got, "image-built-24") {
		t.Errorf("image-built-26 must wait on image-built-24, needs: %v", got)
	}
	if got := byName["image-built-24"].Needs; contains(got, "image-built-26") {
		t.Errorf("image-built-24 must NOT wait on its successor (deadlock), needs: %v", got)
	}
	// The chain ADDS an edge, it does not replace the authored DAG: this node has no
	// upstream NODE (its only need is a tool), so the head link is unblocked and the
	// tail waits on exactly its predecessor.
	if got := byName["image-built-24"].Needs; len(got) != 0 {
		t.Errorf("the head link should be unblocked, got needs %v", got)
	}
	if got := byName["image-built-26"].Needs; len(got) != 1 {
		t.Errorf("the tail link should wait on its predecessor alone, got needs %v", got)
	}
	// The join carries the AUTHORED name and waits on every link, so the consumer edge
	// below is a real all-cells barrier.
	join, ok := byName["image-built"]
	if !ok {
		t.Fatalf("the join must keep the authored node name; jobs: %v", names(m.Jobs))
	}
	if !join.IsGate || len(join.Steps) != 0 {
		t.Errorf("the join must be a pure gate, got %+v", join)
	}
	if !contains(join.Needs, "image-built-24") || !contains(join.Needs, "image-built-26") {
		t.Errorf("the join must wait on every link, needs: %v", join.Needs)
	}
	// The whole point of the join: the consumer is UNTOUCHED by the split.
	if got := byName["image-scanned"].Needs; !contains(got, "image-built") {
		t.Errorf("consumer edge should still name the authored node, got %v", got)
	}
}

// TestSerialiseKeepsOtherAxesParallel pins that only the NAMED axis is walked: a
// memory-bound build wants one series at a time with that series' arches still running
// together, so a chain link keeps every other axis at full fan-out.
func TestSerialiseKeepsOtherAxesParallel(t *testing.T) {
	st, err := ci.Parse([]byte(`{
	  "image": "b19/node-{B19_NODE_SERIES}",
	  "matrix": {"axes": {"B19_NODE_SERIES": ["24", "26"], "M6E_ARCH": ["amd64", "arm64"]}},
	  "tools": {"container-build": {"action": "container-build"}},
	  "nodes": {"image-built": {"matrix": true, "goal": true, "serialise": "B19_NODE_SERIES", "needs": {"container-build": true}}}
	}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rm, err := resolve.Resolve(st)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	m := Build(rm, st, nil)
	if m.Err != nil {
		t.Fatalf("build: %v", m.Err)
	}
	for _, j := range m.Jobs {
		if j.Name != "image-built-24" {
			continue
		}
		for _, a := range j.Matrix {
			want := 1
			if a.Key == "M6E_ARCH" {
				want = 2 // untouched: the arches of ONE series still build together
			}
			if len(a.Values) != want {
				t.Errorf("axis %s: want %d value(s) on a chain link, got %v", a.Key, want, a.Values)
			}
		}
		return
	}
	t.Fatalf("no chain link rendered; jobs: %v", names(m.Jobs))
}

// TestSerialiseUnknownAxisIsNoOp pins the graceful degradation every axis-naming field
// shares (matrix.without, matrix.pin): naming an axis the project does not declare
// LOGS and leaves the job alone. That is what lets ONE shared m6e declaration render
// byte-identically on the projects that never had the axis.
func TestSerialiseUnknownAxisIsNoOp(t *testing.T) {
	st, err := ci.Parse([]byte(`{
	  "image": "b19/node-{B19_NODE_SERIES}",
	  "matrix": {"axes": {"B19_NODE_SERIES": ["24", "26"]}},
	  "tools": {"container-build": {"action": "container-build"}},
	  "nodes": {"image-built": {"matrix": true, "goal": true, "serialise": "M6E_ARCH", "needs": {"container-build": true}}}
	}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rm, err := resolve.Resolve(st)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	m := Build(rm, st, nil)
	if m.Err != nil {
		t.Fatalf("build: %v", m.Err)
	}
	for _, j := range m.Jobs {
		if strings.HasPrefix(j.Name, "image-built-") {
			t.Fatalf("an unmatched axis must not split the node, got job %q", j.Name)
		}
	}
	j, ok := jobByName(m, "image-built")
	if !ok || len(j.Matrix) != 1 || len(j.Matrix[0].Values) != 2 {
		t.Fatalf("the node should keep its full fan-out, got %+v", j)
	}
}

// TestSerialiseCollidingValuesFail pins the fail-closed guard on the job-id slug: two
// axis values may spell ONE id once the forge-illegal characters are folded (`3.14` and
// `3-14`), which would drop a link and build one value twice. The build refuses instead
// — a workflow short a cell is the kind of defect nobody notices until a release is wrong.
func TestSerialiseCollidingValuesFail(t *testing.T) {
	st, err := ci.Parse([]byte(`{
	  "image": "b19/python-{B19_PYTHON_SERIES}",
	  "matrix": {"axes": {"B19_PYTHON_SERIES": ["3.14", "3-14"]}},
	  "tools": {"container-build": {"action": "container-build"}},
	  "nodes": {"image-built": {"matrix": true, "goal": true, "serialise": "B19_PYTHON_SERIES", "needs": {"container-build": true}}}
	}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rm, err := resolve.Resolve(st)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if m := Build(rm, st, nil); m.Err == nil {
		t.Fatalf("colliding slugs must fail the build, got jobs %v", names(m.Jobs))
	}
}

func names(jobs []JobView) []string {
	out := make([]string, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, j.Name)
	}
	return out
}

func jobByName(m Model, name string) (JobView, bool) {
	for _, j := range m.Jobs {
		if j.Name == name {
			return j, true
		}
	}
	return JobView{}, false
}

// TestCellToCellFanInIsCoarse pins the CELL→CELL fan-in DECISION (general-plan
// task 7): GHA/Forgejo `needs:` between two matrix jobs is an ALL-CELLS barrier —
// the engine has no `needs: build[matrix.x == ...]` per-cell primitive — so a scan
// cell waits for EVERY build cell, not just its sibling. m6e pairs per cell only
// because it runs each cell as one `--scope=cell KEY=…` sub-make pass holding the
// whole CELL slice (build→scan together); pf-ci splits per tool (the locked
// one-job-per-tool / edge-level rule), so the pairing is lost at the tool boundary.
//
// We ACCEPT this: the coarse gate costs only parallelism (a straggler build cell
// delays the scan cells), never correctness — per-cell CORRECTNESS rides on the
// cell-keyed artifact Stem, NOT on the gate. This test is the tripwire: if the
// contracted edge or the cell key ever changes shape, it fails loudly.
func TestCellToCellFanInIsCoarse(t *testing.T) {
	m, _ := mustModel(t)
	// build/scan are now STEPS; their host node-jobs (image-built / image-tested) carry
	// the matrix and the coarse edge.
	build, scan := jobOf(m, testContainerBuild), jobOf(m, "grype-scan-image")

	// Both ends of the edge are CELL matrix jobs over the same axis.
	for _, j := range []JobView{build, scan} {
		if j.Class != testCell || len(j.Matrix) != 1 {
			t.Fatalf("%s should be a single-axis CELL job: %+v", j.Name, j)
		}
	}
	// The node edge: image-tested (hosting the scan) waits on the image-built NODE,
	// not a contracted leaf. Exactly one upstream — a plain node name, so the barrier
	// is coarse (it cannot carry a per-cell token).
	if len(scan.Needs) != 1 || scan.Needs[0] != testImageBuilt {
		t.Fatalf("scan node should need exactly image-built, got %v", scan.Needs)
	}
	// Per-cell correctness carrier: each cell's artifact is keyed by its axis value,
	// so scan(resolute) reads image-resolute.tar even though the edge is coarse.
	if got := steps(m)["grype-scan-image"].Stem; got != testImageMatrixStem {
		t.Fatalf("scan step stem must be cell-keyed (the pairing the edge cannot express), got %q", got)
	}

	// In the rendered workflow the barrier is a PLAIN job name — no `needs:` line
	// may smuggle a `matrix.` token (GHA has no such form; asserting its absence
	// documents that the per-cell pairing is deliberately NOT in the gate).
	out, err := Workflow(m, Targets[TargetGHA], ci.Platform{})
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "needs:") && strings.Contains(line, "matrix.") {
			t.Errorf("a needs: line carries a per-cell matrix token — GHA cannot pair cells: %q", line)
		}
	}
	if !strings.Contains(string(out), "image-tested:\n    runs-on: ubuntu-latest\n    needs: [\"image-built\"]") {
		t.Errorf("scan node lost its coarse all-cells barrier on image-built:\n%s", out)
	}
}

// TestGuardIsNotLowered pins the contract that a tool's `guard` predicate is
// IGNORED by this lowering: the run step is a plain inline command, with no
// inline existence prelude and no block scalar. Applicability is a MEMBERSHIP
// concern (include the fragment ⟺ have the file) and the d9t runner self-guards a
// genuinely-absent file; the make lowering still honours `guard` via `test -f`.
func TestGuardIsNotLowered(t *testing.T) {
	const guarded = `{
  "tools": {
    "hadolint": {"image": "reg.example/go-tools:latest", "run": "auto-hadolint", "guard": "**/Dockerfile"}
  },
  "nodes": {
    "source-is-valid": {"goal": true, "needs": {"hadolint": true}}
  }
}`
	st, err := ci.Parse([]byte(guarded))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rm, err := resolve.Resolve(st)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	out, err := Workflow(Build(rm, st, nil), Targets[TargetGHA], ci.Platform{})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	// The guard is dropped: the image tool reaches its image via run-tool with a clean
	// `run:` input, exactly as an UNGUARDED image tool would render (no prelude wrapping).
	if !strings.Contains(s, "uses: projectfile/ci-actions/run-tool@v1") || !strings.Contains(s, "\n          run: auto-hadolint\n") {
		t.Errorf("guarded image tool should render a plain run-tool action\ngot:\n%s", s)
	}
	// No inline prelude leaked through, and no block scalar was emitted for it.
	if strings.Contains(s, "[ -e ") || strings.Contains(s, "guard ") {
		t.Errorf("guard prelude must NOT be emitted\ngot:\n%s", s)
	}
	if n := strings.Count(s, "run: |"); n != 0 {
		t.Errorf("want 0 block-scalar runs (guard not lowered), got %d", n)
	}
}

// TestAgnosticImageComposition proves the manifest image stays vendor-NEUTRAL and
// the concrete ref is composed at GENERATION time: a bare registry-relative path gets
// `<path>:<tag-var>` where the tag is the single ImageTagVar expression (one runtime
// variable, `latest` fallback), and an already-`:tag`-bearing ref is emitted verbatim
// (the tag-presence rule), untouched by the tag variable.
func TestAgnosticImageComposition(t *testing.T) {
	const subtree = `{
  "tools": {
    "golangci-lint": {"image": "d9t/go-tools", "run": "auto-golangci-lint"},
    "hadolint":      {"image": "hadolint/hadolint:v2.14.0", "run": "auto-hadolint"},
    "mdlint":        {"image": "davidanson/markdownlint", "run": "auto-mdlint"}
  },
  "nodes": {
    "source-is-valid": {"goal": true, "needs": {"golangci-lint": true, "hadolint": true, "mdlint": true}}
  }
}`
	st, err := ci.Parse([]byte(subtree))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rm, err := resolve.Resolve(st)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	m := Build(rm, st, nil)
	// The neutral model keeps the agnostic source string — no registry/tag baked in.
	if got := steps(m)["golangci-lint"].Image; got != "d9t/go-tools" {
		t.Errorf("model image should stay agnostic, got %q", got)
	}

	// The image is passed to run-tool as TWO SPLIT inputs (image/version) — `registry:`
	// is not threaded on the RUN side (the registry rides the image var value); the
	// PUSH side threads it via oci-push's `registry:`. A bare path gets the global tag
	// var; a tag-bearing ref is SPLIT at the `:` (its own version, the tag var never
	// touching it).
	const tagVar = "${{ env." + TagEnvVar + " }}"
	base, err := Workflow(m, Targets[TargetGHA], ci.Platform{})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"image: d9t/go-tools\n          version: " + tagVar,            // bare path, tag var
		"image: davidanson/markdownlint\n          version: " + tagVar, // no registry => Docker Hub (bare)
		"image: hadolint/hadolint\n          version: v2.14.0",         // tag-bearing => split verbatim
	} {
		if !strings.Contains(string(base), want) {
			t.Errorf("render missing %q\n%s", want, base)
		}
	}
	// No registry line for any tool — registry is no longer threaded through the manifest.
	if strings.Contains(string(base), "\n          registry:") {
		t.Errorf("no tool should emit a registry line (registry field removed from Manifest)\n%s", base)
	}
}

// TestImageVarExpression proves that a tool manifest whose `image:` value is an
// uppercase ci.images VAR NAME (the agnostic-revolution new form) is resolved to its
// forge-plane registry PATH at Build time — no per-tool override tier:
//
//	B19_GO_IMAGE       -> reg.example/b19/go
//	D9T_JS_TOOLS_IMAGE -> ${{ vars.D9T_DOCKER_REGISTRY }}/d9t/js-tools
//
// The path comes from the ci.images value (`:tag` stripped); a var with no own entry
// resolves through its longest-_IMAGE-prefix parent. With a nil Build, or a var with
// neither entry nor parent, the image degrades to a bare ${{ vars.<VAR> }}.
func TestImageVarExpression(t *testing.T) {
	const subtree = `{
  "tools": {
    "go-vet":   {"image": "B19_GO_VET_IMAGE",  "run": "auto-go-vet"},
    "go-fmt":   {"image": "B19_GO_IMAGE",       "run": "auto-go-fmt"},
    "js":       {"image": "D9T_JS_TOOLS_IMAGE", "run": "auto-markdownlint"},
    "orphan":   {"image": "ORPHAN_TOOLS_IMAGE", "run": "auto-orphan"}
  },
  "nodes": {
    "code-is-valid": {"goal": true, "needs": {"go-vet": true, "go-fmt": true, "js": true, "orphan": true}}
  }
}`
	build := &ci.Build{
		Images: map[string]string{
			// The real-world shape (survey of m6e/*/tools/*.yaml): registry AND tag are
			// make vars embedded in the ci.images value. The forge plane must lower the
			// registry to a juxtaposed ${{ vars.* }} fragment (it never expands `${...}`),
			// else the verbatim ref is invalid; the flip-var TAG lowers to the dev↔latest
			// expression via runToolVersion, so no workspace image bakes a literal tag.
			"B19_GO_IMAGE":   "${B19_DOCKER_REGISTRY}/b19/go:${M6E_BASE_IMAGE_DEFAULT_VERSION}",
			testJsToolsImage: "${D9T_DOCKER_REGISTRY}/d9t/js-tools:${M6E_BASE_IMAGE_DEFAULT_VERSION}",
		},
	}
	st, err := ci.Parse([]byte(subtree))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rm, err := resolve.Resolve(st)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	// With a Build: a var resolved via parent (go-vet), via its own entry (go-fmt, js),
	// and a degenerate orphan (var not in ci.images, no parent → bare vars ref).
	m := Build(rm, st, build)
	s := steps(m)
	cases := []struct{ tool, want string }{
		{"go-vet", testB19GoImage},
		{"go-fmt", testB19GoImage},
		{"js", "${{ vars.D9T_DOCKER_REGISTRY }}/d9t/js-tools"},
		{"orphan", "${{ vars.ORPHAN_TOOLS_IMAGE }}"},
	}
	for _, c := range cases {
		if got := s[c.tool].Image; got != c.want {
			t.Errorf("%s Image: got %q, want %q", c.tool, got, c.want)
		}
	}

	// With nil Build: every var-name tool falls back to single-tier.
	mNil := Build(rm, st, nil)
	sNil := steps(mNil)
	if got := sNil["go-vet"].Image; got != "${{ vars.B19_GO_VET_IMAGE }}" {
		t.Errorf("nil build go-vet Image: got %q, want single-tier ${{ vars.B19_GO_VET_IMAGE }}", got)
	}
	if got := sNil["go-fmt"].Image; got != "${{ vars.B19_GO_IMAGE }}" {
		t.Errorf("nil build go-fmt Image: got %q, want single-tier ${{ vars.B19_GO_IMAGE }}", got)
	}

	// In the rendered workflow the resolved registry path appears as the `image:` input to run-tool.
	const tagVar = "${{ env." + TagEnvVar + " }}"
	out, err := Workflow(m, Targets[TargetGHA], ci.Platform{})
	if err != nil {
		t.Fatal(err)
	}
	wantExpr := "image: " + testB19GoImage + "\n          version: " + tagVar
	if !strings.Contains(string(out), wantExpr) {
		t.Errorf("rendered workflow missing resolved image path:\nwant: %q\n%s", wantExpr, out)
	}
}

// TestExternalPinnedToolImage pins the hadolint-shaped case that the rate-limit failure
// exposed: a ci.images VAR NAME whose value is an EXTERNAL vendor ref with a HARD tag pin
// (`hadolint/hadolint:v2.14.0`) — no registry make-var. Three properties must hold, none
// of which the pre-fix lowering gave:
//
//   - the pin ROUND-TRIPS as `version: v2.14.0` (was: dropped → `latest` → rate-limited);
//   - the path carries an INSTANCE override `${{ vars.HADOLINT_IMAGE || 'hadolint/hadolint' }}`
//     so an instance points it at a mirror (was: bare literal, no knob);
//   - `pull: missing` (was: `always`, re-hitting Docker Hub every run on an immutable tag).
//
// A WORKSPACE image (registry make-var) in the same subtree must NOT get the override wrap
// and must keep the mutable flip tag with no pull line — the guard that stops a whole-image
// var from shadowing a registry path.
func TestExternalPinnedToolImage(t *testing.T) {
	const subtree = `{
  "tools": {
    "hadolint": {"image": "HADOLINT_IMAGE",    "run": "auto-hadolint"},
    "go-fmt":   {"image": "GO_TOOL_IMAGE",      "run": "auto-go-fmt"}
  },
  "nodes": {
    "source-is-valid": {"goal": true, "needs": {"hadolint": true, "go-fmt": true}}
  }
}`
	build := &ci.Build{
		Images: map[string]string{
			"HADOLINT_IMAGE": "hadolint/hadolint:v2.14.0",
			"GO_TOOL_IMAGE":  "${B19_DOCKER_REGISTRY}/b19/go:${M6E_BASE_IMAGE_DEFAULT_VERSION}",
		},
	}
	st, err := ci.Parse([]byte(subtree))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rm, err := resolve.Resolve(st)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	m := Build(rm, st, build)
	s := steps(m)

	if got, want := s["hadolint"].Image, "${{ vars.HADOLINT_IMAGE || 'hadolint/hadolint' }}"; got != want {
		t.Errorf("hadolint Image (override knob): got %q, want %q", got, want)
	}
	if got, want := s["hadolint"].PinnedTag, "v2.14.0"; got != want {
		t.Errorf("hadolint PinnedTag (preserve pin): got %q, want %q", got, want)
	}
	if got := s["hadolint"].RunToolPull(); got != "missing" {
		t.Errorf("hadolint RunToolPull: got %q, want missing (immutable pin)", got)
	}
	// Workspace image: registry-var path, flip tag, no override wrap, no pull line.
	if got, want := s["go-fmt"].Image, testB19GoImage; got != want {
		t.Errorf("go-fmt Image (no override wrap): got %q, want %q", got, want)
	}
	if got := s["go-fmt"].PinnedTag; got != "" {
		t.Errorf("go-fmt PinnedTag: got %q, want empty (mutable flip tag)", got)
	}
	if got := s["go-fmt"].RunToolPull(); got != "" {
		t.Errorf("go-fmt RunToolPull: got %q, want empty (mutable => always default)", got)
	}

	out, err := Workflow(m, Targets[TargetGHA], ci.Platform{})
	if err != nil {
		t.Fatal(err)
	}
	wantStep := "image: ${{ vars.HADOLINT_IMAGE || 'hadolint/hadolint' }}\n          version: v2.14.0\n          pull: missing\n"
	if !strings.Contains(string(out), wantStep) {
		t.Errorf("rendered workflow missing pinned/override/pull step:\nwant: %q\n%s", wantStep, out)
	}
}

// TestMatrixOverridesLowering pins the matrix.overrides lowering end-to-end (the
// b19/zig case): a per-series LLVM build-arg varies per cell. The container-build step
// forwards B19_LLVM_SERIES as ${{ matrix.B19_LLVM_SERIES }} (matrix-passed, NOT a
// vars-store lookup — the var is bound on EVERY cell, so no default backstop leaks in),
// the image basename substitutes the axis, and the rendered workflow carries a
// strategy.matrix.include with each cell's derived LLVM value.
func TestMatrixOverridesLowering(t *testing.T) {
	const subtree = `{
	  "image": "b19/zig-{B19_ZIG_SERIES}",
	  "matrix": {
	    "axes": {"B19_ZIG_SERIES": ["0.16", "0.15"]},
	    "overrides": [
	      {"B19_ZIG_SERIES": "0.16", "B19_LLVM_SERIES": "21"},
	      {"B19_ZIG_SERIES": "0.15", "B19_LLVM_SERIES": "20"}
	    ]
	  },
	  "tools": {"container-build": {"action": "container-build"}},
	  "nodes": {"image-built": {"matrix": true, "needs": {"container-build": true}},
	            "published":   {"goal": true, "needs": {"image-built": true}}}
	}`
	// org.projectfile.build.args as the make reader merges them: B19_LLVM_SERIES
	// carries a static default (22) and a composed base-image ref that embeds it —
	// both must be overridden by the per-cell matrix value, not lowered as vars lookups.
	build := &ci.Build{
		Args: []ci.BuildInput{
			{Name: "B19_LLVM_SERIES", Default: "22"},
			{Name: "B19_LLVM_BASE_IMAGE", Default: "${B19_DOCKER_REGISTRY}/b19/llvm-${B19_LLVM_SERIES}"},
		},
	}
	st, err := ci.Parse([]byte(subtree))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rm, err := resolve.Resolve(st)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	m := Build(rm, st, build)
	cb := steps(m)[testContainerBuild]

	// The extra var is matrix-passed: its env binding is ${{ matrix.B19_LLVM_SERIES }}.
	const wantLLVM = "${{ matrix.B19_LLVM_SERIES }}"
	gotLLVM := ""
	for _, e := range cb.Env {
		if e.Key == "B19_LLVM_SERIES" {
			gotLLVM = e.Value
		}
	}
	if gotLLVM != wantLLVM {
		t.Errorf("B19_LLVM_SERIES env: want %q, got %q", wantLLVM, gotLLVM)
	}
	// B19_LLVM_SERIES is forwarded as a build-arg name (the cell carries it), and the
	// axis too. Neither may lower to a vars-store lookup anywhere in the step env.
	if !strings.Contains(cb.BuildArgNames, "B19_LLVM_SERIES") {
		t.Errorf("BuildArgNames must include B19_LLVM_SERIES, got %q", cb.BuildArgNames)
	}
	if !strings.Contains(cb.BuildArgNames, "B19_ZIG_SERIES") {
		t.Errorf("BuildArgNames must include the axis B19_ZIG_SERIES, got %q", cb.BuildArgNames)
	}
	for _, e := range cb.Env {
		if strings.Contains(e.Value, "vars.B19_LLVM_SERIES") {
			t.Errorf("B19_LLVM_SERIES must be matrix-passed, not a vars lookup: %s=%s", e.Key, e.Value)
		}
	}
	// The image basename substitutes the axis placeholder per cell.
	if want := "b19/zig-${{ matrix.B19_ZIG_SERIES }}"; cb.ImageBasename != want {
		t.Errorf("ImageBasename: want %q, got %q", want, cb.ImageBasename)
	}

	// The rendered workflow carries strategy.matrix.include with both cells, the
	// fields key-sorted within each entry and entries in declared order.
	out, err := Workflow(m, Targets[TargetGHA], ci.Platform{})
	if err != nil {
		t.Fatal(err)
	}
	wantInclude := strings.TrimLeft(`        include:
          - B19_LLVM_SERIES: "21"
            B19_ZIG_SERIES: "0.16"
          - B19_LLVM_SERIES: "20"
            B19_ZIG_SERIES: "0.15"`, " ")
	if !strings.Contains(string(out), wantInclude) {
		t.Errorf("rendered workflow missing the include block:\nwant:\n%s\ngot:\n%s", wantInclude, out)
	}
}

// TestMatrixExcludeRender pins the exclude lowering on BOTH forges: a node matrix's
// `exclude` rows reach strategy.matrix.exclude with their fields key-sorted and
// quoted exactly as the axis values are, so the forge drops the same cells the make
// and Tekton lowerings drop. The driver is a platform grid with an impossible corner
// (no darwin/riscv64 toolchain) that must stay ONE node, because the artifact
// hand-off is keyed by the cell.
func TestMatrixExcludeRender(t *testing.T) {
	subtree := `{
	  "nodes": {"binaries-built": {"goal": true,
	    "matrix": {"axes": {"GOOS": ["linux", "darwin"], "GOARCH": ["amd64", "arm64", "riscv64"]},
	               "exclude": [{"GOOS": "darwin", "GOARCH": "riscv64"}]},
	    "needs": {"build-binaries": true}}}
	}`
	st, err := ci.Parse([]byte(subtree))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rm, err := resolve.Resolve(st)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	m := Build(rm, st, nil)
	wantExclude := strings.TrimLeft(`        exclude:
          - GOARCH: "riscv64"
            GOOS: "darwin"`, " ")
	for _, target := range []string{TargetGHA, TargetForgejo} {
		out, err := Workflow(m, Targets[target], ci.Platform{})
		if err != nil {
			t.Fatalf("%s: %v", target, err)
		}
		if !strings.Contains(string(out), wantExclude) {
			t.Errorf("%s workflow missing the exclude block:\nwant:\n%s\ngot:\n%s", target, wantExclude, out)
		}
		// The axes themselves stay whole — exclude subtracts cells, never values.
		if !strings.Contains(string(out), `GOARCH: ["amd64", "arm64", "riscv64"]`) {
			t.Errorf("%s workflow must keep the full GOARCH axis, got:\n%s", target, out)
		}
	}
}

// TestMatrixOverridesPartialFallback pins the b19/llvm case: an override decorates
// ONLY the exception cell (series 20 -> noble), leaving series 21/22 undecorated.
// Both the standalone extra-var binding and the composed FROM ref that embeds it must
// backstop to the build-arg default (resolute) so the un-decorated cells resolve to a
// valid image instead of an empty ${{ matrix.B19_UBUNTU_SERIES }}. The cell's matrix
// value still wins wherever the override set it (|| is first-truthy).
func TestMatrixOverridesPartialFallback(t *testing.T) {
	const subtree = `{
	  "image": "b19/llvm-{B19_LLVM_SERIES}",
	  "matrix": {
	    "axes": {"B19_LLVM_SERIES": ["22", "21", "20"]},
	    "overrides": [
	      {"B19_LLVM_SERIES": "20", "B19_UBUNTU_SERIES": "noble"}
	    ]
	  },
	  "tools": {"container-build": {"action": "container-build"}},
	  "nodes": {"image-built": {"matrix": true, "needs": {"container-build": true}},
	            "published":   {"goal": true, "needs": {"image-built": true}}}
	}`
	// B19_UBUNTU_SERIES carries a static default (resolute) plus a composed base ref
	// embedding it — the include-merged shape from m6e/b19/bases/ubuntu.yaml.
	build := &ci.Build{
		Args: []ci.BuildInput{
			{Name: testUbuntuSeries, Default: testResolute},
			{Name: testUbuntuBaseImage, Default: "${B19_DOCKER_REGISTRY}/b19/ubuntu/${B19_UBUNTU_SERIES}"},
		},
	}
	st, err := ci.Parse([]byte(subtree))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rm, err := resolve.Resolve(st)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	cb := steps(Build(rm, st, build))[testContainerBuild]

	env := map[string]string{}
	for _, e := range cb.Env {
		env[e.Key] = e.Value
	}
	// Standalone extra-var binding: matrix value, else vars store, else the default.
	if want := "${{ matrix.B19_UBUNTU_SERIES || vars.B19_UBUNTU_SERIES || 'resolute' }}"; env[testUbuntuSeries] != want {
		t.Errorf("B19_UBUNTU_SERIES env: want %q, got %q", want, env[testUbuntuSeries])
	}
	// The composed FROM ref embeds the SAME fallback for the series segment, so an
	// un-decorated cell never lowers to `.../b19/ubuntu/:` (empty series).
	if got := env[testUbuntuBaseImage]; !strings.Contains(got, "matrix.B19_UBUNTU_SERIES || vars.B19_UBUNTU_SERIES || 'resolute'") {
		t.Errorf("B19_UBUNTU_BASE_IMAGE must backstop the series to the default, got %q", got)
	}
}

// TestPlatformOverlay proves the org.projectfile.ci.gha deployment overlay paints
// onto the workflow: a runner override (replacing the adapter default on EVERY
// job), a per-job timeout, workflow-level permissions (key-sorted) and a
// concurrency group — and that an absent overlay (ci.Platform{}) changes nothing.
func TestPlatformOverlay(t *testing.T) {
	m, _ := mustModel(t)
	plat := ci.Platform{
		RunsOn:         []string{"self-hosted", "linux"},
		TimeoutMinutes: 30,
		Permissions:    map[string]string{"packages": "write", "contents": "read"},
		Concurrency:    &ci.Concurrency{Group: "ci-${{ github.ref }}", CancelInProgress: true},
	}
	out, err := Workflow(m, Targets[TargetGHA], plat)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{
		`runs-on: ["self-hosted", "linux"]`,                 // list-form runner override
		"timeout-minutes: 30",                               // per-job timeout
		"permissions:\n  contents: read\n  packages: write", // key-sorted, workflow-level
		"concurrency:\n  group: ci-${{ github.ref }}\n  cancel-in-progress: true",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("overlay workflow missing %q\n---\n%s", want, s)
		}
	}
	// The override must hit EVERY job, never the adapter default.
	if strings.Contains(s, "runs-on: ubuntu-latest") {
		t.Errorf("runs-on override leaked the adapter default ubuntu-latest:\n%s", s)
	}
	// An absent overlay is a no-op: no permissions/concurrency/timeout appear.
	base, _ := Workflow(m, Targets[TargetGHA], ci.Platform{})
	for _, absent := range []string{"permissions:", "concurrency:", "timeout-minutes:"} {
		if strings.Contains(string(base), absent) {
			t.Errorf("empty overlay should emit no %q", absent)
		}
	}
}

// TestNodeConcurrency proves a node's manifest `concurrency:` lowers to a JOB-level
// block on THAT node's job only — carried verbatim from the manifest (not synthesised
// from the tool), and absent on every other job.
func TestNodeConcurrency(t *testing.T) {
	const src = `{
  "tools": {"publish": {"run": "make publish"}},
  "nodes": {
    "source-is-valid": {"needs": {"shellcheck": true}},
    "published": {"goal": true, "concurrency": {"group": "publish-b19/ubuntu"}, "needs": {"source-is-valid": true, "publish": true}}
  }
}`
	st, err := ci.Parse([]byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rm, err := resolve.Resolve(st)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	out, err := Workflow(Build(rm, st, nil), Targets[TargetGHA], ci.Platform{})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	// 6-space indent => the JOB-level block (the workflow-level one is 2-space).
	want := "    concurrency:\n      group: publish-b19/ubuntu\n      cancel-in-progress: false"
	if !strings.Contains(s, want) {
		t.Errorf("missing job concurrency block:\nwant:\n%s\ngot:\n%s", want, s)
	}
	// The guard rides ONE job, not every job.
	if n := strings.Count(s, "\n      group: "); n != 1 {
		t.Errorf("want exactly one job-level concurrency group, got %d\n%s", n, s)
	}
}

// TestTargetsDifferOnlyByAdapter is the "implement GHA + Forgejo at once" proof:
// the two outputs differ ONLY in the adapter tokens (runner label, regenerate
// hint, artifact action versions) — the job graph, ordering and matrix are
// identical. (Checkout is now actions/checkout@v7 on both; it used to be a
// divergence. The artifact actions DO still diverge: gha rides the v2 protocol
// majors — download@v8 / upload@v7 — while forgejo is pinned to @v3, because
// Forgejo's backend only speaks the v1 artifact protocol; both fold to one token.)
func TestTargetsDifferOnlyByAdapter(t *testing.T) {
	m, _ := mustModel(t)
	gha, _ := Workflow(m, Targets[TargetGHA], ci.Platform{})
	forgejo, _ := Workflow(m, Targets[TargetForgejo], ci.Platform{})

	normalise := func(b []byte) string {
		r := strings.NewReplacer(
			"ubuntu-latest", "RUNNER",
			"docker", "RUNNER",
			"actions/checkout@v7", "CHECKOUT",
			"actions/download-artifact@v8", "DOWNLOAD",
			"actions/download-artifact@v3", "DOWNLOAD",
			"actions/upload-artifact@v7", "UPLOAD",
			"actions/upload-artifact@v3", "UPLOAD",
			"--target gha", "--target T",
			"--target forgejo", "--target T",
		)
		return r.Replace(string(b))
	}
	if normalise(gha) != normalise(forgejo) {
		t.Errorf("targets diverge beyond the adapter tokens:\nGHA:\n%s\nForgejo:\n%s", gha, forgejo)
	}
}

// TestArtifactProtocolPerTarget pins the Forgejo platform constraint: upload/
// download-artifact@v4+ ride `@actions/artifact` v2, which refuses any non-github.com
// server as GHES, so Forgejo (v1 artifact protocol) MUST stay on @v3 while GitHub keeps
// the current majors. This is the regression guard for the "GHESNotSupportedError" the
// runner hit — keep it a per-target default, never a forced downgrade of GitHub.
func TestArtifactProtocolPerTarget(t *testing.T) {
	for _, c := range []struct{ key, download, upload string }{
		{TargetGHA, "actions/download-artifact@v8", "actions/upload-artifact@v7"},
		{TargetForgejo, "actions/download-artifact@v3", "actions/upload-artifact@v3"},
	} {
		tg := Targets[c.key]
		if tg.Download != c.download {
			t.Errorf("%s Download = %q, want %q", c.key, tg.Download, c.download)
		}
		if tg.Upload != c.upload {
			t.Errorf("%s Upload = %q, want %q", c.key, tg.Upload, c.upload)
		}
	}
}

// membershipSubtree exercises the target-membership opt-out: `grype-scan-image`
// is disabled on forgejo (registry push is per-vendor), `mdformat` is m6e-only (CI
// never commits a reformat). The graph is otherwise shared, so the gha and forgejo
// renders must differ ONLY in which tool-jobs survive — and never dangle.
const membershipSubtree = `{
  "tools": {
    "shellcheck": {"image": "reg/misc:latest"},
    "grype-scan-image": {"image": "reg/go:latest", "forgejo": false},
    "mdformat": {"image": "reg/py:latest", "gha": false, "forgejo": false}
  },
  "nodes": {
    "linted": {"needs": {"shellcheck": true}},
    "scanned": {"needs": {"grype-scan-image": true}},
    "formatted": {"needs": {"mdformat": true}},
    "ready": {"goal": true, "needs": {"linted": true, "scanned": true, "formatted": true}}
  }
}`

// jobNames renders one target's workflow and returns the set of job names present.
func jobNames(t *testing.T, targetKey string) map[string]bool {
	t.Helper()
	st, err := ci.Parse([]byte(membershipSubtree))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	target := Targets[targetKey]
	rm, err := resolve.Resolve(st.ForTarget(target.Key))
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	m := Build(rm, st.ForTarget(target.Key), nil)
	names := map[string]bool{}
	for _, j := range m.Jobs {
		names[j.Name] = true
		// A tool now runs as a STEP of its node-job; record the tool names too so the
		// membership assertions ("does grype-scan-image run") still see them.
		for _, s := range j.Steps {
			names[s.Name] = true
		}
	}
	return names
}

func TestTargetMembershipPrunesTools(t *testing.T) {
	gha := jobNames(t, "gha")
	forgejo := jobNames(t, TargetForgejo)

	// mdformat is m6e-only: absent from BOTH committed workflows.
	if gha["mdformat"] || forgejo["mdformat"] {
		t.Errorf("mdformat (m6e-only) leaked into a committed workflow: gha=%v forgejo=%v", gha["mdformat"], forgejo["mdformat"])
	}
	// grype-scan-image runs on gha, not forgejo.
	if !gha["grype-scan-image"] {
		t.Errorf("grype-scan-image should run on gha")
	}
	if forgejo["grype-scan-image"] {
		t.Errorf("grype-scan-image is forgejo:false — it must not appear in the forgejo workflow")
	}
	// The always-on tool survives on both.
	if !gha[testShellcheck] || !forgejo[testShellcheck] {
		t.Errorf("shellcheck (no membership flag) must appear on both: gha=%v forgejo=%v", gha[testShellcheck], forgejo[testShellcheck])
	}
}

// TestTargetMembershipNoDangle proves the contraction: when a pruned tool was a
// node's only leaf, the rendered workflow must not reference it in any `needs:`.
func TestTargetMembershipNoDangle(t *testing.T) {
	st, _ := ci.Parse([]byte(membershipSubtree))
	rm, _ := resolve.Resolve(st.ForTarget(TargetForgejo))
	out, err := Workflow(Build(rm, st.ForTarget(TargetForgejo), nil), Targets[TargetForgejo], ci.Platform{})
	if err != nil {
		t.Fatal(err)
	}
	for _, ghost := range []string{"grype-scan-image", "mdformat"} {
		if strings.Contains(string(out), ghost) {
			t.Errorf("pruned tool %q still referenced in forgejo workflow (dangling needs):\n%s", ghost, out)
		}
	}
}

// providerSubtree marks container-build as a PROVIDER leaf (no image, no make):
// the renderer must dispatch it to the native provider partial, not the portable
// default. grype-scan-image is left a portable image tool so this fixture renders
// fully; service-up is the DEFERRED provider proving the loud-error guard.
// M6E_NAMESPACE/M6E_PROJECT are the post-Load literals of ${image.namespace} /
// ${image.name} (image b19/ubuntu): render sees a Subtree AFTER ci.Load's
// interpolation pass, so the identity args arrive already resolved (this test
// bypasses Load; the ${…}→literal derivation itself is covered in internal/ci).
const providerSubtree = `{
  "image": "b19/ubuntu",
  "matrix": {"axes": {"B19_UBUNTU_SERIES": ["resolute", "noble"]}},
  "tools": {
    "container-build": {"action": "container-build", "args": {
      "SOURCE_DOCKER_REGISTRY": {"var": "SOURCE_DOCKER_REGISTRY"},
      "B19_UBUNTU_SERIES": {"var": "B19_UBUNTU_SERIES"},
      "M6E_NAMESPACE": "b19",
      "M6E_PROJECT": "ubuntu",
      "M6E_VERSION": {"ci": "version"},
      "B19_UBUNTU_HASH": {"file": ".container/foundation/deps/ubuntu/{B19_UBUNTU_SERIES}.sha256.deps"}
    }, "mounts": [{"from": "fetch", "to": "/fetch"}]},
    "grype-scan-image": {"image": "reg.example/go-tools:latest", "run": "auto-grype"}
  },
  "nodes": {
    "image-built": {"matrix": true, "needs": {"container-build": true}},
    "image-tested": {"matrix": true, "needs": {"image-built": true, "grype-scan-image": true}},
    "ready-to-publish": {"goal": true, "needs": {"image-tested": true}}
  }
}`

// TestProviderLeafRenders proves a container-build PROVIDER leaf lowers to the
// EXTERNAL provider action ref (Q3) + its `with:` inputs, NOT an inline buildx
// recipe and NOT `make` in the cloud. The recipe now lives in projectfile/ci-actions.
func TestProviderLeafRenders(t *testing.T) {
	st, err := ci.Parse([]byte(providerSubtree))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rm, err := resolve.Resolve(st)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	out, err := Workflow(Build(rm, st, nil), Targets[TargetGHA], ci.Platform{})
	if err != nil {
		t.Fatalf("workflow: %v", err)
	}
	s := string(out)
	for _, want := range []string{
		"uses: projectfile/ci-actions/container-build/buildx@v1", // externalised, pinned, backend-keyed ref
		"with:",
		"artifact-name: image-${{ matrix.B19_UBUNTU_SERIES }}", // cell-keyed hand-off stem
		"build-args: |-", // NAME=VALUE block (value rides the input — composite can't read job env:)
		"            SOURCE_DOCKER_REGISTRY=${{ vars.SOURCE_DOCKER_REGISTRY }}", // a build-arg's VALUE rides the action input
		"B19_UBUNTU_SERIES: ${{ matrix.B19_UBUNTU_SERIES }}",                    // axis VALUE ALSO stays on the job env: map (non-composite steps)
	} {
		if !strings.Contains(s, want) {
			t.Errorf("provider workflow missing %q\n---\n%s", want, s)
		}
	}
	// Q3 REGRESSION: the recipe must have MOVED OUT of the resolver — no inline
	// buildx/upload steps may survive in the rendered workflow.
	for _, gone := range []string{"docker buildx build", "setup-buildx-action", testUploadArtifact} {
		if strings.Contains(s, gone) {
			t.Errorf("provider recipe %q leaked inline — it must live in the external action:\n%s", gone, s)
		}
	}
	// REGRESSION: the file: arg's decomment/grep must NOT be inlined — that shell lives
	// in the action — and the mounts list stays a clean scalar. (build-args is now a
	// NAME=VALUE block: the value MUST ride the action input because a composite action
	// cannot read the job env: as process env — the canonical action-input shape, cf.
	// docker/build-push-action's own newline KEY=VALUE `build-args`.)
	for _, gone := range []string{"mounts: |", "$GITHUB_ENV", "grep -v"} {
		if strings.Contains(s, gone) {
			t.Errorf("%q leaked into the rendered workflow — it must be declarative:\n%s", gone, s)
		}
	}
	// A provider leaf MUST NOT degrade to a host make invocation.
	if strings.Contains(s, "make container-build") {
		t.Errorf("container-build provider leaked `make` into the cloud workflow:\n%s", s)
	}
	// init-shared-scripts was REMOVED: ci-actions is now self-contained — labels
	// and version come from the pf-cli/git resolvers vendored at lib/, and the
	// standard m6e build-args are added inline, so the container-build action no
	// longer clones the m6e submodule. The build action step itself must remain.
	if strings.Contains(s, "init-shared-scripts") {
		t.Errorf("container-build job still emits the removed init-shared-scripts step:\n%s", s)
	}
	if !strings.Contains(s, "\n      - name: container-build\n        uses: projectfile/ci-actions/container-build/buildx@v1") {
		t.Errorf("container-build provider body lost its 6-space indent under steps:\n%s", s)
	}
	// The portable scanner still takes the default path: an image tool reaches its
	// image via run-tool (split image + version), with its entrypoint as the run input.
	if !strings.Contains(s, "image: reg.example/go-tools\n          version: latest") || !strings.Contains(s, "run: auto-grype") {
		t.Errorf("portable grype-scan-image lost its run-tool image/run path:\n%s", s)
	}
}

// TestBuildArgsPassthrough pins the native-YAML lowering of manifest `args`: build-arg
// NAME=VALUE pairs lower to a `build-args:` block scalar (the canonical action-input
// shape — the VALUE rides the input because a composite action can't read the job env:
// as process env; the value ALSO stays on the job env: map for non-composite steps), a
// `file:` arg lowers to a `file-args:` scalar the action reads itself (NO inline grep),
// and `mounts:` is a clean scalar — zero inline shell in the rendered workflow.
func TestBuildArgsPassthrough(t *testing.T) {
	st, err := ci.Parse([]byte(providerSubtree))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rm, err := resolve.Resolve(st)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	out, err := Workflow(Build(rm, st, nil), Targets[TargetGHA], ci.Platform{})
	if err != nil {
		t.Fatalf("workflow: %v", err)
	}
	s := string(out)
	// The action receives a NAME=VALUE block (sorted); the value rides the input so it
	// survives the composite-action boundary (the runner drops the job env: there).
	for _, want := range []string{
		"build-args: |-",
		"            B19_UBUNTU_SERIES=${{ matrix.B19_UBUNTU_SERIES }}",
		"            M6E_NAMESPACE=b19",
		"            M6E_PROJECT=ubuntu",
		"            M6E_VERSION=${{ github.ref_name }}",
		"            SOURCE_DOCKER_REGISTRY=${{ vars.SOURCE_DOCKER_REGISTRY }}",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("build-args block missing %q:\n%s", want, s)
		}
	}
	// The value ALSO stays on the job env: map (kept for non-composite steps + ${{ env }} refs).
	if !strings.Contains(s, "SOURCE_DOCKER_REGISTRY: ${{ vars.SOURCE_DOCKER_REGISTRY }}") {
		t.Errorf("var build-arg value should ALSO ride the job env: map:\n%s", s)
	}
	// get: identity args derive a generation-time LITERAL (b19/ubuntu → b19 / ubuntu),
	// carried as a job env value — NOT a ${{ vars }} ref, NOT a second pf-cli call.
	for _, want := range []string{"M6E_NAMESPACE: b19", "M6E_PROJECT: ubuntu"} {
		if !strings.Contains(s, want) {
			t.Errorf("identity build-arg %q missing from job env (basename split):\n%s", want, s)
		}
	}
	// ci: the abstract version key lowers to the target's git-ref expression, in env.
	if !strings.Contains(s, "M6E_VERSION: ${{ github.ref_name }}") {
		t.Errorf("ci version build-arg missing its git-ref expr in env:\n%s", s)
	}
	// The matrix axis is the matrix binding (never a vars ref) — as a build-arg VALUE in
	// the block AND on the job env: map.
	if strings.Contains(s, "B19_UBUNTU_SERIES: ${{ vars.") || strings.Contains(s, "B19_UBUNTU_SERIES=${{ vars.") {
		t.Errorf("matrix axis must be a matrix binding, not a vars ref:\n%s", s)
	}
	if !strings.Contains(s, "B19_UBUNTU_SERIES: ${{ matrix.B19_UBUNTU_SERIES }}") {
		t.Errorf("matrix axis lost its job env: binding:\n%s", s)
	}
	// file: a per-cell deps file lowers to a declarative file-args scalar (axis placeholder
	// kept as the matrix ref); the action reads it — NO inline grep, NO $GITHUB_ENV.
	if !strings.Contains(s, `file-args: B19_UBUNTU_HASH=.container/foundation/deps/ubuntu/${{ matrix.B19_UBUNTU_SERIES }}.sha256.deps`) {
		t.Errorf("file build-arg should lower to a declarative file-args scalar:\n%s", s)
	}
	for _, gone := range []string{"$GITHUB_ENV", "grep -v", "${{ env.B19_UBUNTU_HASH }}"} {
		if strings.Contains(s, gone) {
			t.Errorf("file arg must be read by the action, not inline (%q leaked):\n%s", gone, s)
		}
	}
	// The abstract mount lowers to a clean scalar — no block, no concrete --build-context.
	if !strings.Contains(s, "mounts: fetch=/fetch") {
		t.Errorf("abstract mount should lower to a clean name=mountpoint scalar:\n%s", s)
	}
	for _, gone := range []string{"mounts: |", "--build-context"} {
		if strings.Contains(s, gone) {
			t.Errorf("%q must not appear (declarative, flag stays in action):\n%s", gone, s)
		}
	}
}

// TestBuildArgsAutoInjected proves that org.projectfile.build.args entries are
// automatically forwarded as build-args to container-build — no per-arg
// redeclaration in ci.tools.container-build.args needed. It pins the four lowering
// shapes the make reader also emits: a flat FROM ref (empty default → bare
// ${{ vars.NAME }}, forward only when set), a literal default (→ ${{ vars.NAME ||
// 'default' }}), a file: input (→ a per-cell file-arg), and an axis-named input
// (skipped — the cell passes it). Explicit manifest args take priority over the
// auto-derived one of the same name (override wins).
func TestBuildArgsAutoInjected(t *testing.T) {
	const subtree = `{
  "image": "b19/ubuntu",
  "matrix": {"axes": {"B19_UBUNTU_SERIES": ["resolute", "noble"]}},
  "tools": {
    "container-build": {"action": "container-build", "args": {
      "M6E_NAMESPACE": "b19",
      "B19_DASEL_IMAGE": {"var": "OVERRIDE_IMAGE"}
    }}
  },
  "nodes": {
    "image-built": {"matrix": true, "needs": {"container-build": true}},
    "ready-to-publish": {"goal": true, "needs": {"image-built": true}}
  }
}`
	build := &ci.Build{
		Args: []ci.BuildInput{
			{Name: "B19_FD_IMAGE"},                                              // flat FROM ref, empty default
			{Name: testUbuntuVersion, Default: testResolute},                    // literal default
			{Name: testUbuntuHash, File: ".container/{B19_UBUNTU_SERIES}/hash"}, // per-cell file
			{Name: testUbuntuSeries},                                            // axis name → skipped
			{Name: "B19_DASEL_IMAGE"},                                           // explicit arg wins
		},
	}
	st, err := ci.Parse([]byte(subtree))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rm, err := resolve.Resolve(st)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	out, err := Workflow(Build(rm, st, build), Targets[TargetGHA], ci.Platform{})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{
		"B19_FD_IMAGE=${{ vars.B19_FD_IMAGE }}",                                      // empty default → bare var
		"B19_UBUNTU_VERSION=${{ vars.B19_UBUNTU_VERSION || 'resolute' }}",            // literal default fallback
		"file-args: B19_UBUNTU_HASH=.container/${{ matrix.B19_UBUNTU_SERIES }}/hash", // per-cell file-arg
	} {
		if !strings.Contains(s, want) {
			t.Errorf("auto-injected build-arg missing %q:\n%s", want, s)
		}
	}
	// An axis-named arg is NOT forwarded as a build-arg value (the cell passes it).
	if strings.Contains(s, "B19_UBUNTU_SERIES=${{ vars.B19_UBUNTU_SERIES") {
		t.Errorf("axis-named build.args input must be skipped, not var-forwarded:\n%s", s)
	}
	// Explicit manifest arg overrides the auto-derived var: B19_DASEL_IMAGE uses OVERRIDE_IMAGE.
	if !strings.Contains(s, "B19_DASEL_IMAGE=${{ vars.OVERRIDE_IMAGE }}") {
		t.Errorf("explicit arg should override auto-injected: want B19_DASEL_IMAGE=${{ vars.OVERRIDE_IMAGE }}:\n%s", s)
	}
	if strings.Contains(s, "B19_DASEL_IMAGE=${{ vars.B19_DASEL_IMAGE }}") {
		t.Errorf("auto-injected B19_DASEL_IMAGE must not appear when explicit arg overrides it:\n%s", s)
	}
	// With nil Build, none of the auto-injected args appear.
	outNil, err := Workflow(Build(rm, st, nil), Targets[TargetGHA], ci.Platform{})
	if err != nil {
		t.Fatal(err)
	}
	sNil := string(outNil)
	for _, absent := range []string{"B19_FD_IMAGE", testUbuntuVersion, testUbuntuHash} {
		if strings.Contains(sNil, absent) {
			t.Errorf("nil Build should not inject %q:\n%s", absent, sNil)
		}
	}
}

// TestBuildArgMakeExprDefault pins the THIRD build-arg lowering shape: a default that
// is itself a make-plane expression (an image FROM ref ${REGISTRY}/path:${TAG}). Such a
// default cannot ride the ${{ vars.NAME || 'default' }} form — the forge plane never
// expands ${...}, so the verbatim default would reach the build backend as an INVALID
// reference (buildah: "parsing reference …: invalid reference format"). Each ${VAR} is
// lowered to a juxtaposed ${{ vars.VAR }} fragment; the ImageTagVar ref keeps its
// 'latest' fallback so an unset tag var does not yield an empty-tag ref. Mirrors the
// real b19/ubuntu FROM args (B19_DASEL_IMAGE, B19_FD_IMAGE, B19_MINIJINJA_IMAGE).
func TestBuildArgMakeExprDefault(t *testing.T) {
	const subtree = `{
  "image": "b19/ubuntu",
  "tools": {
    "container-build": {"action": "container-build"}
  },
  "nodes": {
    "image-built": {"needs": {"container-build": true}},
    "ready": {"goal": true, "needs": {"image-built": true}}
  }
}`
	build := &ci.Build{
		Args: []ci.BuildInput{
			{Name: "B19_DASEL_IMAGE", Default: "${B19_DOCKER_REGISTRY}/b19/dasel:${M6E_BASE_IMAGE_DEFAULT_VERSION}"},
			{Name: testUbuntuVersion, Default: testResolute}, // literal default — unaffected path
			// A composed base-image ref that EMBEDS a var which itself carries a literal
			// build-arg default (the real b19/go shape). The embedded ${B19_UBUNTU_SERIES}
			// must keep that default as its forge fallback, else an unset repo var collapses
			// it to an empty segment (reg/b19/ubuntu/:latest — buildah: invalid reference).
			{Name: testUbuntuBaseImage, Default: "${B19_DOCKER_REGISTRY}/b19/ubuntu/${B19_UBUNTU_SERIES}:${M6E_BASE_IMAGE_DEFAULT_VERSION}"},
			{Name: testUbuntuSeries, Default: testResolute},
		},
	}
	st, err := ci.Parse([]byte(subtree))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rm, err := resolve.Resolve(st)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	out, err := Workflow(Build(rm, st, build), Targets[TargetGHA], ci.Platform{})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	// The make-expr default is lowered to a juxtaposed forge ref; the tag var keeps
	// its 'latest' fallback.
	want := "B19_DASEL_IMAGE=${{ vars.B19_DOCKER_REGISTRY }}/b19/dasel:${{ env.M6E_TAG }}"
	if !strings.Contains(s, want) {
		t.Errorf("make-expr build-arg not lowered:\nwant %q\ngot:\n%s", want, s)
	}
	// A literal default keeps the ${{ vars.NAME || 'default' }} form (regression guard).
	if !strings.Contains(s, "B19_UBUNTU_VERSION=${{ vars.B19_UBUNTU_VERSION || 'resolute' }}") {
		t.Errorf("literal default form regressed:\n%s", s)
	}
	// An embedded var ref that has its OWN build-arg default keeps that default as its
	// forge fallback — the b19/go base-image bug (a bare ${{ vars.B19_UBUNTU_SERIES }}
	// rendered an empty series segment on a forge where the repo var was never set).
	wantBase := "B19_UBUNTU_BASE_IMAGE=${{ vars.B19_DOCKER_REGISTRY }}/b19/ubuntu/${{ vars.B19_UBUNTU_SERIES || 'resolute' }}:${{ env.M6E_TAG }}"
	if !strings.Contains(s, wantBase) {
		t.Errorf("embedded defaulted var lost its fallback:\nwant %q\ngot:\n%s", wantBase, s)
	}
	// The empty-segment ref (the actual failure) must never reach the workflow.
	if strings.Contains(s, "/b19/ubuntu/:") || strings.Contains(s, "/b19/ubuntu/${{ vars.B19_UBUNTU_SERIES }}:") {
		t.Errorf("series segment collapsed / lost its fallback:\n%s", s)
	}
	// The raw make syntax must NOT reach the workflow (the bug: buildah saw
	// ${B19_DOCKER_REGISTRY}/b19/dasel:${M6E_BASE_IMAGE_DEFAULT_VERSION} verbatim), nor
	// the old broken ${{ vars.NAME || '<make-syntax>' }} wrapping.
	for _, bad := range []string{
		"${B19_DOCKER_REGISTRY}",
		"${M6E_BASE_IMAGE_DEFAULT_VERSION}",
		"B19_DASEL_IMAGE=${{ vars.B19_DASEL_IMAGE || '",
	} {
		if strings.Contains(s, bad) {
			t.Errorf("raw make syntax / broken wrapping leaked into output (%q):\n%s", bad, s)
		}
	}
}

// TestSinkComposedImageRefs pins the forge contract of the pull-sink composition:
// a declared foreign image reaches the workflow as ONE expression — the per-image
// full-ref override, then format() with the vars.SOURCE_DOCKER_REGISTRY redirect
// (the sink's literal head as fallback) and the entry's parts as args — never a
// bare vars.<NAME> an operator must set, and never raw make syntax. The same
// Build also pins the repository-scoped tool form and the pf-cli full-ref form.
func TestSinkComposedImageRefs(t *testing.T) {
	const subtree = `{
  "image": "b19/go",
  "tools": {
    "container-build": {"action": "container-build"},
    "js-lint": {"image": "D9T_JS_TOOLS_IMAGE", "run": "make lint"}
  },
  "nodes": {
    "image-built": {"needs": {"container-build": true}},
    "linted": {"needs": {"js-lint": true}},
    "ready": {"goal": true, "needs": {"image-built": true, "linted": true}}
  }
}`
	build := &ci.Build{
		Images: map[string]string{
			testUbuntuBaseImage: "kiota.ch/b19/ubuntu/${B19_UBUNTU_SERIES}:${M6E_BASE_IMAGE_DEFAULT_VERSION}",
			testJsToolsImage:    "kiota.ch/d9t/js-tools:${M6E_BASE_IMAGE_DEFAULT_VERSION}",
			PfCliImageVar:       "kiota.ch/projectfile/cli:${M6E_BASE_IMAGE_DEFAULT_VERSION}",
		},
		ImageHeads: map[string]string{
			testUbuntuBaseImage: testKiotaHead,
			testJsToolsImage:    testKiotaHead,
			PfCliImageVar:       testKiotaHead,
		},
		Args: []ci.BuildInput{
			{Name: testUbuntuBaseImage}, // empty default — the entry, not the default, composes
			{Name: testUbuntuSeries, Default: testResolute},
		},
	}
	st, err := ci.Parse([]byte(subtree))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rm, err := resolve.Resolve(st)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	out, err := Workflow(Build(rm, st, build), Targets[TargetGHA], ci.Platform{})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	// The build-arg FROM ref: full-ref override + redirect + series default + tag hoist.
	wantBase := "B19_UBUNTU_BASE_IMAGE=${{ vars.B19_UBUNTU_BASE_IMAGE || format('{0}/b19/ubuntu/{1}:{2}', vars.SOURCE_DOCKER_REGISTRY || 'kiota.ch', vars.B19_UBUNTU_SERIES || 'resolute', env.M6E_TAG) }}"
	if !strings.Contains(s, wantBase) {
		t.Errorf("sink-composed build-arg wrong:\nwant %q\ngot:\n%s", wantBase, s)
	}
	// The same ref promoted to the JOB env block must carry the tag arg INLINED: the
	// env context does not exist while an env block evaluates ("Unknown Variable
	// Access env"), so inlineHoists expands the bare env.M6E_TAG token inside the
	// larger expression — the Forgejo schema failure of the first regeneration.
	wantJobEnv := "B19_UBUNTU_BASE_IMAGE: ${{ vars.B19_UBUNTU_BASE_IMAGE || format('{0}/b19/ubuntu/{1}:{2}', vars.SOURCE_DOCKER_REGISTRY || 'kiota.ch', vars.B19_UBUNTU_SERIES || 'resolute', vars.BASE_IMAGE_DEFAULT_VERSION || 'latest') }}"
	if !strings.Contains(s, wantJobEnv) {
		t.Errorf("sink-composed job-env value not hoist-expanded:\nwant %q\ngot:\n%s", wantJobEnv, s)
	}
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "B19_UBUNTU_BASE_IMAGE:") && strings.Contains(line, "env.M6E_TAG") {
			t.Errorf("bare env.M6E_TAG survived inside the env-block form:\n%s", line)
		}
	}
	// The tool image: repository-scoped (run-tool appends the tag), no args needed.
	wantTool := "image: ${{ vars.D9T_JS_TOOLS_IMAGE || format('{0}/d9t/js-tools', vars.SOURCE_DOCKER_REGISTRY || 'kiota.ch') }}"
	if !strings.Contains(s, wantTool) {
		t.Errorf("sink-composed tool image wrong:\nwant %q\ngot:\n%s", wantTool, s)
	}
	// pf-cli: the FULL ref inside the one expression (the entry's tag part lowers in).
	wantCli := "pf-cli-image: ${{ vars.PF_CLI_IMAGE || format('{0}/projectfile/cli:{1}', vars.SOURCE_DOCKER_REGISTRY || 'kiota.ch', env.M6E_TAG) }}"
	if !strings.Contains(s, wantCli) {
		t.Errorf("sink-composed pf-cli ref wrong:\nwant %q\ngot:\n%s", wantCli, s)
	}
	// An images-declared arg is NOT a dispatch input (a full ref cannot round-trip
	// as one); the scalar series arg stays.
	if strings.Contains(s, "B19_UBUNTU_BASE_IMAGE:\n") {
		t.Errorf("images-declared arg exposed as dispatch input:\n%s", s)
	}
	if !strings.Contains(s, "B19_UBUNTU_SERIES:") {
		t.Errorf("scalar series arg lost its dispatch input:\n%s", s)
	}
	// The raw make syntax must never reach the workflow.
	for _, bad := range []string{"${B19_UBUNTU_SERIES}", "${M6E_BASE_IMAGE_DEFAULT_VERSION}", "=kiota.ch/"} {
		if strings.Contains(s, bad) {
			t.Errorf("raw make syntax leaked into output (%q):\n%s", bad, s)
		}
	}
}

// TestImageScanConsumesBuildTar pins the DERIVED build→scan hand-off (general-plan
// task 1): image-scan is a PORTABLE tool (image+run), NOT a provider. Because it
// `needs` the container-build provider, the render auto-wires the OCI-tar hand-off
// from that edge alone — no manifest field: a cell-keyed download-artifact step
// plus a `M6E_IMAGE_ARCHIVE=<Stem>.tar` env the d9t entrypoint reads daemonlessly.
// The cell key matches the producer's (same axes), so each scan cell pulls its own
// tar — the per-cell correctness task 7 leans on.
func TestImageScanConsumesBuildTar(t *testing.T) {
	st, err := ci.Parse([]byte(providerSubtree))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rm, err := resolve.Resolve(st)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	m := Build(rm, st, nil)
	step := steps(m)
	// The portable consumer STEP carries the archive env; its node-job pulls the tar.
	scan := step["grype-scan-image"]
	if scan.Action != "" {
		t.Fatalf("grype-scan-image must be a PLAIN tool (no action), got %q", scan.Action)
	}
	scanJob := jobOf(m, "grype-scan-image")
	if len(scanJob.Downloads) != 1 || scanJob.Downloads[0].Name != testImageMatrixStem {
		t.Errorf("scan node should download its cell-keyed build artifact, got %+v", scanJob.Downloads)
	}
	var archive string
	for _, e := range scan.Env {
		if e.Key == ImageArchiveEnv {
			archive = e.Value
		}
	}
	if archive != testImageMatrixStem+".tar" {
		t.Errorf("scan step should bind M6E_IMAGE_ARCHIVE to its cell tar, got %q", archive)
	}
	// The PRODUCER must NOT consume itself — its node downloads nothing, no archive env.
	buildJob := jobOf(m, testContainerBuild)
	if len(buildJob.Downloads) != 0 {
		t.Errorf("the build producer node must not download an artifact, got %+v", buildJob.Downloads)
	}
	for _, e := range step[testContainerBuild].Env {
		if e.Key == ImageArchiveEnv {
			t.Errorf("the build producer must not get M6E_IMAGE_ARCHIVE")
		}
	}

	// In the rendered workflow the download lands AFTER checkout and BEFORE the run.
	out, err := Workflow(m, Targets[TargetGHA], ci.Platform{})
	if err != nil {
		t.Fatal(err)
	}
	want := "    steps:\n" +
		"      - uses: actions/checkout@v7\n" +
		"        with:\n" +
		"          submodules: true\n" +
		"      - uses: actions/download-artifact@v8\n" +
		"        with:\n" +
		"          name: " + testImageMatrixStem + "\n" +
		"      - name: grype-scan-image\n" +
		"        uses: projectfile/ci-actions/run-tool@v1\n" +
		"        with:\n" +
		"          image: reg.example/go-tools\n" +
		"          version: latest\n" +
		"          run: auto-grype\n" +
		"          env: |-\n" +
		"            B19_UBUNTU_SERIES=${{ matrix.B19_UBUNTU_SERIES }}\n" +
		"            M6E_IMAGE_ARCHIVE=" + testImageMatrixStem + ".tar"
	if !strings.Contains(string(out), want) {
		t.Errorf("scan job missing the ordered download→run-tool body:\n%s", out)
	}
	// Exactly one download-artifact step (the lone consumer) — the producer and the
	// SOURCE/JOIN jobs never pull an artifact.
	if n := strings.Count(string(out), "download-artifact"); n != 1 {
		t.Errorf("want exactly 1 download-artifact step (the scan consumer), got %d", n)
	}
}

// auditSubtree is the `audited` published-image re-scan: a registry-pull scanner
// (grype-scan-registry, `env: [M6E_IMAGE_PUBLISHED]`) on a matrix image-is-audited node
// whose goal (audited) needs it. Unlike TestImageScanConsumesBuildTar the scan node has NO
// build edge — an audit does not rebuild — so it must NOT get a tar; it PULLS the published
// image by ref instead. The image basename carries an {AXIS} token so the per-cell ref is
// provable.
const auditSubtree = `{
  "image": "b19/ubuntu-{B19_UBUNTU_SERIES}",
  "matrix": {"axes": {"B19_UBUNTU_SERIES": ["resolute", "noble"]}},
  "tools": {
    "grype-scan-registry": {"image": "reg.example/go-tools:latest", "run": "auto-grype image", "env": ["M6E_IMAGE_PUBLISHED"]}
  },
  "nodes": {
    "image-is-audited": {"matrix": true, "needs": {"grype-scan-registry": true}},
    "audited": {"goal": true, "needs": {"image-is-audited": true}}
  }
}`

// TestAuditScanPullsPublishedImage pins the audit re-scan hand-off: a scanner requesting the
// M6E_IMAGE_PUBLISHED contract var gets the resolver-COMPUTED per-cell published ref
// (<OUTPUT_REGISTRY>/<basename>:latest) — the forge sibling of the make plane's
// M6E_IMAGE_PUBLISHED — and NOT a build tar (no rebuild in an audit). The contract var must
// never survive as a forwarded (undefined) vars-store lookup.
func TestAuditScanPullsPublishedImage(t *testing.T) {
	st, err := ci.Parse([]byte(auditSubtree))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rm, err := resolve.Resolve(st)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	m := Build(rm, st, nil)
	scan := steps(m)["grype-scan-registry"]

	// The published ref is COMPUTED per cell: <OUTPUT_REGISTRY>/<per-cell basename>:latest.
	want := "${{ vars.OUTPUT_REGISTRY }}/b19/ubuntu-${{ matrix.B19_UBUNTU_SERIES }}:latest"
	var got string
	for _, e := range scan.Env {
		switch e.Key {
		case ImagePublishedEnv:
			got = e.Value
		case ImageArchiveEnv:
			t.Errorf("audit scan must NOT get %s (no build in scope)", ImageArchiveEnv)
		}
	}
	if got != want {
		t.Errorf("audit scan step must inject %s=%q (published ref), got %q", ImagePublishedEnv, want, got)
	}
	// The contract var must NOT survive as a forwarded (undefined) vars-store lookup.
	for _, n := range scan.EnvNames {
		if n == ImagePublishedEnv {
			t.Errorf("%s must be resolver-computed, not a forwarded credential name", ImagePublishedEnv)
		}
	}
	// The scan node has no build producer — it downloads nothing (it pulls by ref).
	if dl := jobOf(m, "grype-scan-registry").Downloads; len(dl) != 0 {
		t.Errorf("audit scan must not download a build artifact, got %+v", dl)
	}

	// Rendered: the run-tool env block carries the computed ref after the axis binding.
	out, err := Workflow(m, Targets[TargetGHA], ci.Platform{})
	if err != nil {
		t.Fatal(err)
	}
	wantBlock := "          env: |-\n" +
		"            B19_UBUNTU_SERIES=${{ matrix.B19_UBUNTU_SERIES }}\n" +
		"            M6E_IMAGE_PUBLISHED=${{ vars.OUTPUT_REGISTRY }}/b19/ubuntu-${{ matrix.B19_UBUNTU_SERIES }}:latest"
	if !strings.Contains(string(out), wantBlock) {
		t.Errorf("audit scan job missing the computed published-ref env block:\n%s", out)
	}
}

// TestAuditScanTargetsThePullSink pins the audit target against the DECLARED read
// destination rather than a composed prefix. The fixture reads from Docker Hub, whose
// path is FLAT (`namespace/name`) — a shape `<OUTPUT_REGISTRY>/<nested basename>` cannot
// produce at all, so a passing assertion proves the sink's own grammar reached the scan
// and not merely that some ref did. Both spellings of the ref (the job `env:` block and
// the run-tool `env:` input) must be the ONE value, and the axis must still be per cell.
func TestAuditScanTargetsThePullSink(t *testing.T) {
	st, err := ci.Parse([]byte(auditSubtree))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rm, err := resolve.Resolve(st)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	// Only the GHA lowering declares a route, so the same model exercises both paths.
	m := Build(rm, st, &ci.Build{PullRefs: map[string]ci.SinkRef{
		ci.LoweringGHA: {Sink: "hub", Ref: "docker.io/damianbuho/b19-ubuntu-{B19_UBUNTU_SERIES}:latest"},
	}})
	gha, err := Workflow(m, Targets[TargetGHA], ci.Platform{})
	if err != nil {
		t.Fatalf("gha: %v", err)
	}
	want := "docker.io/damianbuho/b19-ubuntu-${{ matrix.B19_UBUNTU_SERIES }}:latest"
	for _, block := range []string{
		"      " + ImagePublishedEnv + ": " + want,      // job env
		"            " + ImagePublishedEnv + "=" + want, // the run-tool env input
	} {
		if !strings.Contains(string(gha), block) {
			t.Errorf("audit scan missing %q\n---\n%s", block, gha)
		}
	}
	if strings.Contains(string(gha), "vars.OUTPUT_REGISTRY }}/b19/ubuntu") {
		t.Errorf("a declared pull sink must REPLACE the prefix composition\n---\n%s", gha)
	}
	// Rendered SECOND from the SAME model: forgejo declares no pull, so it must keep the
	// OUTPUT_REGISTRY fallback rather than inherit the destination the GHA pass bound —
	// the StepView is shared across targets, which is where such a leak would live.
	forgejo, err := Workflow(m, Targets[TargetForgejo], ci.Platform{})
	if err != nil {
		t.Fatalf("forgejo: %v", err)
	}
	if strings.Contains(string(forgejo), "docker.io/damianbuho") {
		t.Errorf("forgejo declares no pull and must not inherit the GHA destination\n---\n%s", forgejo)
	}
	fallback := "${{ vars.OUTPUT_REGISTRY }}/b19/ubuntu-${{ matrix.B19_UBUNTU_SERIES }}:latest"
	if !strings.Contains(string(forgejo), ImagePublishedEnv+": "+fallback) {
		t.Errorf("forgejo must keep the prefix fallback\n---\n%s", forgejo)
	}
}

// artifactSubtree is the binary-build → forge-release split: a plain `run:` producer
// (build-binaries, manifest `artifact: dist`) in a matrix cell, and a release consumer
// (gh-release) whose node needs the producer's node — so the build-artifact edge is
// DERIVED exactly like the image-scan tar edge, but for a non-action tool. Single-value
// axes keep the cell-keyed name deterministic.
const artifactSubtree = `{
  "matrix": {"axes": {"GOOS": ["linux"], "GOARCH": ["amd64"]}},
  "tools": {
    "build-binaries": {"run": "go build -o dist/pf .", "artifact": "dist"},
    "gh-release": {"run": "gh release create"}
  },
  "nodes": {
    "binaries-built": {"matrix": true, "needs": {"build-binaries": true}},
    "binaries-released": {"matrix": true, "needs": {"binaries-built": true, "gh-release": true}},
    "published": {"goal": true, "needs": {"binaries-released": true}}
  }
}`

// TestBuildArtifactHandoff pins the generic build-artifact edge: the producer uploads
// its `artifact:` path under a cell-keyed name (tool + axes), and the consumer that
// reaches it through the DAG downloads the SAME name back to that path — no consuming-
// side field, no image archive env (this is a binary, not an OCI tar). It is the plain-
// `run:` analogue of TestImageScanConsumesBuildTar.
func TestBuildArtifactHandoff(t *testing.T) {
	st, err := ci.Parse([]byte(artifactSubtree))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rm, err := resolve.Resolve(st)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	m := Build(rm, st, nil)
	step := steps(m)
	const name = "build-binaries-${{ matrix.GOARCH }}-${{ matrix.GOOS }}" + artifactScopeSuffix
	// Producer STEP: uploads its declared path under the cell-keyed name; its node pulls nothing.
	prod := step["build-binaries"]
	if prod.Upload != name {
		t.Errorf("producer Upload = %q, want %q", prod.Upload, name)
	}
	if prod.UploadPath != testDist {
		t.Errorf("producer UploadPath = %q, want %q", prod.UploadPath, testDist)
	}
	if len(jobOf(m, "build-binaries").Downloads) != 0 {
		t.Errorf("producer node must not download, got %+v", jobOf(m, "build-binaries").Downloads)
	}
	// Consumer node: downloads the SAME cell-keyed name to the producer's path; no archive env.
	consJob := jobOf(m, "gh-release")
	if len(consJob.Downloads) != 1 || consJob.Downloads[0].Name != name {
		t.Errorf("consumer node Downloads = %+v, want one named %q", consJob.Downloads, name)
	}
	if len(consJob.Downloads) == 1 && consJob.Downloads[0].Path != testDist {
		t.Errorf("consumer DownloadPath = %q, want %q", consJob.Downloads[0].Path, testDist)
	}
	if step["gh-release"].Upload != "" {
		t.Errorf("consumer must not upload, got %q", step["gh-release"].Upload)
	}
	for _, e := range step["gh-release"].Env {
		if e.Key == ImageArchiveEnv {
			t.Errorf("a binary consumer must not get the image archive env")
		}
	}
	// Rendered: producer's upload lands after its run; consumer's download (with path)
	// lands after checkout and before its run.
	out, err := Workflow(m, Targets[TargetGHA], ci.Platform{})
	if err != nil {
		t.Fatal(err)
	}
	wantUp := "      - name: build-binaries\n" +
		"        run: go build -o dist/pf .\n" +
		"      - uses: actions/upload-artifact@v7\n" +
		"        with:\n" +
		"          name: " + name + "\n" +
		"          path: dist\n" +
		// overwrite: the run_id-scoped name re-uploads under the same name on a re-run,
		// which upload-artifact@v4+ (gha) refuses without this.
		"          overwrite: true"
	if !strings.Contains(string(out), wantUp) {
		t.Errorf("producer missing the ordered run→upload body:\n%s", out)
	}
	// Forgejo's v1 store is last-write-wins and has no overwrite input, so it must NOT emit it.
	fout, err := Workflow(m, Targets[TargetForgejo], ci.Platform{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(fout), "overwrite:") {
		t.Errorf("forgejo upload must not carry overwrite (unsupported by @v3):\n%s", fout)
	}
	wantDown := "      - uses: actions/download-artifact@v8\n" +
		"        with:\n" +
		"          name: " + name + "\n" +
		"          path: dist\n" +
		"      - name: gh-release\n" +
		"        run: gh release create"
	if !strings.Contains(string(out), wantDown) {
		t.Errorf("consumer missing the ordered download→run body:\n%s", out)
	}
}

// releaseSubtree is a GOOS x GOARCH binary build whose publish leaf is the
// forgejo-release provider — the shape every fleet Go project has, reduced to the
// nodes the release plane acts on.
const releaseSubtree = `{
  "matrix": {"axes": {"GOOS": ["linux"], "GOARCH": ["amd64"]}},
  "tools": {
    "build-binaries": {"run": "go build -o dist/pf .", "artifact": "dist"},
    "forgejo-release": {"action": "forgejo-release", "env": ["FORGEJO_TOKEN"]}
  },
  "nodes": {
    "binaries-built": {"matrix": true, "needs": {"build-binaries": true}},
    "binaries-released": {"matrix": true, "needs": {"binaries-built": true, "forgejo-release": true}},
    "published": {"goal": true, "needs": {"binaries-released": true}}
  }
}`

// testReleaseAsset is the unsuffixed binary path releaseSubtree builds; the action
// appends the cell axes to it.
const testReleaseAsset = "dist/pf"

// TestForgejoReleaseAction pins the providers/forgejo-release lowering: an action
// consumer of the binary-build hand-off renders as `uses:` (not a bare `run:`),
// carrying the git tag (version) + the resolved binary path (release-asset-path)
// as `with:` inputs. The build→consumer download edge still restores dist/ before
// the action runs (the hand-off is driven by the needs edge + the producer's
// artifact:, not by the consumer's action/run shape).
func TestForgejoReleaseAction(t *testing.T) {
	st, err := ci.Parse([]byte(releaseSubtree))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	// Parse does not run Load's artifact-path resolution; set it manually to test the
	// render lowering in isolation (Load-side resolution is pinned by TestLoadResolvesReleaseAssetPath).
	man := st.Tools["forgejo-release"]
	man.ReleaseAssetPath = testReleaseAsset
	st.Tools["forgejo-release"] = man
	rm, err := resolve.Resolve(st)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	m := Build(rm, st, nil)
	step := steps(m)["forgejo-release"]
	if step.Action != "forgejo-release" {
		t.Errorf("Action = %q, want forgejo-release", step.Action)
	}
	if step.PublishVersion != "${{ github.ref_name }}" {
		t.Errorf("PublishVersion = %q, want ${{ github.ref_name }}", step.PublishVersion)
	}
	if step.ReleaseAssetPath != testReleaseAsset {
		t.Errorf("ReleaseAssetPath = %q, want dist/pf", step.ReleaseAssetPath)
	}
	// The consumer node still downloads the producer's dist/ artifact (the hand-off
	// edge is shape-agnostic about the consumer being an action).
	consJob := jobOf(m, "forgejo-release")
	if len(consJob.Downloads) != 1 || consJob.Downloads[0].Path != testDist {
		t.Errorf("consumer Downloads = %+v, want one restoring dist/", consJob.Downloads)
	}
	// Rendered: the action lowers to `uses:` with version + release-asset-path inputs.
	out, err := Workflow(m, Targets[TargetForgejo], ci.Platform{})
	if err != nil {
		t.Fatal(err)
	}
	// The version input is followed by a template comment explaining release-asset-path;
	// assert the key lines are present and ordered rather than an exact multi-line match.
	for _, line := range []string{
		"- name: forgejo-release",
		"uses: projectfile/ci-actions/forgejo-release@v1",
		"version: ${{ github.ref_name }}",
		"release-asset-path: dist/pf",
	} {
		if !strings.Contains(string(out), line) {
			t.Errorf("rendered forgejo-release missing %q:\n%s", line, out)
		}
	}
}

// reportsSubtree is a node with TWO containerised scanners that EMIT diagnostic reports
// (manifest `reports:`) but are CONSUMED by nothing — a gate node needs them. It pins
// that the reports of a whole job roll up into ONE upload (the entire `reports/` dir),
// distinct from the build-artifact edge: always()-guarded, missing-file tolerant, and
// NEVER a download edge (nothing downloads a report).
const reportsSubtree = `{
  "matrix": {"axes": {"S": ["x"]}},
  "tools": {
    "grype-scan-tar": {"image": "d9t/go-tools", "run": "auto-grype image", "reports": "reports/grype-image.*"},
    "trivy-scan-tar": {"image": "d9t/go-tools", "run": "auto-trivy image", "reports": "reports/trivy-image.*"}
  },
  "nodes": {
    "image-is-secure": {"matrix": true, "needs": {"grype-scan-tar": true, "trivy-scan-tar": true}},
    "published": {"goal": true, "needs": {"image-is-secure": true}}
  }
}`

// TestReportUploadAlways pins the per-JOB report rollup: the two scanners in one node
// produce a SINGLE upload of the whole `reports/` dir under a cell-keyed job name,
// guarded `if: always()` with missing files tolerated (a fail-closed scan still
// publishes), and NO per-tool upload nor a download edge — distinct from the binary
// build-artifact hand-off.
func TestReportUploadAlways(t *testing.T) {
	st, err := ci.Parse([]byte(reportsSubtree))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rm, err := resolve.Resolve(st)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	m := Build(rm, st, nil)
	const name = "reports-image-is-secure-${{ matrix.S }}" + artifactScopeSuffix
	var job *JobView
	for i := range m.Jobs {
		if m.Jobs[i].Name == "image-is-secure" {
			job = &m.Jobs[i]
		}
	}
	if job == nil {
		t.Fatal("image-is-secure job not built")
	}
	if job.ReportsUpload != name {
		t.Errorf("job ReportsUpload = %q, want %q", job.ReportsUpload, name)
	}
	if job.ReportsPath != "reports/" {
		t.Errorf("job ReportsPath = %q, want %q (the whole reports dir)", job.ReportsPath, "reports/")
	}
	// No member step carries its own upload (the rollup replaced per-tool uploads).
	for _, s := range job.Steps {
		if s.Upload != "" {
			t.Errorf("a scanner must not also be a build-artifact producer, got Upload=%q", s.Upload)
		}
	}
	// No node downloads the report (it has no consumer).
	for _, j := range m.Jobs {
		for _, d := range j.Downloads {
			if strings.HasPrefix(d.Name, "reports-") {
				t.Errorf("a report must never become a download edge, job %q got %+v", j.Name, d)
			}
		}
	}
	out, err := Workflow(m, Targets[TargetGHA], ci.Platform{})
	if err != nil {
		t.Fatal(err)
	}
	want := "      - uses: actions/upload-artifact@v7\n" +
		"        if: ${{ always() }}\n" +
		"        with:\n" +
		"          name: " + name + "\n" +
		"          path: reports/\n" +
		"          if-no-files-found: warn"
	if !strings.Contains(string(out), want) {
		t.Errorf("missing always()-guarded per-job report upload:\n%s", out)
	}
	// Exactly ONE reports upload for the two-scanner job — not one per tool.
	if n := strings.Count(string(out), "name: "+name); n != 1 {
		t.Errorf("want exactly 1 rolled-up reports upload, got %d", n)
	}
}

// cacheSubtree is a scanner tool declaring a NAME `mounts:` entry (its DB at a container
// path). The resolver lowers the cache two ways from this ONE neutral source (Phase 8 /
// Law 3): a self-hosted forge runner binds a persistent dir RO; an ephemeral GitHub runner
// restores an actions/cache there first, then mounts it — the model never names
// actions/cache. A NAME `from` (no `/`) is the cache discriminator.
const cacheSubtree = `{
  "tools": {
    "auto-grype": {"image": "d9t/go-tools", "mounts": [{"from": "grype-db", "to": "/home/ubuntu/.cache/grype/db"}]}
  },
  "nodes": {
    "source-is-secure": {"goal": true, "needs": {"auto-grype": true}}
  }
}`

// TestScannerCacheLowering pins the `caches:` lowering. The SAME neutral declaration
// forks by runner kind: forgejo (self-hosted) binds a persistent dir RO via the run-tool
// `mounts` input and emits NO restore step; gha (ephemeral) emits an actions/cache/restore
// step before the run-tool step and mounts the restored dir RO. The step also carries the
// neutral name→path pair so a non-GHA/Forgejo engine reads the same model.
func TestScannerCacheLowering(t *testing.T) {
	st, err := ci.Parse([]byte(cacheSubtree))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rm, err := resolve.Resolve(st)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	m := Build(rm, st, nil)
	// Neutral model: the step carries the cache name → container path, untouched by target.
	scan := steps(m)["auto-grype"]
	if len(scan.Caches) != 1 || scan.Caches[0].Name != "grype-db" ||
		scan.Caches[0].Path != "/home/ubuntu/.cache/grype/db" {
		t.Fatalf("step Caches = %+v, want one {grype-db, /home/ubuntu/.cache/grype/db}", scan.Caches)
	}

	// FORGEJO: a persistent RO bind, no restore action (the refresh pipeline fills the dir).
	fj, err := Workflow(m, Targets[TargetForgejo], ci.Platform{})
	if err != nil {
		t.Fatal(err)
	}
	// A single RO bind of the refresh-owned shared DB (the scan default mode).
	if s := string(fj); !strings.Contains(s, "mounts: /app/data/ci-cache/grype-db:/home/ubuntu/.cache/grype/db:ro") {
		t.Errorf("forgejo: missing the RO cache mount:\n%s", s)
	}
	if strings.Contains(string(fj), "actions/cache") {
		t.Errorf("forgejo (self-hosted) must NOT emit an actions/cache step:\n%s", fj)
	}

	// GHA: a restore step BEFORE the run-tool step, then a mount of the restored dir.
	gha, err := Workflow(m, Targets[TargetGHA], ci.Platform{})
	if err != nil {
		t.Fatal(err)
	}
	want := "      - name: restore grype-db\n" +
		"        uses: actions/cache/restore@v4\n" +
		"        with:\n" +
		"          path: ${{ runner.temp }}/ci-cache/grype-db\n" +
		"          key: ci-cache-grype-db-\n" +
		"          restore-keys: |\n" +
		"            ci-cache-grype-db-\n" +
		"      - name: auto-grype\n" +
		"        uses: projectfile/ci-actions/run-tool@v1\n"
	if s := string(gha); !strings.Contains(s, want) {
		t.Errorf("gha: missing the ordered restore→run-tool body:\n%s", s)
	}
	// A single RO bind of the restored shared DB (the scan default mode).
	if !strings.Contains(string(gha), "mounts: ${{ runner.temp }}/ci-cache/grype-db:/home/ubuntu/.cache/grype/db:ro") {
		t.Errorf("gha: missing the restored RO cache mount:\n%s", gha)
	}
}

// writerCacheSubtree is a `*-db-update` writer: a run-tool whose named cache mounts
// read-write (it REFRESHES the shared scanner DB in place).
const writerCacheSubtree = `{
  "tools": {
    "grype-db-update": {"image": "d9t/go-tools", "run": "grype db update", "mounts": [{"from": "grype-db", "to": "/app/.cache/grype", "mode": "read-write"}]}
  },
  "nodes": {
    "scanner-db-refreshed": {"goal": true, "needs": {"grype-db-update": true}}
  }
}`

// TestScannerCacheWriterSave pins the SAVE half of the ephemeral lowering: a read-write
// (writer) mount emits an actions/cache/save step AFTER the run-tool step so the refreshed
// DB is persisted under a unique rolled key; a self-hosted runner writes the persistent dir
// in place and emits NO cache action. This is the counterpart to TestScannerCacheLowering's
// read-only (scan) case, which saves nothing.
func TestScannerCacheWriterSave(t *testing.T) {
	st, err := ci.Parse([]byte(writerCacheSubtree))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rm, err := resolve.Resolve(st)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	m := Build(rm, st, nil)

	// GHA: restore BEFORE, run-tool with the RW mount, then a save AFTER (unique rolled key).
	gha, err := Workflow(m, Targets[TargetGHA], ci.Platform{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(gha), "mounts: ${{ runner.temp }}/ci-cache/grype-db:/app/.cache/grype:rw") {
		t.Errorf("gha: missing the RW writer mount:\n%s", gha)
	}
	wantSave := "      - name: save grype-db\n" +
		"        uses: actions/cache/save@v4\n" +
		"        with:\n" +
		"          path: ${{ runner.temp }}/ci-cache/grype-db\n" +
		"          key: ci-cache-grype-db-${{ github.run_id }}\n"
	if s := string(gha); !strings.Contains(s, wantSave) {
		t.Errorf("gha: missing the actions/cache/save step for the rw writer:\n%s", s)
	}
	// The save must come AFTER the tool ran, never before it.
	if idxTool, idxSave := strings.Index(string(gha), "grype db update"), strings.Index(string(gha), "cache/save"); idxSave < idxTool {
		t.Errorf("gha: save step ordered before the run-tool step (save=%d tool=%d)", idxSave, idxTool)
	}

	// FORGEJO (self-hosted): the writer binds the persistent dir RW; NO cache action either way.
	fj, err := Workflow(m, Targets[TargetForgejo], ci.Platform{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(fj), "mounts: /app/data/ci-cache/grype-db:/app/.cache/grype:rw") {
		t.Errorf("forgejo: missing the RW persistent bind:\n%s", fj)
	}
	if strings.Contains(string(fj), "actions/cache") {
		t.Errorf("forgejo (self-hosted) must NOT emit an actions/cache step:\n%s", fj)
	}
}

// pathMountSubtree is a run-tool with TWO `mounts` entries: a NAME (a cache, lowered
// everywhere) and a real PATH (a host socket — m6e-only). The unified `mounts` list has
// no separate caches concept; `from` discriminates.
const pathMountSubtree = `{
  "tools": {
    "auto-scan": {"image": "d9t/go-tools", "mounts": [
      {"from": "scan-db", "to": "/app/.cache/scan"},
      {"from": "/var/run/docker.sock", "to": "/var/run/docker.sock"}
    ]}
  },
  "nodes": {
    "source-is-secure": {"goal": true, "needs": {"auto-scan": true}}
  }
}`

// TestRealPathMountSkippedOnForge pins the from-discriminator: a NAME mount becomes a
// forge cache; a real-PATH mount (the host docker socket) is m6e-only and never reaches
// a forge step — no Caches entry, nothing referencing the socket in either workflow.
func TestRealPathMountSkippedOnForge(t *testing.T) {
	st, err := ci.Parse([]byte(pathMountSubtree))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rm, err := resolve.Resolve(st)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	m := Build(rm, st, nil)
	scan := steps(m)["auto-scan"]
	if len(scan.Caches) != 1 || scan.Caches[0].Name != "scan-db" {
		t.Fatalf("step Caches = %+v, want only the NAME mount {scan-db, ...}", scan.Caches)
	}
	for _, tgt := range []string{TargetForgejo, TargetGHA} {
		wf, err := Workflow(m, Targets[tgt], ci.Platform{})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(wf), "docker.sock") {
			t.Errorf("%s: a real-path mount must be skipped, found docker.sock:\n%s", tgt, wf)
		}
	}
}

// liveTeardownSubtree mirrors the b19 `live` fuse: an `up` tool, a `test` tool, and a
// `when: always` teardown, each in the `live` group. The teardown hangs off a node
// DOWNSTREAM of the test (container-is-clean needs container-is-tested), so the same
// hosting-node inheritance that orders dc-up-d before container-test orders dc-down LAST.
const liveTeardownSubtree = `{
  "tools": {
    "dc-up-d": {"fuse": "live", "run": "docker compose up --detach"},
    "container-test": {"fuse": "live", "run": "test"},
    "dc-down": {"fuse": "live", "when": "always", "run": "docker compose down --volumes --remove-orphans"}
  },
  "nodes": {
    "container-is-ready":  {"needs": {"dc-up-d": true}},
    "container-is-tested": {"needs": {"container-is-ready": true, "container-test": true}},
    "container-is-clean":  {"goal": true, "needs": {"container-is-tested": true, "dc-down": true}}
  }
}`

// TestAlwaysTeardownLowering pins the cleanup primitive: a `when: always` fused member
// (1) renders an `if: ${{ always() }}` guard so a failing test never leaks the detached
// stack, and (2) lands LAST in the fused job's step order (after up + test), driven by
// the DAG, not by chance.
func TestAlwaysTeardownLowering(t *testing.T) {
	st, err := ci.Parse([]byte(liveTeardownSubtree))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rm, err := resolve.Resolve(st)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	m := Build(rm, st, nil)

	// All three members co-locate in ONE job (a compose stack cannot span jobs).
	j := jobOf(m, testDCDown)
	var order []string
	for _, s := range j.Steps {
		order = append(order, s.Name)
	}
	want := []string{testDCUp, testContainerTest, testDCDown}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("fused step order = %v, want %v (teardown must run last)", order, want)
	}

	// Neutral model: only the teardown carries the guard.
	if got := steps(m)[testDCDown].If; got != "${{ always() }}" {
		t.Errorf("dc-down.If = %q, want %q", got, "${{ always() }}")
	}
	if got := steps(m)[testContainerTest].If; got != "" {
		t.Errorf("container-test.If = %q, want empty (no guard on a normal step)", got)
	}

	// Rendered: the guard appears verbatim on the teardown step, both forges.
	for _, tgt := range []string{TargetGHA, TargetForgejo} {
		out, err := Workflow(m, Targets[tgt], ci.Platform{})
		if err != nil {
			t.Fatalf("%s: %v", tgt, err)
		}
		body := "      - name: dc-down\n" +
			"        if: ${{ always() }}\n" +
			"        run: docker compose down --volumes --remove-orphans\n"
		if !strings.Contains(string(out), body) {
			t.Errorf("%s: missing the guarded teardown step:\n%s", tgt, out)
		}
	}
}

// liveExecSubtree is the liveTeardown shape with the test member lowered to the
// container-exec ACTION (action + the in-container command), pinning that the fused
// live test renders a `uses:` not an inline `docker exec`.
const liveExecSubtree = `{
  "tools": {
    "dc-up-d": {"fuse": "live", "run": "docker compose up --detach"},
    "container-test": {"fuse": "live", "action": "container-exec", "run": "test.d"},
    "dc-down": {"fuse": "live", "when": "always", "run": "docker compose down --volumes --remove-orphans"}
  },
  "nodes": {
    "container-is-ready":  {"needs": {"dc-up-d": true}},
    "container-is-tested": {"needs": {"container-is-ready": true, "container-test": true}},
    "container-is-clean":  {"goal": true, "needs": {"container-is-tested": true, "dc-down": true}}
  }
}`

// TestContainerExecLowering pins that a fused live test tool with `action:
// container-exec` lowers to the versioned action — a clean `uses:` with the derived
// container-instance ref + the in-container command — and that the inline `docker
// exec` shell is GONE from the rendered YAML (Law 2: imperative shell in the action).
func TestContainerExecLowering(t *testing.T) {
	st, err := ci.Parse([]byte(liveExecSubtree))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rm, err := resolve.Resolve(st)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	m := Build(rm, st, nil)

	cs := steps(m)[testContainerTest]
	if cs.Action != "container-exec" {
		t.Fatalf("container-test.Action = %q, want container-exec", cs.Action)
	}
	if got := cs.ExecContainer(); got != "${{ env.M6E_CONTAINER_INSTANCE }}" {
		t.Errorf("ExecContainer = %q, want the derived instance ref", got)
	}

	for _, tgt := range []string{TargetGHA, TargetForgejo} {
		out, err := Workflow(m, Targets[tgt], ci.Platform{})
		if err != nil {
			t.Fatalf("%s: %v", tgt, err)
		}
		s := string(out)
		block := "      - name: container-test\n" +
			"        uses: projectfile/ci-actions/container-exec@v1\n" +
			"        with:\n" +
			"          container: ${{ env.M6E_CONTAINER_INSTANCE }}\n" +
			"          run: test.d\n"
		if !strings.Contains(s, block) {
			t.Errorf("%s: missing container-exec step:\n%s", tgt, s)
		}
		if strings.Contains(s, `docker exec "${M6E_CONTAINER_INSTANCE}"`) {
			t.Errorf("%s: inline `docker exec` must be GONE (now the action)", tgt)
		}
	}
}

// secretsSubtree reuses the liveTeardown shape (a fused `live` group: dc-up-d,
// container-test, dc-down) and adds a built image so the synthetic secrets step has
// an `image:` (the `image: self` target). The secrets declarations themselves ride
// on the Build (the SAME place pf-cli's merged JSON lands in production), NOT in the
// subtree — they are the org.projectfile.ci.secrets subtree, read by LoadBuild.
const secretsSubtree = `{
  "image": "acme/site",
  "tools": {
    "dc-up-d": {"fuse": "live", "run": "docker compose up --detach"},
    "container-test": {"fuse": "live", "run": "test"},
    "dc-down": {"fuse": "live", "when": "always", "run": "docker compose down --volumes --remove-orphans"}
  },
  "nodes": {
    "container-is-ready":  {"needs": {"dc-up-d": true}},
    "container-is-tested": {"needs": {"container-is-ready": true, "container-test": true}},
    "container-is-clean":  {"goal": true, "needs": {"container-is-tested": true, "dc-down": true}}
  }
}`

// secretsBuild is the Build a project declaring org.projectfile.ci.secrets +
// D9T_MISC_TOOLS_IMAGE produces: the RAW secrets JSON (forwarded verbatim to
// provision.sh) and the misc-tools image var (the default-image input).
var secretsBuild = &ci.Build{
	Images: map[string]string{
		"D9T_MISC_TOOLS_IMAGE": "${D9T_DOCKER_REGISTRY}/d9t/misc-tools:${M6E_BASE_IMAGE_DEFAULT_VERSION}",
	},
	Secrets: []byte(`{"gf.security.admin.password":{"docker":{"run":"m6e-secret-random"}},"o9s.mariadb.user":"mariadb"}`),
}

// TestSecretsProvisionStep pins the cloud half of org.projectfile.ci.secrets: a
// declared secrets subtree injects a SYNTHETIC secrets-provision step BEFORE dc-up-d in
// the live (fused) job, dispatching to the external ci-actions library with the RAW
// declarations JSON + the misc-tools default-image + the project's own image. The step
// is invisible to the matrix/env/cred folds (it carries none of those), and a nil/empty
// Secrets injects NOTHING — the empty-subtree no-op invariant.
func TestSecretsProvisionStep(t *testing.T) {
	st, err := ci.Parse([]byte(secretsSubtree))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rm, err := resolve.Resolve(st)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	m := Build(rm, st, secretsBuild)

	// The synthetic step co-locates with the fused live members (a compose stack cannot
	// span jobs — secrets must exist in the SAME workspace dc-up-d reads), FIRST.
	liveJob := jobOf(m, testDCUp)
	var order []string
	for _, s := range liveJob.Steps {
		order = append(order, s.Name)
	}
	wantOrder := []string{ActionSecretsProvision, testDCUp, testContainerTest, testDCDown}
	if !reflect.DeepEqual(order, wantOrder) {
		t.Fatalf("fused step order = %v, want %v (secrets-provision must lead dc-up-d)", order, wantOrder)
	}

	// Neutral model: the synthetic step carries the three Secrets inputs and nothing else.
	sp := steps(m)[ActionSecretsProvision]
	if sp.Action != ActionSecretsProvision {
		t.Errorf("Action = %q, want %q", sp.Action, ActionSecretsProvision)
	}
	if sp.SecretsJSON != string(secretsBuild.Secrets) {
		t.Errorf("SecretsJSON = %q, want the raw declarations verbatim", sp.SecretsJSON)
	}
	// default-image resolves the misc-tools var the SAME way pfCliImageRef resolves the
	// cli image (registry path + tag); the registry make-ref lowers to a forge vars expr.
	if got := sp.SecretsDefaultImage; !strings.Contains(got, "/d9t/misc-tools:") {
		t.Errorf("SecretsDefaultImage = %q, want the misc-tools ref (registry path + tag)", got)
	}
	// image: the project's OWN built image (the `image: self` target), registry-less
	// (composeImage over a workspace basename) + the run-scoped self tag.
	if got := sp.SecretsImage; !strings.Contains(got, "acme/site:") {
		t.Errorf("SecretsImage = %q, want the project's own image ref", got)
	}
	// The synthetic step must NOT perturb the live job's other folds: it carries no
	// Env/EnvNames (those are run-tool concerns), so the fused job's env stays the union
	// of the real members only.
	for _, e := range liveJob.Env {
		if strings.HasPrefix(e.Key, "secrets-") {
			t.Errorf("synthetic step leaked into job env: %+v", e)
		}
	}

	// Rendered: the step lowers to a `uses:` dispatching to the external library, with
	// declarations (YAML-quoted JSON), dir, default-image, and image — on BOTH forges.
	for _, tgt := range []string{TargetGHA, TargetForgejo} {
		out, err := Workflow(m, Targets[tgt], ci.Platform{})
		if err != nil {
			t.Fatalf("%s: %v", tgt, err)
		}
		s := string(out)
		uses := "      - name: secrets-provision\n" +
			"        uses: projectfile/ci-actions/secrets-provision@v1\n"
		if !strings.Contains(s, uses) {
			t.Errorf("%s: missing secrets-provision step:\n%s", tgt, s)
		}
		// declarations is YAML-quoted so the JSON survives the YAML->env round-trip; the
		// raw declarations content is inside.
		if !strings.Contains(s, "          declarations: ") {
			t.Errorf("%s: missing declarations input", tgt)
		}
		if !strings.Contains(s, "gf.security.admin.password") {
			t.Errorf("%s: declarations value missing the raw JSON content", tgt)
		}
		if !strings.Contains(s, "          dir: .secrets\n") {
			t.Errorf("%s: missing dir: .secrets", tgt)
		}
		if !strings.Contains(s, "          default-image: ") {
			t.Errorf("%s: missing default-image input", tgt)
		}
		if !strings.Contains(s, "          image: ") {
			t.Errorf("%s: missing image input", tgt)
		}
		// The step PRECEDES dc-up-d in the rendered job (provision before stack-up).
		spIdx := strings.Index(s, uses)
		upIdx := strings.Index(s, "      - name: dc-up-d\n")
		if spIdx < 0 || upIdx < 0 || spIdx > upIdx {
			t.Errorf("%s: secrets-provision must precede dc-up-d (sp=%d up=%d)", tgt, spIdx, upIdx)
		}
	}
}

// TestSecretsProvisionNoop pins the empty-subtree no-op invariant: a nil/empty
// org.projectfile.ci.secrets injects NO synthetic step — the fused live job renders
// exactly dc-up-d/container-test/dc-down as today (byte-identical ordering).
func TestSecretsProvisionNoop(t *testing.T) {
	st, err := ci.Parse([]byte(secretsSubtree))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rm, err := resolve.Resolve(st)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	// nil Build (no secrets declared) ⇒ no secrets-provision step anywhere.
	m := Build(rm, st, nil)
	if _, ok := steps(m)[ActionSecretsProvision]; ok {
		t.Fatal("nil Build: a secrets-provision step was injected (want none)")
	}
	liveJob := jobOf(m, testDCUp)
	var order []string
	for _, s := range liveJob.Steps {
		order = append(order, s.Name)
	}
	want := []string{testDCUp, testContainerTest, testDCDown}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("nil Build fused order = %v, want %v (no-op invariant)", order, want)
	}
	// An explicitly EMPTY Secrets is the same no-op.
	m2 := Build(rm, st, &ci.Build{})
	if _, ok := steps(m2)[ActionSecretsProvision]; ok {
		t.Fatal("empty Build.Secrets: a secrets-provision step was injected (want none)")
	}
}

// ociPushSubtree is the §9 image-publish branch: an oci-push ACTION leaf that
// CONSUMES the container-build tar (publish-image needs image-built as a sibling,
// exactly like the *-scan-tar nodes) and declares the registry-login env NAMES.
const ociPushSubtree = `{
  "image": "projectfile/cli",
  "tools": {
    "container-build": {"action": "container-build"},
    "oci-push": {"action": "oci-push", "env": ["REGISTRY_USERNAME", "REGISTRY_PASSWORD"]}
  },
  "nodes": {
    "image-built": {"needs": {"container-build": true}},
    "publish-image": {"needs": {"oci-push": true, "image-built": true}},
    "published": {"goal": true, "needs": {"publish-image": true}}
  }
}`

// TestOciPushLowers pins the §9 publish-image branch: oci-push is an ACTION leaf
// (lowers to the externalised ci-actions ref, never `make` in the cloud), yet it is
// a tar CONSUMER — it carries the SAME derived download as the image scans (the
// edge to container-build), pushes the basename the producer stamped, and binds the
// registry-login secrets by NAME via the credentials overlay (Task-1 surface reuse).
func TestOciPushLowers(t *testing.T) {
	st, err := ci.Parse([]byte(ociPushSubtree))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rm, err := resolve.Resolve(st)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	m := Build(rm, st, nil)
	pushStep := steps(m)[testOCIPush]
	pushJob := jobOf(m, testOCIPush) // the publish-image node-job hosting it
	// CONSUMER, not producer: it dispatches to the action AND its node pulls the build
	// tar, but (unlike a portable scan) gets NO M6E_IMAGE_ARCHIVE env — the action reads
	// the tar by artifact-name. The step carries the basename to compose the ref.
	if pushStep.Action != ActionOciPush {
		t.Fatalf("oci-push must keep its action, got %q", pushStep.Action)
	}
	if len(pushJob.Downloads) != 1 || pushJob.Downloads[0].Name != "image"+artifactScopeSuffix {
		t.Errorf("oci-push node should download the build's cell tar, got %+v", pushJob.Downloads)
	}
	if pushStep.ImageBasename != "projectfile/cli" {
		t.Errorf("oci-push needs the basename to compose the push ref, got %q", pushStep.ImageBasename)
	}
	// The git tag rides the same ci:version expression the build-arg uses — the action
	// lowers it into the semver tag cascade (latest/major/minor/patch).
	if want := ciContextExpr[ci.CIKeyVersion]; pushStep.PublishVersion != want {
		t.Errorf("oci-push must carry the git tag for the cascade: want %q, got %q", want, pushStep.PublishVersion)
	}
	for _, e := range pushStep.Env {
		if e.Key == ImageArchiveEnv {
			t.Errorf("an action consumer must not get %s (it reads the tar by name)", ImageArchiveEnv)
		}
	}
	if got := strings.Join(pushJob.EnvNames, ","); got != "REGISTRY_PASSWORD,REGISTRY_USERNAME" {
		t.Errorf("oci-push node must declare the registry-login env NAMES (sorted), got %q", got)
	}

	// Rendered (with a credentials overlay): the externalised ref, the cell-keyed
	// download BEFORE the action, the stamped image ref, and the bound secrets.
	creds := ci.Platform{Credentials: map[string]string{
		"REGISTRY_USERNAME": "${{ secrets.REGISTRY_USERNAME }}",
		"REGISTRY_PASSWORD": "${{ secrets.REGISTRY_PASSWORD }}",
	}}
	out, err := Workflow(m, Targets[TargetGHA], creds)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{
		"uses: projectfile/ci-actions/oci-push@v1",             // externalised, pinned ref
		"      - uses: actions/download-artifact@v8",           // tar pulled first
		"          name: image" + artifactScopeSuffix,          // this cell's stem
		"          artifact-name: image" + artifactScopeSuffix, // handed to the action
		"          image: projectfile/cli:",                    // basename the producer stamped
		"          version: ${{ github.ref_name }}",            // git tag -> action's semver cascade
		"REGISTRY_USERNAME: ${{ secrets.REGISTRY_USERNAME }}",  // credentials overlay binding
	} {
		if !strings.Contains(s, want) {
			t.Errorf("oci-push workflow missing %q\n---\n%s", want, s)
		}
	}
	// OUTPUT_REGISTRY: oci-push ALWAYS threads the push-target var (empty => Docker Hub);
	// it is forge-dependent (set per forge), never a per-tool manifest field.
	if !strings.Contains(s, "registry: ${{ vars.OUTPUT_REGISTRY }}") {
		t.Errorf("oci-push must thread OUTPUT_REGISTRY as the push target\n%s", s)
	}
}

// TestOciPushRegistryVar pins that the push target is the forge-dependent
// OUTPUT_REGISTRY var — ALWAYS threaded, never a per-tool manifest field. A legacy
// manifest `registry:` key (the old input-registry-reuse, removed) is silently ignored:
// the push target is a deployment concern (set per forge), not a manifest concern. The
// INPUT registries (B19_DOCKER_REGISTRY etc.) answer "where base images come FROM";
// OUTPUT_REGISTRY answers "where THIS project publishes TO" — distinct, and the two can
// differ (pull b19/go from kiota.ch, publish d9t/go-tools to ghcr.io).
func TestOciPushRegistryVar(t *testing.T) {
	// A legacy manifest registry: key — now silently ignored (the Manifest field is gone;
	// Parse does not DisallowUnknownFields, so it is dropped).
	sub := strings.Replace(ociPushSubtree,
		`"oci-push": {"action": "oci-push", "env": ["REGISTRY_USERNAME", "REGISTRY_PASSWORD"]}`,
		`"oci-push": {"action": "oci-push", "registry": "PF_DOCKER_REGISTRY", "env": ["REGISTRY_USERNAME", "REGISTRY_PASSWORD"]}`, 1)
	st, err := ci.Parse([]byte(sub))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rm, err := resolve.Resolve(st)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	out, err := Workflow(Build(rm, st, nil), Targets[TargetGHA], ci.Platform{})
	if err != nil {
		t.Fatal(err)
	}
	// The push target is ALWAYS OUTPUT_REGISTRY (forge-dependent), regardless of any
	// legacy manifest registry: key.
	if !strings.Contains(string(out), "registry: ${{ vars.OUTPUT_REGISTRY }}") {
		t.Errorf("oci-push push target must be OUTPUT_REGISTRY:\n%s", out)
	}
	if strings.Contains(string(out), "registry: ${{ vars.PF_DOCKER_REGISTRY }}") {
		t.Errorf("a legacy manifest registry: must NOT be threaded (push target is OUTPUT_REGISTRY):\n%s", out)
	}
}

// TestUnprovidedActionErrors is the defect guard: an `action:` tool with no
// registered ci-actions partial must HARD-FAIL the render, never silently emit `make`.
func TestUnprovidedActionErrors(t *testing.T) {
	const deferred = `{
  "tools": {"dc-up-d": {"action": "service-up"}},
  "nodes": {"up": {"goal": true, "needs": {"dc-up-d": true}}}
}`
	st, _ := ci.Parse([]byte(deferred))
	rm, _ := resolve.Resolve(st)
	_, err := Workflow(Build(rm, st, nil), Targets[TargetGHA], ci.Platform{})
	if err == nil {
		t.Fatal("an unprovided action tool (service-up) must fail the render, not fall through")
	}
	if !strings.Contains(err.Error(), "service-up") || !strings.Contains(err.Error(), "no fragment") {
		t.Errorf("error should name the unprovided action + reason, got: %v", err)
	}
}

// TestContainerBuildBackend proves the two build backends are selectable: the
// per-target default (gha => buildx) lowers to the buildx library entry, the
// org.projectfile.ci.<target>.builder overlay flips it to the buildah analogue
// (same provider, same DAG — only WHERE/HOW it runs), and an unknown backend
// fails fast rather than emitting a dangling action ref.
func TestContainerBuildBackend(t *testing.T) {
	st, err := ci.Parse([]byte(providerSubtree))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rm, err := resolve.Resolve(st)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	model := Build(rm, st, nil)

	// Default: the gha adapter selects buildx.
	def, err := Workflow(model, Targets[TargetGHA], ci.Platform{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(def), "container-build/buildx@v1") {
		t.Errorf("default backend should be buildx:\n%s", def)
	}

	// forgejo defaults to buildah, NOT buildx: our forgejo runner is host-mode
	// podman, where `podman-docker` has no `buildx` subcommand. The daemonless
	// buildah analogue is the per-target default there (gha keeps buildx above).
	fj, err := Workflow(model, Targets[TargetForgejo], ci.Platform{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(fj), "container-build/buildah@v1") {
		t.Errorf("forgejo default backend should be buildah (podman runner):\n%s", fj)
	}
	if strings.Contains(string(fj), "container-build/buildx@v1") {
		t.Errorf("forgejo must not emit a buildx ref (no buildx under podman):\n%s", fj)
	}

	// Overlay override: builder: buildah flips the SAME provider to the daemonless
	// analogue — the only difference is the backend segment of the ref.
	bah, err := Workflow(model, Targets[TargetGHA], ci.Platform{Builder: "buildah"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(bah), "container-build/buildah@v1") {
		t.Errorf("builder: buildah should select the buildah entry:\n%s", bah)
	}
	if strings.Contains(string(bah), "container-build/buildx@v1") {
		t.Errorf("buildah override must not leave a buildx ref:\n%s", bah)
	}

	// Unknown backend is a config error — fail the render, name the offender.
	_, err = Workflow(model, Targets[TargetGHA], ci.Platform{Builder: "kaniko"})
	if err == nil {
		t.Fatal("an unknown builder must fail the render, not emit a dangling ref")
	}
	if !strings.Contains(err.Error(), "kaniko") || !strings.Contains(err.Error(), "build backend") {
		t.Errorf("error should name the bad builder + reason, got: %v", err)
	}
}

// TestRobotTokenSecretIsStorable guards the fleet-wide secret name against a rename
// into a namespace the forge reserves. The `||` fallback would swallow the mistake —
// an unstorable name renders a clean workflow, the secret can never be created, and
// the first symptom is a private submodule failing to clone in CI.
func TestRobotTokenSecretIsStorable(t *testing.T) {
	if err := validateSecretName(RobotTokenSecret); err != nil {
		t.Fatalf("RobotTokenSecret %q is not a name the forge would store: %v", RobotTokenSecret, err)
	}
	// The guard has teeth: the prefixes an author reaches for first are the refused ones.
	for _, bad := range []string{"FORGEJO_TOKEN", "GITEA_ROBOT", "github_token", "ci-robot-token"} {
		if err := validateSecretName(bad); err == nil {
			t.Errorf("%q must be rejected — the forge refuses to store it", bad)
		}
	}
}

// TestCheckoutTokenOverride proves the org.projectfile.ci.<target>.checkout-token knob
// is OPTIONAL (absent => the checkout step renders byte-identical to before, so the
// fleet is untouched) and NON-DESTRUCTIVE when set (the emitted expression falls back
// to the run-scoped token, so a project may opt in before the forge holds the secret).
func TestCheckoutTokenOverride(t *testing.T) {
	st, err := ci.Parse([]byte(providerSubtree))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rm, err := resolve.Resolve(st)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	model := Build(rm, st, nil)

	// No overlay: no `token:` input at all — the checkout keeps the run-scoped default.
	def, err := Workflow(model, Targets[TargetForgejo], ci.Platform{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(def), "token:") {
		t.Errorf("absent checkout-token must emit no token input:\n%s", def)
	}

	// Opted in: the step authenticates as the robot account under the ONE fleet-wide
	// name, falling back to the run-scoped token when the secret is not provisioned.
	out, err := Workflow(model, Targets[TargetForgejo], ci.Platform{CheckoutToken: true})
	if err != nil {
		t.Fatal(err)
	}
	want := "          token: ${{ secrets." + RobotTokenSecret + " || github.token }}"
	if !strings.Contains(string(out), want) {
		t.Errorf("checkout-token opt-in not applied under the checkout step:\n%s", out)
	}
	// The knob touches the checkout step ONLY — submodules stay on, nothing else moves.
	if strings.Count(string(out), "token: ${{ secrets.") != strings.Count(string(out), "submodules: true") {
		t.Errorf("token must ride every checkout and nothing else:\n%s", out)
	}

	// Same knob, same constant on the other target — an operator provisions ONE secret
	// per organisation, never a per-forge or per-project name.
	gha, err := Workflow(model, Targets[TargetGHA], ci.Platform{CheckoutToken: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(gha), want) {
		t.Errorf("gha must spell the same fleet-wide secret:\n%s", gha)
	}
}

// TestActionRefOverride proves the org.projectfile.ci.<target>.actions overlay
// replaces the per-target adapter action refs (checkout + the ci-actions library),
// that an absent overlay keeps the defaults, and that an unknown slot fails fast.
func TestActionRefOverride(t *testing.T) {
	st, err := ci.Parse([]byte(providerSubtree))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rm, err := resolve.Resolve(st)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	model := Build(rm, st, nil)

	// Default (no overlay): adapter constants — checkout@v7 + projectfile/ci-actions@v1.
	def, err := Workflow(model, Targets[TargetGHA], ci.Platform{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(def), "actions/checkout@v7") ||
		!strings.Contains(string(def), "projectfile/ci-actions/container-build/buildx@v1") {
		t.Fatalf("default refs missing:\n%s", def)
	}

	// Overlay: every slot set. checkout is swapped wholesale; the ci-actions `repo@tag`
	// ref splits at the final `@` into library + version, recomposed per action path.
	// download/upload arms are exercised here too (same shape) even though this model
	// emits no artifact step to print them.
	over := ci.Platform{Actions: map[string]string{
		SlotCheckout:         "actions/checkout@v99",
		SlotDownloadArtifact: "actions/download-artifact@v42",
		SlotUploadArtifact:   "actions/upload-artifact@v41",
		SlotCIActions:        "acme/actions@v5",
	}}
	out, err := Workflow(model, Targets[TargetGHA], over)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "actions/checkout@v99") {
		t.Errorf("checkout override not applied:\n%s", out)
	}
	if !strings.Contains(string(out), "acme/actions/container-build/buildx@v5") {
		t.Errorf("ci-actions library override (repo@tag split) not applied:\n%s", out)
	}
	if strings.Contains(string(out), "actions/checkout@v7") ||
		strings.Contains(string(out), "projectfile/ci-actions") {
		t.Errorf("defaults leaked past the override:\n%s", out)
	}

	// A `ci-actions` value with no `@tag` is a config error, not a dangling ref.
	_, err = Workflow(model, Targets[TargetGHA], ci.Platform{Actions: map[string]string{SlotCIActions: "acme/actions"}})
	if err == nil || !strings.Contains(err.Error(), "repo@tag") {
		t.Fatalf("a tagless ci-actions ref must fail naming the expected form, got: %v", err)
	}

	// An unknown slot fails fast and names itself (deterministic — sorted iteration).
	_, err = Workflow(model, Targets[TargetGHA], ci.Platform{Actions: map[string]string{"setup-node": "actions/setup-node@v6"}})
	if err == nil || !strings.Contains(err.Error(), "setup-node") {
		t.Fatalf("an unknown action slot must fail naming the offender, got: %v", err)
	}
}

func TestUnknownTargetAbsent(t *testing.T) {
	if _, ok := Targets["tekton"]; ok {
		t.Fatal("tekton is a different engine — it must NOT share the GHA template registry yet")
	}
}

// liveSubtree is the b19 dynamic-test region: a per-series build (container-build
// ACTION, emits the OCI tar) feeds a stack bring-up (dc-up-d) and a test run
// (container-test), BOTH carrying `fuse: live`. On an ephemeral runner a stack
// started in one job is gone in the next, so the two `live` leaves must COLLAPSE
// into one job — derived from the shared fuse group + the needs-edge between them,
// with NO leaf-name special-casing. The cross-group edge to container-build
// survives and brings the cell-keyed tar with it (load → up → test as run-steps).
const liveSubtree = `{
  "image": "b19/ubuntu",
  "matrix": {"axes": {"B19_UBUNTU_SERIES": ["resolute", "noble"]}},
  "tools": {
    "container-build": {"action": "container-build"},
    "dc-up-d":         {"fuse": "live", "run": "docker compose up -d --wait", "set-env": {"COMPOSE_FILE": ".compose/pipeline.yaml"}},
    "container-test":  {"fuse": "live", "run": "docker exec \"${M6E_CONTAINER_INSTANCE}\" test.d"}
  },
  "nodes": {
    "image-built":      {"matrix": true, "needs": {"container-build": true}},
    "container-ready":  {"matrix": true, "needs": {"image-built": true, "dc-up-d": true}},
    "container-tested": {"matrix": true, "needs": {"container-ready": true, "container-test": true}},
    "ready-to-publish": {"goal": true, "needs": {"container-tested": true}}
  }
}`

// TestLiveLeavesFuse pins the CO-LOCATION law: a set of same-`fuse` leaves
// connected by needs collapses to ONE job at the subgraph's sink, rendering each
// member as an ordered run-step. The internal edge (container-test→dc-up-d)
// vanishes; the cross-group edge (→container-build) survives and DERIVES the
// OCI-tar load. The law is generic — it names no node, goal, or specific group; it
// reads the `fuse` value and the needs-edges only.
func TestLiveLeavesFuse(t *testing.T) {
	st, err := ci.Parse([]byte(liveSubtree))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rm, err := resolve.Resolve(st)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	m := Build(rm, st, nil)
	by := map[string]JobView{}
	for _, j := range m.Jobs {
		by[j.Name] = j
	}
	// No member survives as its own job — both `live` leaves are now STEPS.
	if jobOf(m, testDCUp).Name != "container-tested" {
		t.Errorf("dc-up-d must be ABSORBED into the fused live node-job, got host %q", jobOf(m, testDCUp).Name)
	}
	// The fused job is named for the sink's NODE (container-tested), hosts the ordered
	// member steps, and is a CELL (per series). A gate stands for the absorbed member's
	// node (container-ready).
	sink, ok := by["container-tested"]
	if !ok {
		t.Fatalf("the fused job must be the sink's node container-tested; jobs: %+v", m.Jobs)
	}
	if cr := by["container-ready"]; !cr.IsGate {
		t.Errorf("the absorbed member's node (container-ready) must become an echo gate: %+v", cr)
	}
	// The fused job runs its members as ordered run-steps: dc-up-d (dependency) first,
	// then container-test (the sink). No `action`, no `make` — just the run commands.
	if len(sink.Steps) != 2 ||
		sink.Steps[0].Action != "" ||
		sink.Steps[0].Command() != "docker compose up -d --wait" ||
		sink.Steps[1].Command() != `docker exec "${M6E_CONTAINER_INSTANCE}" test.d` {
		t.Errorf("fused job steps must be [up, test] in dependency order, got %+v", sink.Steps)
	}
	// Compose-runtime env on the fused job: the AUTHORED set-env literal (COMPOSE_FILE)
	// PLUS the resolver-DERIVED build→live contract vars — M6E_IMAGE_FULLNAME (the loaded
	// ref, composeImage over the per-cell basename) and the live-stack IDENTITY
	// M6E_COMPOSE_PROJECT_NAME / M6E_CONTAINER_INSTANCE = `ci-<basename>` (slashes→dashes,
	// the same stem m6e's make plane computes — NOT a hardcoded `ci-app`). None is a
	// secret; all ride the job env block.
	jobEnv := map[string]string{}
	for _, e := range sink.Env {
		jobEnv[e.Key] = e.Value
	}
	wantImage := "b19/ubuntu:${{ env.M6E_TAG }}" + artifactScopeSuffix
	if jobEnv[ImageFullnameEnv] != wantImage {
		t.Errorf("fused live job must inject %s=%q (the run-scoped loaded ref), got %q", ImageFullnameEnv, wantImage, jobEnv[ImageFullnameEnv])
	}
	if jobEnv["COMPOSE_FILE"] != ".compose/pipeline.yaml" {
		t.Errorf("authored set-env literal COMPOSE_FILE must land on the fused job env, got %+v", jobEnv)
	}
	// IDENTITY = ci-<basename> (no ci-app hardcode) + a per-RUN suffix so two runs of the
	// same pipeline never name one stack (the resolute rc=137: a sibling/other-run `down`
	// reaped this cell's live container). run_id is unique per run, run_attempt per re-run.
	wantIdent := "ci-b19-ubuntu" + runScopeSuffix
	if jobEnv[ComposeProjectEnv] != wantIdent || jobEnv[ContainerInstanceEnv] != wantIdent {
		t.Errorf("fused live job must DERIVE %s/%s=%q (basename + run scope), got %+v",
			ComposeProjectEnv, ContainerInstanceEnv, wantIdent, jobEnv)
	}
	// The image ref MUST be run-scoped: the unique tag (selfImageTagExpr) cannot be
	// shadowed by a stale same-named image in the runner's docker store, and a trailing
	// docker-cleanup reaps it at job end. Stamp + load + run all flow through composeImage,
	// so they agree on the run-scoped tag.
	if !strings.Contains(jobEnv[ImageFullnameEnv], "github.run_id") {
		t.Errorf("%s MUST be run-scoped (unique tag to avoid docker-store shadowing), got %q", ImageFullnameEnv, jobEnv[ImageFullnameEnv])
	}
	// …and it MUST NOT carry run_attempt. The tag is stamped into the run_id-scoped
	// artifact by container-build, so an attempt-scoped tag makes a re-run load a tar
	// tagged for the attempt that BUILT it and then ask compose for its own attempt's
	// tag: the lookup misses the store and falls through to a registry pull of an image
	// that exists nowhere ("manifest unknown"). Once any Load-ing cell failed, no re-run
	// could ever go green again.
	if strings.Contains(jobEnv[ImageFullnameEnv], "run_attempt") {
		t.Errorf("%s MUST NOT be attempt-scoped (a re-run consumes an earlier attempt's artifact), got %q", ImageFullnameEnv, jobEnv[ImageFullnameEnv])
	}
	// The run-scoped tag is reaped from the daemon at job end (else the store gains one
	// image per run forever — the mutable tag self-recycled; the unique one does not).
	if sink.RmiImage != jobEnv[ImageFullnameEnv] {
		t.Errorf("fused job must queue %s=%q for end-of-job docker rmi, got %q", ImageFullnameEnv, jobEnv[ImageFullnameEnv], sink.RmiImage)
	}
	// Same for the stack's network: `down` cannot remove one that still has an endpoint
	// (the `up`-failed path) and does not fail the step, so an unreaped run-scoped network
	// leaks per run. The reaped name MUST be the one a `network: live` tool joins.
	if want := jobEnv[ComposeProjectEnv] + composeNetworkSuffix; sink.RmNetwork != want {
		t.Errorf("fused job must queue the live network %q for end-of-job removal, got %q", want, sink.RmNetwork)
	}
	if sink.Class != testCell || len(sink.Matrix) != 1 {
		t.Errorf("fused job must remain a CELL with the build's axis: %+v", sink)
	}
	// Node needs: the authored node edge container-tested → container-ready. image-built
	// is reached TRANSITIVELY (container-ready → image-built), which orders the build it
	// loads — no extra edge needed in the node model.
	if len(sink.Needs) != 1 || sink.Needs[0] != "container-ready" {
		t.Errorf("fused node needs must be its authored node-dep [container-ready], got %v", sink.Needs)
	}
	// The build→live tar hand-off is DERIVED from a member's build edge: the node pulls
	// the cell-keyed tar ONCE and `docker load`s it into the daemon for compose.
	wantStem := testImageMatrixStem
	if !sink.Load || sink.Stem != wantStem {
		t.Errorf("fused job must load the build's cell tar (Load + Stem=%q), got Load=%v Stem=%q", wantStem, sink.Load, sink.Stem)
	}
	if len(sink.Downloads) != 1 || sink.Downloads[0].Name != wantStem {
		t.Errorf("fused job must download the build's cell tar once, got %+v", sink.Downloads)
	}
	// No-anonymous-images: the explicit basename still rides the PRODUCER step to stamp
	// the archive name=; the fused job just `docker load`s that tagged tar.
	if got := steps(m)[testContainerBuild].ImageBasename; got != "b19/ubuntu" {
		t.Errorf("container-build must carry the image basename to stamp name=, got %q", got)
	}

	// Render: the tar download + `docker load`, then the member run-steps, NO
	// standalone dc-up-d job, NO live action ref, and NO `make` for either leaf.
	gha, err := Workflow(m, Targets[TargetGHA], ci.Platform{})
	if err != nil {
		t.Fatal(err)
	}
	s := string(gha)
	for _, want := range []string{
		"container-tested:", // the NODE-job hosting the fused live steps
		"uses: actions/download-artifact@v8",
		"name: " + testImageMatrixStem,
		"uses: projectfile/ci-actions/container-load@v1",
		"archive: " + testImageMatrixStem + ".tar",
		"run: docker compose up -d --wait",
		`run: docker exec "${M6E_CONTAINER_INSTANCE}" test.d`,
		// Job `env:` values carry the EXPANDED tag (the env context does not exist yet
		// while the block is evaluated) — see inlineHoists.
		"M6E_IMAGE_FULLNAME: b19/ubuntu:${{ vars.BASE_IMAGE_DEFAULT_VERSION || 'latest' }}",
		"COMPOSE_FILE: .compose/pipeline.yaml",
		"M6E_COMPOSE_PROJECT_NAME: ci-b19-ubuntu",
		"M6E_CONTAINER_INSTANCE: ci-b19-ubuntu",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("fused live workflow missing %q\n---\n%s", want, s)
		}
	}
	// The dissolved live job no longer dispatches to an external action.
	if strings.Contains(s, "ci-actions/live") {
		t.Errorf("fused live must be plain run-steps, not a live action ref:\n%s", s)
	}
	if strings.Contains(s, "\n  dc-up-d:") {
		t.Errorf("absorbed dc-up-d leaked a standalone job key:\n%s", s)
	}
	// INDENT GUARD: the fused body lands at 6 spaces under steps: — checkout, then
	// the build-tar download, then the load + member run-steps (same column).
	if !strings.Contains(s, "\n    steps:\n      - uses: actions/checkout@v7\n        with:\n          submodules: true\n      - uses: actions/download-artifact@v8") {
		t.Errorf("fused live body lost its 6-space indent under steps:\n%s", s)
	}
	for _, gone := range []string{"make dc-up-d", "make container-test", "run: dc-up-d\n", "run: container-test\n"} {
		if strings.Contains(s, gone) {
			t.Errorf("a fused live leaf degraded to a bare-name host invocation %q:\n%s", gone, s)
		}
	}
}

// TestFusedGroupSpanningGateIsAcyclic is the regression for the gate↔fused-sink
// cycle. When a `fuse` group SPANS a gate (the `live` up→test pair fuses ACROSS
// the container-ready milestone), the gate's own absorbed member (dc-up-d)
// redirects to the surviving sink (container-test) while that sink already
// depends on the gate via the downstream member's node-deps — a job→job cycle no
// node→node preflight can see, which made `act` refuse the whole workflow ("could
// not find any stages to run"). The deferred-edge fix drops the cycle-closing
// gate→sink edge, leaving the forward chain image-built → container-ready →
// container-test → container-tested.
func TestFusedGroupSpanningGateIsAcyclic(t *testing.T) {
	st, err := ci.Parse([]byte(liveSubtree))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rm, err := resolve.Resolve(st)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	m := Build(rm, st, nil)
	adj := map[string][]string{}
	for _, j := range m.Jobs {
		adj[j.Name] = j.Needs
	}
	// The submerged gate (container-ready) must NOT need the node that absorbed its
	// member (container-tested) — that back-edge is what would close the cycle.
	if contains(adj["container-ready"], "container-tested") {
		t.Errorf("container-ready must not need the fused node container-tested (the cycle back-edge): %v", adj["container-ready"])
	}
	// The forward ordering survives: the fused node still waits on the gate.
	if !contains(adj["container-tested"], "container-ready") {
		t.Errorf("fused node container-tested must still wait on the container-ready gate, got %v", adj["container-tested"])
	}
	// Whole graph must topologically sort — the property `act` needs to build its
	// stage list. Three-colour DFS: a back-edge to a node still on the stack = cycle.
	const (
		white = 0
		grey  = 1
		black = 2
	)
	colour := map[string]int{}
	var path []string
	var walk func(string) string
	walk = func(n string) string {
		colour[n] = grey
		path = append(path, n)
		for _, d := range adj[n] {
			switch colour[d] {
			case grey:
				return strings.Join(append(path, d), " -> ")
			case white:
				if c := walk(d); c != "" {
					return c
				}
			}
		}
		path = path[:len(path)-1]
		colour[n] = black
		return ""
	}
	for _, j := range m.Jobs {
		if colour[j.Name] == white {
			path = path[:0]
			if c := walk(j.Name); c != "" {
				t.Fatalf("rendered job graph has a cycle: %s", c)
			}
		}
	}
}

// TestTeardownRendersNothing pins the "ephemeral runner = implicit teardown"
// guarantee (capabilities-plan 3a). The maintenance umbrellas (cleaned /
// deep-cleaned and their dc-down / image-remove / fetch-clean leaves) are
// UNFLAGGED sinks that sit OUTSIDE the goal's needs-closure. Because a flagged
// goal suppresses the all-sinks fallback (resolve.goals), those nodes are
// unreachable and their tools never become jobs — the runner is discarded whole,
// so there is nothing to tear down. If a future manifest accidentally pulls a
// teardown leaf into ready-to-publish's closure, this fails loudly.
func TestTeardownRendersNothing(t *testing.T) {
	const withTeardown = `{
  "tools": {"container-build": {"run": "make container-build"}},
  "nodes": {
    "ready-to-publish": {"goal": true, "needs": {"image-built": true}},
    "image-built":      {"needs": {"container-build": true}},
    "cleaned":          {"needs": {"dc-down": true}},
    "deep-cleaned":     {"needs": {"image-remove": true, "fetch-clean": true}}
  }
}`
	st, err := ci.Parse([]byte(withTeardown))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rm, err := resolve.Resolve(st)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	m := Build(rm, st, nil)
	for _, j := range m.Jobs {
		switch j.Name {
		// Teardown NODES (and so their tool steps) must not materialise at all — out of
		// the goal's closure means out of the workflow entirely.
		case "cleaned", "deep-cleaned":
			t.Errorf("unreachable teardown node %q materialised a job — closure is leaking", j.Name)
		}
		for _, s := range j.Steps {
			switch s.Name {
			case testDCDown, "image-remove", "fetch-clean":
				t.Errorf("teardown leaf %q leaked into the job model — the implicit-teardown guarantee is broken", s.Name)
			}
		}
	}
	// Sanity: the real work IS present (not vacuously green). The reachable nodes are
	// image-built (a job hosting the container-build step) and ready-to-publish (a pure-
	// join gate); nothing from the unreachable teardown subtree.
	var jobsWithSteps, gates []string
	for _, j := range m.Jobs {
		if j.IsGate {
			gates = append(gates, j.Name)
		} else {
			jobsWithSteps = append(jobsWithSteps, j.Name)
		}
	}
	if len(jobsWithSteps) != 1 || jobsWithSteps[0] != testImageBuilt {
		t.Errorf("want exactly the image-built node-job, got %v", jobsWithSteps)
	}
	if steps(m)[testContainerBuild].Run != "make container-build" {
		t.Errorf("image-built must host the container-build step, got step %+v", steps(m)[testContainerBuild])
	}
	if len(gates) != 1 || gates[0] != "ready-to-publish" {
		t.Errorf("want a gate for the pure-join node [ready-to-publish], got %v", gates)
	}
}

// TestNodesMaterialiseAsGates pins the node-GATE lowering (point 2): every
// reachable DAG node — including a pure-join goal (`goal: true`, only `needs`, no
// tool of its own) and the abstract intermediates — renders as a no-op gate job, so
// the emitted graph mirrors the AUTHORED DAG instead of the contracted tool edges.
// The gates are observability, not correctness: GHA/Forgejo already fail a run when
// ANY job fails, so the join needs no aggregator — but a visible `ready-to-publish`
// gate is a debuggable milestone and a branch-protection target. A gate runs a bare
// `echo <node>` with no checkout; tool leaves still render as real jobs.
func TestNodesMaterialiseAsGates(t *testing.T) {
	const pureJoin = `{
  "tools": {"container-build": {"run": "make container-build"}},
  "nodes": {
    "ready-to-publish": {"goal": true, "needs": {"linted": true, "image-built": true}},
    "linted":           {"needs": {"shellcheck": true}},
    "image-built":      {"needs": {"container-build": true}}
  }
}`
	st, err := ci.Parse([]byte(pureJoin))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rm, err := resolve.Resolve(st)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	// The join IS a resolved terminal (it drives the closure)...
	found := false
	for _, g := range rm.Goals {
		if g == "ready-to-publish" {
			found = true
		}
	}
	if !found {
		t.Fatalf("ready-to-publish should be a resolved goal, got %v", rm.Goals)
	}
	// ...and now (Decision 2) every reachable node materialises as ONE job: a node WITH
	// tools is a real job hosting them as steps; a PURE-JOIN node (no tools) is an echo
	// gate. The job graph still mirrors the authored DAG (needs = node-deps).
	m := Build(rm, st, nil)
	by := map[string]JobView{}
	for _, j := range m.Jobs {
		by[j.Name] = j
	}
	// The two tool-bearing nodes are real jobs (NOT gates) hosting their tool as a step.
	wantStepJobs := map[string]string{ // node -> its sole tool step
		"linted":       testShellcheck,
		testImageBuilt: testContainerBuild,
	}
	for node, tool := range wantStepJobs {
		j, ok := by[node]
		if !ok || j.IsGate {
			t.Errorf("node %q must be a real job hosting steps, got %+v", node, j)
			continue
		}
		if len(j.Steps) != 1 || j.Steps[0].Name != tool {
			t.Errorf("node %q must host exactly the %q step, got %+v", node, tool, j.Steps)
		}
	}
	// The pure-join goal is a gate mirroring its node-deps.
	g, ok := by["ready-to-publish"]
	if !ok || !g.IsGate || g.Class != ClassGate {
		t.Errorf("pure-join ready-to-publish must be a gate (IsGate + ClassGate), got %+v", g)
	}
	if !reflect.DeepEqual(g.Needs, []string{testImageBuilt, "linted"}) {
		t.Errorf("gate ready-to-publish needs = %v, want [image-built linted]", g.Needs)
	}
	// The tool leaves are STEPS now, never top-level jobs.
	for _, leaf := range []string{testShellcheck, testContainerBuild} {
		if _, isJob := by[leaf]; isJob {
			t.Errorf("tool leaf %q must be a step, not a top-level job", leaf)
		}
	}
	gha, err := Workflow(m, Targets[TargetGHA], ci.Platform{})
	if err != nil {
		t.Fatal(err)
	}
	s := string(gha)
	// The pure-join goal renders as a bare echo gate, no checkout.
	if !strings.Contains(s, "  ready-to-publish:\n    runs-on: ubuntu-latest\n    needs: [\"image-built\", \"linted\"]\n    steps:\n      - run: echo ready-to-publish") {
		t.Errorf("pure-join goal must render as an echo gate mirroring its needs:\n%s", s)
	}
	// The tool-bearing nodes render as jobs (the tools are inside them as steps).
	for _, node := range []string{"linted:", "image-built:"} {
		if !strings.Contains(s, node) {
			t.Errorf("node-job %q missing from workflow:\n%s", node, s)
		}
	}
}

// whenSubtree exercises the neutral `when` trigger predicate (the-last-stand
// Phase 1): a tag-gated publish leaf, a main-gated release leaf, and the un-gated
// build/lint that run on every event. It is the shape b19/cli adopt — publish on a
// tag, release on a push to main, everything else always.
const whenSubtree = `{
  "image": "b19/ubuntu",
  "tools": {
    "container-build": {"action": "container-build"},
    "oci-push":  {"action": "oci-push", "env": ["REGISTRY_USERNAME", "REGISTRY_PASSWORD"]},
    "release":   {"run": "semantic-release"}
  },
  "nodes": {
    "image-built":   {"needs": {"container-build": true}},
    "publish-image": {"when": {"events": ["tag"]}, "needs": {"oci-push": true, "image-built": true}},
    "released":      {"when": {"events": ["push:main"]}, "needs": {"release": true, "image-built": true}},
    "done":          {"goal": true, "needs": {"publish-image": true, "released": true}}
  }
}`

// TestWhenTriggerPredicate pins the neutral per-node trigger primitive: a node's
// `when.events` lower to (a) a per-job `if:` on that node's gate AND the tools it
// owns, and (b) — only when every job is gated — a narrowed `on:` surface. The
// tokens stay vendor-neutral in the model; the GHA `if:`/`on:` spelling is composed
// at render. Crucially, the presence of ANY un-gated job (build/lint) keeps `on:`
// broad, so the file is byte-compatible with today's and gating rides on `if:`.
func TestWhenTriggerPredicate(t *testing.T) {
	st, err := ci.Parse([]byte(whenSubtree))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rm, err := resolve.Resolve(st)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	m := Build(rm, st, nil)

	// Neutral model carries the event tokens, not the vendor spelling — on the NODE-job
	// that hosts each tool (Decision 2: the node carries the predicate, the tool is a step).
	if got := strings.Join(jobOf(m, testOCIPush).Events, ","); got != "tag" {
		t.Errorf("publish-image (hosting oci-push) should carry its tag predicate, got %q", got)
	}
	if got := strings.Join(jobOf(m, "release").Events, ","); got != "push:main" {
		t.Errorf("released (hosting release) should carry push:main, got %q", got)
	}
	// The un-gated build node is unconditional (empty) even though a gated node needs it.
	if len(jobOf(m, testContainerBuild).Events) != 0 {
		t.Errorf("image-built is needed un-gated → must stay unconditional, got %v", jobOf(m, testContainerBuild).Events)
	}

	out, err := Workflow(m, Targets[TargetGHA], ci.Platform{})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	// (a) per-job if: the tag publish node and the main release node each carry it.
	for _, want := range []string{
		"  publish-image:\n",
		"    if: startsWith(github.ref, 'refs/tags/')",
		"    if: github.ref == 'refs/heads/main'",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("when workflow missing %q\n---\n%s", want, s)
		}
	}
	// (b) on: stays BROAD — an un-gated job (image-built) forces the wide surface;
	// gating is by if:, so the trigger block is byte-identical to a no-when workflow.
	if !strings.Contains(s, "\"on\":\n  push: {}\n  pull_request: {}\n") {
		t.Errorf("with an un-gated job present, on: must stay broad push/pull_request:\n%s", s)
	}
	// The un-gated build node must NOT carry an if: line (it runs on every event).
	if strings.Contains(s, "  image-built:\n    runs-on: ubuntu-latest\n    if:") {
		t.Errorf("image-built (un-gated) must not get an if: gate:\n%s", s)
	}
}

// TestWhenNarrowsOnWhenAllGated pins the narrowing path: when EVERY reachable job
// is gated (no un-gated build/lint), the `on:` surface shrinks to exactly the union
// of the declared events — a tag-only + main workflow does not spawn a run per PR.
// Multiple events on one node OR together in the job `if:`.
func TestWhenNarrowsOnWhenAllGated(t *testing.T) {
	const allGated = `{
  "tools": {"release": {"run": "semantic-release"}, "publish": {"run": "publish"}},
  "nodes": {
    "released":  {"when": {"events": ["push:main", "tag"]}, "needs": {"release": true}},
    "published": {"goal": true, "when": {"events": ["tag"]}, "needs": {"publish": true, "released": true}}
  }
}`
	st, err := ci.Parse([]byte(allGated))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rm, err := resolve.Resolve(st)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	out, err := Workflow(Build(rm, st, nil), Targets[TargetGHA], ci.Platform{})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	// Narrowed surface: a push block with both the branch filter and the tag glob,
	// and NO pull_request (no job runs on PRs, so the workflow never fires for one).
	for _, want := range []string{
		"  push:\n    branches: [\"main\"]\n    tags: [\"**\"]",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("narrowed on: missing %q\n---\n%s", want, s)
		}
	}
	if strings.Contains(s, "pull_request") {
		t.Errorf("an all-gated (tag/main) workflow must not trigger on pull_request:\n%s", s)
	}
	if strings.Contains(s, "push: {}") {
		t.Errorf("an all-gated workflow must narrow push:, not stay broad:\n%s", s)
	}
	// The multi-event node OR-s its predicates (each parenthesised).
	want := "if: (github.ref == 'refs/heads/main') || (startsWith(github.ref, 'refs/tags/'))"
	if !strings.Contains(s, want) {
		t.Errorf("multi-event node must OR its predicates:\n%s", s)
	}
}

// previewSubtree gates the publish node on `tag` OR `preview`, the b19 publish policy
// shape: a release still publishes its cascade, and a push to a branch nobody can name
// ahead of time publishes a preview of it.
const previewSubtree = `{
  "image": "b19/ubuntu",
  "tools": {
    "container-build": {"action": "container-build"},
    "oci-push": {"action": "oci-push", "emit": "ci.image.published"}
  },
  "nodes": {
    "image-built":   {"needs": {"container-build": true}},
    "publish-image": {"when": {"events": ["tag", "preview"]}, "needs": {"oci-push": true, "image-built": true}},
    "done":          {"goal": true, "needs": {"publish-image": true}}
  }
}`

// TestPreviewGateAndInput pins the whole preview lowering: the job gate that admits a
// non-primary branch, the ref FACT handed to the action (never the tag policy — that is
// the versioned action's, like the semver cascade beside it), and the downstream rebuild
// fact a preview must NOT emit.
func TestPreviewGateAndInput(t *testing.T) {
	st, err := ci.Parse([]byte(previewSubtree))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rm, err := resolve.Resolve(st)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	b := &ci.Build{Events: &ci.Events{WebhookVar: ci.DefaultWebhookVar}}
	out, err := Workflow(Build(rm, st, b), Targets[TargetGHA], ci.Platform{})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)

	for _, want := range []string{
		// The gate: a tag, OR a branch ref that is none of the primary names. Both
		// operands read the `github` context alone, which is what keeps it legal on a
		// JOB `if:` — see TestJobIfNeverReadsTheMatrixContext.
		// Token order is decodeWhen's sort, which is what keeps the render byte-stable.
		"if: (startsWith(github.ref, 'refs/heads/') && github.ref != 'refs/heads/main' && " +
			"github.ref != 'refs/heads/master') || (startsWith(github.ref, 'refs/tags/'))",
		// The FACT: which branch, empty on a tag. What it makes of it (one
		// `latest-<branch>` tag, primary sink only) is the action's rule, not ours.
		"preview: ${{ github.ref_type == 'branch' && github.ref_name || '' }}",
		// The release input is untouched beside it — a preview narrows, never replaces.
		"version: ${{ github.ref_name }}",
		// A preview publish must not announce a published image: the router turns that
		// fact into a rebuild dispatch for every consumer of it.
		"github.ref_type == 'tag'",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("preview lowering missing %q\n---\n%s", want, s)
		}
	}
}

// TestPreviewPrimaryBranchIsDeclared pins the override half: a project that records its
// default branch is gated on THAT name, so a fleet convention never overrides a document.
func TestPreviewPrimaryBranchIsDeclared(t *testing.T) {
	st, err := ci.Parse([]byte(previewSubtree))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rm, err := resolve.Resolve(st)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	out, err := Workflow(Build(rm, st, &ci.Build{PrimaryBranches: []string{"release"}}),
		Targets[TargetGHA], ci.Platform{})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.Contains(s, "github.ref != 'refs/heads/release'") {
		t.Errorf("a declared primary branch must drive the gate:\n%s", s)
	}
	// The fleet default must be GONE, not merely joined — a project on `release` pushes
	// to `main` as an ordinary branch, and that has to publish a preview.
	if strings.Contains(s, "github.ref != 'refs/heads/main'") {
		t.Errorf("a declared primary branch must REPLACE the default set:\n%s", s)
	}
}

// TestPreviewWidensThePushSurface pins the `on:` half. A preview cannot contribute a
// branch NAME — that is the entire point of the token — so an all-gated file widens to
// every branch and leaves the narrowing to the job `if:`, while still keeping its tag
// glob rather than collapsing to a bare `push: {}`.
func TestPreviewWidensThePushSurface(t *testing.T) {
	const allGated = `{
  "tools": {"publish": {"run": "publish"}},
  "nodes": {
    "published": {"goal": true, "when": {"events": ["tag", "preview"]}, "needs": {"publish": true}}
  }
}`
	st, err := ci.Parse([]byte(allGated))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rm, err := resolve.Resolve(st)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	out, err := Workflow(Build(rm, st, nil), Targets[TargetGHA], ci.Platform{})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.Contains(s, "  push:\n    branches: [\"**\"]\n    tags: [\"**\"]") {
		t.Errorf("a [tag, preview] file must widen to every branch and keep its tag glob:\n%s", s)
	}
	if strings.Contains(s, "pull_request") {
		t.Errorf("an all-gated preview workflow must not trigger on pull_request:\n%s", s)
	}
}

// TestWhenRejectsUnknownEvent is the parse guard: a token outside the closed
// vocabulary fails the parse rather than silently widening (or muting) a trigger.
func TestWhenRejectsUnknownEvent(t *testing.T) {
	const bad = `{
  "nodes": {"published": {"goal": true, "when": {"events": ["deploy"]}, "needs": {"publish": true}}}
}`
	_, err := ci.Parse([]byte(bad))
	if err == nil {
		t.Fatal("an unknown when event (deploy) must fail the parse")
	}
	if !strings.Contains(err.Error(), "deploy") || !strings.Contains(err.Error(), "when") {
		t.Errorf("error should name the bad event + the when field, got: %v", err)
	}
	// An empty events list is equally a defect.
	_, err = ci.Parse([]byte(`{"nodes":{"n":{"when":{"events":[]},"needs":{"t":true}}}}`))
	if err == nil || !strings.Contains(err.Error(), "at least one event") {
		t.Errorf("empty when.events must fail with a clear message, got: %v", err)
	}
}

// TestDispatchTriggerEmitsButton pins the manual-dispatch surface (the dispatch button
// + parametrised-build inputs): declaring a GOAL node's dispatch adds workflow_dispatch
// to its file's `on:` with each typed input, ADDITIVE to the broad push/PR surface an
// un-gated job keeps. Defaults render YAML-typed (boolean bare, string/choice quoted).
func TestDispatchTriggerEmitsButton(t *testing.T) {
	const src = `{
  "tools": {"lint": {"run": "lint"}},
  "nodes": {"checked": {"goal": true, "dispatch": {"inputs": {
    "environment": {"type": "choice", "options": ["staging", "prod"], "default": "staging", "description": "target env"},
    "verbose":     {"type": "boolean", "default": false}
  }}, "needs": {"lint": true}}}
}`
	st, err := ci.Parse([]byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rm, err := resolve.Resolve(st)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	out, err := Workflow(Build(rm, st, nil), Targets[TargetGHA], ci.Platform{})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{
		"  workflow_dispatch:\n    inputs:\n",
		"      environment:\n        type: choice\n        description: \"target env\"\n        default: \"staging\"\n        options: [\"staging\", \"prod\"]",
		"      verbose:\n        type: boolean\n        default: false\n",
		"  push: {}\n  pull_request: {}\n", // un-gated job keeps the broad surface
	} {
		if !strings.Contains(s, want) {
			t.Errorf("dispatch workflow missing %q\n---\n%s", want, s)
		}
	}
}

// TestScheduleTriggerEmitsCron pins the cron surface for a per-goal scheduled file: a
// goal node carrying schedule + when:[schedule] lowers to a `on: schedule`-ONLY file
// (a pure timer pipeline — the midnight-audit case), with the cron `if:` on its jobs.
// No push/PR: a scheduled goal owns its own file and must not also fire on every push.
// TestDispatchBuildArgsExposesAndOverrides pins the parametrised-build feature: a goal
// declaring `dispatch: {build-args: true}` AUTO-exposes every declared
// org.projectfile.build.args string arg as its own workflow_dispatch input (pre-filled
// with its default), and the container-build step prefers a manual-run value —
// ${{ inputs.NAME || vars.NAME || 'default' }}. A `file:` arg (per-cell digest) and a
// composed `${...}` default are NOT exposable, so neither appears on the form nor gains an
// inputs prefix. The push/PR surface stays (an un-gated goal), so the button is ADDITIVE.
func TestDispatchBuildArgsExposesAndOverrides(t *testing.T) {
	const src = `{
  "tools": {"container-build": {"action": "container-build"}},
  "nodes": {"image-built": {"goal": true, "dispatch": {"build-args": true}, "needs": {"container-build": true}}}
}`
	build := &ci.Build{
		Args: []ci.BuildInput{
			{Name: "B19_NODE_SERIES", Default: "24"},                               // literal default -> exposed, pre-filled
			{Name: "B19_LOCALES", Default: ""},                                     // empty default -> exposed, no pre-fill
			{Name: testUbuntuHash, File: ".container/deps/{B19_NODE_SERIES}.deps"}, // file arg -> NOT exposed
			{Name: "B19_BASE_IMAGE", Default: "${B19_DOCKER_REGISTRY}/b19/ubuntu"}, // composed default -> NOT exposed
		},
	}
	st, err := ci.Parse([]byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rm, err := resolve.Resolve(st)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	m := Build(rm, st, build)

	// Half 1 — the run form: each exposable arg is a typed input, defaults pre-filled;
	// non-exposable args are absent.
	out, err := Workflow(m, Targets[TargetGHA], ci.Platform{})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{
		"  workflow_dispatch:\n    inputs:\n",
		"      B19_LOCALES:\n        type: string\n", // empty default -> no `default:` line
		"      B19_NODE_SERIES:\n        type: string\n        default: \"24\"\n",
		"  push: {}\n  pull_request: {}\n", // additive: the button never removes push/PR
	} {
		if !strings.Contains(s, want) {
			t.Errorf("dispatch-build-args form missing %q\n---\n%s", want, s)
		}
	}
	// Precise: a non-exposable arg must not appear as an INPUT (`name:` + `type:`); it
	// legitimately still rides the job env / build-args, so match the form shape only.
	for _, absent := range []string{testUbuntuHash, "B19_BASE_IMAGE"} {
		if strings.Contains(s, "      "+absent+":\n        type:") {
			t.Errorf("non-exposable arg %q leaked onto the run form\n---\n%s", absent, s)
		}
	}

	// Half 2 — the build: the exposed args gain the inputs override; the composed arg does not.
	env := map[string]string{}
	for _, e := range steps(m)[testContainerBuild].Env {
		env[e.Key] = e.Value
	}
	if want := "${{ inputs.B19_NODE_SERIES || vars.B19_NODE_SERIES || '24' }}"; env["B19_NODE_SERIES"] != want {
		t.Errorf("B19_NODE_SERIES override: want %q, got %q", want, env["B19_NODE_SERIES"])
	}
	if want := "${{ inputs.B19_LOCALES || vars.B19_LOCALES }}"; env["B19_LOCALES"] != want {
		t.Errorf("B19_LOCALES override: want %q, got %q", want, env["B19_LOCALES"])
	}
	if got := env["B19_BASE_IMAGE"]; strings.Contains(got, "inputs.") {
		t.Errorf("a composed default must NOT gain an inputs prefix, got %q", got)
	}
}

// TestDispatchBuildArgsGoalScoped proves the override is GOAL-scoped: the SAME
// container-build under a goal WITHOUT the flag lowers byte-identically to today (no
// inputs prefix) — the input only exists in the file whose goal declared it.
func TestDispatchBuildArgsGoalScoped(t *testing.T) {
	const src = `{
  "tools": {"container-build": {"action": "container-build"}},
  "nodes": {"image-built": {"goal": true, "needs": {"container-build": true}}}
}`
	build := &ci.Build{Args: []ci.BuildInput{{Name: "B19_NODE_SERIES", Default: "24"}}}
	st, err := ci.Parse([]byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rm, err := resolve.Resolve(st)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	env := map[string]string{}
	for _, e := range steps(Build(rm, st, build))[testContainerBuild].Env {
		env[e.Key] = e.Value
	}
	if want := "${{ vars.B19_NODE_SERIES || '24' }}"; env["B19_NODE_SERIES"] != want {
		t.Errorf("a flagless goal must not add an inputs prefix: want %q, got %q", want, env["B19_NODE_SERIES"])
	}
}

func TestScheduleTriggerEmitsCron(t *testing.T) {
	const src = `{
  "tools": {"scan": {"run": "scan"}},
  "nodes": {
    "nightly": {"goal": true, "when": {"events": ["schedule"]}, "schedule": [{"cron": "0 3 * * *"}], "needs": {"scan": true}}
  }
}`
	st, err := ci.Parse([]byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rm, err := resolve.Resolve(st)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	out, err := Workflow(Build(rm, st, nil), Targets[TargetGHA], ci.Platform{})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.Contains(s, "name: nightly") {
		t.Errorf("the file must be named for its goal:\n%s", s)
	}
	for _, want := range []string{
		"  schedule:\n    - cron: \"0 3 * * *\"\n",
		"    if: github.event_name == 'schedule'",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("schedule workflow missing %q\n---\n%s", want, s)
		}
	}
	if strings.Contains(s, "push:") || strings.Contains(s, "pull_request") {
		t.Errorf("a scheduled goal owns a timer-only file, no push/PR:\n%s", s)
	}
}

// TestPureManualSurface pins the orthogonality rule: when EVERY job is gated on
// dispatch (no push/tag event anywhere) the `on:` surface is workflow_dispatch ONLY —
// a manual-only pipeline must not silently gain push/PR.
func TestPureManualSurface(t *testing.T) {
	const src = `{
  "tools": {"deploy": {"run": "deploy"}},
  "nodes": {"deployed": {"goal": true, "when": {"events": ["dispatch"]}, "dispatch": {}, "needs": {"deploy": true}}}
}`
	st, err := ci.Parse([]byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rm, err := resolve.Resolve(st)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	out, err := Workflow(Build(rm, st, nil), Targets[TargetGHA], ci.Platform{})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.Contains(s, "  workflow_dispatch:\n") {
		t.Errorf("a manual-only pipeline must emit workflow_dispatch:\n%s", s)
	}
	if strings.Contains(s, "push:") || strings.Contains(s, "pull_request") {
		t.Errorf("a manual-only pipeline must NOT gain push/pull_request:\n%s", s)
	}
}

// TestPerGoalSplitIsolatesFiles pins the one-file-per-goal lowering: ForGoal pins the
// subtree to a single goal, so each goal renders to its OWN workflow — named for the
// goal, carrying ONLY that goal's trigger surface and ONLY its closure's jobs. Two
// disjoint goals (a tag/dispatch publish + a cron audit) must not bleed into each other.
func TestPerGoalSplitIsolatesFiles(t *testing.T) {
	const src = `{
  "tools": {"publish": {"run": "publish"}, "audit": {"run": "audit"}},
  "nodes": {
    "published":     {"goal": true, "when": {"events": ["tag", "dispatch"]}, "dispatch": {}, "needs": {"publish": true}},
    "nightly-audit": {"goal": true, "when": {"events": ["schedule"]}, "schedule": [{"cron": "0 3 * * *"}], "needs": {"audit": true}}
  }
}`
	st, err := ci.Parse([]byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	render := func(goal string) string {
		gst := st.ForGoal(goal)
		rm, err := resolve.Resolve(gst)
		if err != nil {
			t.Fatalf("%s resolve: %v", goal, err)
		}
		out, err := Workflow(Build(rm, gst, nil), Targets[TargetGHA], ci.Platform{})
		if err != nil {
			t.Fatalf("%s render: %v", goal, err)
		}
		return string(out)
	}

	pub := render("published")
	for _, want := range []string{"name: published\n", "  workflow_dispatch:\n", "    tags: [\"**\"]", "publish"} {
		if !strings.Contains(pub, want) {
			t.Errorf("published.yaml missing %q\n---\n%s", want, pub)
		}
	}
	for _, absent := range []string{"schedule:", "cron", "audit:"} {
		if strings.Contains(pub, absent) {
			t.Errorf("published.yaml must not carry the audit goal's %q\n---\n%s", absent, pub)
		}
	}

	aud := render("nightly-audit")
	for _, want := range []string{"name: nightly-audit\n", "  schedule:\n    - cron: \"0 3 * * *\"\n", "audit"} {
		if !strings.Contains(aud, want) {
			t.Errorf("nightly-audit.yaml missing %q\n---\n%s", want, aud)
		}
	}
	for _, absent := range []string{"workflow_dispatch", "push:", "pull_request", "publish:"} {
		if strings.Contains(aud, absent) {
			t.Errorf("nightly-audit.yaml must not carry the publish goal's %q\n---\n%s", absent, aud)
		}
	}
}

// TestTriggersReject pins the parse guards: every malformed trigger field fails fast
// rather than emitting a workflow with a dropped input, an unselectable default, an
// ignored cron, a trigger on a non-goal node, or a node gated on an event no goal
// declares (a dead branch).
func TestTriggersReject(t *testing.T) {
	cases := map[string]struct{ src, want string }{
		"unknown input type":  {`{"nodes":{"n":{"goal":true,"dispatch":{"inputs":{"x":{"type":"secret"}}},"needs":{"t":true}}}}`, "unknown type"},
		"choice no options":   {`{"nodes":{"n":{"goal":true,"dispatch":{"inputs":{"x":{"type":"choice"}}},"needs":{"t":true}}}}`, "options are required"},
		"options non-choice":  {`{"nodes":{"n":{"goal":true,"dispatch":{"inputs":{"x":{"type":"string","options":["a"]}}},"needs":{"t":true}}}}`, "forbidden otherwise"},
		"bad bool default":    {`{"nodes":{"n":{"goal":true,"dispatch":{"inputs":{"x":{"type":"boolean","default":"maybe"}}},"needs":{"t":true}}}}`, "true or false"},
		"choice bad default":  {`{"nodes":{"n":{"goal":true,"dispatch":{"inputs":{"x":{"type":"choice","options":["a"],"default":"b"}}},"needs":{"t":true}}}}`, "not one of"},
		"short cron":          {`{"nodes":{"n":{"goal":true,"schedule":[{"cron":"0 3 * *"}],"needs":{"t":true}}}}`, "must have 5 fields"},
		"trigger on non-goal": {`{"nodes":{"n":{"schedule":[{"cron":"0 3 * * *"}],"needs":{"t":true}}}}`, "only valid on a goal node"},
		"dead dispatch gate":  {`{"nodes":{"n":{"when":{"events":["dispatch"]},"needs":{"t":true}}}}`, "no goal declares dispatch"},
		"dead schedule gate":  {`{"nodes":{"n":{"when":{"events":["schedule"]},"needs":{"t":true}}}}`, "no goal declares schedule"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ci.Parse([]byte(tc.src))
			if err == nil {
				t.Fatalf("%s: expected a parse error", name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("%s: error should contain %q, got: %v", name, tc.want, err)
			}
		})
	}
}

// TestCredentialEnvBinding pins the §9-B credentials overlay: a tool's neutral env
// NAME (gh-release `env: [GH_TOKEN]`) binds to the target's secret ref, SCOPED to the
// declaring job only; a declared name the overlay does not supply (GOPROXY) is skipped
// (no empty-clobber); a job that declares no env never sees the secret (no leak); and
// an empty overlay emits no secret at all (graceful degradation).
func TestCredentialEnvBinding(t *testing.T) {
	st, err := ci.Parse([]byte(`{
	  "gha": {"credentials": {"GH_TOKEN": "${{ secrets.GITHUB_TOKEN }}"}},
	  "tools": {
	    "lint":       {"run": "auto-lint"},
	    "gh-release": {"run": "gh release create", "env": ["GH_TOKEN", "GOPROXY"]}
	  },
	  "nodes": {
	    "source-is-ready": {"needs": {"lint": true}},
	    "published":       {"goal": true, "needs": {"gh-release": true, "source-is-ready": true}}
	  }
	}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rm, err := resolve.Resolve(st)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	m := Build(rm, st, nil)
	out, err := Workflow(m, Targets[TargetGHA], st.Platforms["gha"])
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.Contains(s, "GH_TOKEN: ${{ secrets.GITHUB_TOKEN }}") {
		t.Errorf("gh-release job missing bound GH_TOKEN secret:\n%s", s)
	}
	if strings.Contains(s, "GOPROXY") {
		t.Errorf("GOPROXY has no credentials entry — must NOT be emitted (clobber-safe):\n%s", s)
	}
	if n := strings.Count(s, "secrets.GITHUB_TOKEN"); n != 1 {
		t.Errorf("secret must be scoped to the one declaring job, got %d occurrences", n)
	}
	// Empty overlay (no credentials) => no secret binding at all.
	base, _ := Workflow(m, Targets[TargetGHA], ci.Platform{})
	if strings.Contains(string(base), "secrets.GITHUB_TOKEN") {
		t.Errorf("empty overlay must emit no secret binding:\n%s", base)
	}
}

// lefthookSubtree mixes the two recognised hook nodes with ordinary CI nodes, so
// the test pins BOTH that hook nodes lower to lefthook hooks AND that a normal
// node (source-is-valid, ready-to-publish) is NOT mistaken for one.
const lefthookSubtree = `{
  "tools": {"shellcheck": {"run": "make shellcheck"}},
  "nodes": {
    "source-is-valid": {"needs": {"shellcheck": true}},
    "pre-commit":      {"needs": {"source-is-valid": true}},
    "pre-push":        {"needs": {"pre-commit": true}},
    "ready-to-publish":{"goal": true, "needs": {"source-is-valid": true}}
  }
}`

func TestLefthookRendersHookNodesAsDispatchers(t *testing.T) {
	st, err := ci.Parse([]byte(lefthookSubtree))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := HookNodes(st); !reflect.DeepEqual(got, []string{"pre-commit", "pre-push"}) {
		t.Fatalf("HookNodes = %v, want [pre-commit pre-push] in HookStages order", got)
	}
	out, err := Lefthook(st, Targets[TargetLefthook])
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	// Each hook is a make-dispatcher to the node of the same name (single source
	// of truth = the DAG); the membership lives in the node, never here.
	for _, want := range []string{
		"pre-commit:\n  commands:\n    dag:\n      run: make pre-commit",
		"pre-push:\n  commands:\n    dag:\n      run: make pre-push",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("lefthook.yaml missing dispatcher block %q:\n%s", want, s)
		}
	}
	// Ordinary CI nodes are NOT hooks — only the HookStages allow-list maps.
	for _, reject := range []string{"source-is-valid:", "ready-to-publish:", testShellcheck} {
		if strings.Contains(s, reject) {
			t.Errorf("non-hook node %q leaked into lefthook.yaml:\n%s", reject, s)
		}
	}
}

// A project with no hook node renders a header-only (no-op) config rather than
// failing — graceful degradation, same posture as an empty CI subtree.
func TestLefthookNoHookNodesRendersHeaderOnly(t *testing.T) {
	st, err := ci.Parse([]byte(matrixSubtree)) // declares no pre-commit/pre-push
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := HookNodes(st); len(got) != 0 {
		t.Fatalf("HookNodes = %v, want none", got)
	}
	out, err := Lefthook(st, Targets[TargetLefthook])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "commands:") {
		t.Errorf("no hook nodes must emit no hook blocks:\n%s", out)
	}
	// REUSE-IgnoreStart
	if !strings.Contains(string(out), "SPDX-License-Identifier: MIT") {
		t.Errorf("header-only config still carries the SPDX header:\n%s", out)
	}
	// REUSE-IgnoreEnd
}

// resolveWorkflowExt is the single knob behind the committed-workflow extension:
// PF_CI_WORKFLOW_EXT. The default is the workspace standard ".yaml"; a
// ".yml" lover overrides it. A garbage value must FAIL FAST (a config typo
// producing a silently-wrong filename is the failure mode the guard exists for).
// Targets reads the value at package init, so this exercises the resolver
// directly (not the package-level workflowExtVar the map cached).
func TestResolveWorkflowExtDefaultAndOverride(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"", extYAML},      // unset => the workspace standard default
		{extYAML, extYAML}, // explicit default
		{extYML, extYML},   // the documented escape hatch
	} {
		t.Setenv(WorkflowExtEnv, tc.in)
		if got := resolveWorkflowExt(); got != tc.want {
			t.Errorf("resolveWorkflowExt(in=%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	// An unrecognised value panics (fail fast on a typo like "yaml" or ".YAML").
	t.Setenv(WorkflowExtEnv, ".YAML")
	if !didPanic(func() { resolveWorkflowExt() }) {
		t.Errorf("resolveWorkflowExt(%q) must panic on an unrecognised value", ".YAML")
	}
}

// didPanic reports whether fn panicked. Kept local — a one-off helper for the
// override test's negative case.
func didPanic(fn func()) (panicked bool) {
	defer func() { panicked = recover() != nil }()
	fn()
	return
}

// eventsSubtree is a minimal opted-in project: one goal node, one tool. The events
// wiring is orthogonal to the DAG shape, so a tiny graph exercises the notify job.
const eventsSubtree = `{
  "tools": {
    "shellcheck": {}
  },
  "nodes": {
    "source-is-valid": {"goal": true, "needs": {"shellcheck": true}}
  }
}`

// TestEmitJobRenders proves the events model's forge half: an opted-in project grows a
// `notify` job that POSTs the neutral envelope, gated on the webhook var, needing every
// real job; a project that did NOT opt in grows nothing (secure/quiet default).
func TestEmitJobRenders(t *testing.T) {
	st, err := ci.Parse([]byte(eventsSubtree))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rm, err := resolve.Resolve(st)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	// Opted OUT (nil Build, or a Build with no Events): no notify job at all.
	outOff, err := Workflow(Build(rm, st, nil), Targets[TargetGHA], ci.Platform{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(outOff), "notify:") || strings.Contains(string(outOff), "emit lifecycle event") {
		t.Errorf("no events declared must render NO notify job:\n%s", outOff)
	}

	// Opted IN: events subtree present -> a notify job.
	b := &ci.Build{Events: &ci.Events{WebhookVar: ci.DefaultWebhookVar}}
	m := Build(rm, st, b)
	out, err := Workflow(m, Targets[TargetGHA], ci.Platform{})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	t.Logf("rendered workflow:\n%s", s)

	// The job exists, gated on always()+the webhook var, needing the real job.
	for _, want := range []string{
		"\n  notify:\n",
		"if: ${{ always() && vars.EVENTS_WEBHOOK_URL != '' }}",
		"needs: [\"source-is-valid\"]",
		"- name: emit lifecycle event",
		"run: |",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("notify job missing %q\ngot:\n%s", want, s)
		}
	}

	// The envelope is field-identical to the m6e emit script's contract, in order.
	env := `printf '{"event":"%s","project":"%s","goal":"%s","status":"%s","url":"%s","ts":"%s","payload":{}}'`
	if !strings.Contains(s, env) {
		t.Errorf("envelope shape/order must match the m6e contract\nwant substring: %s\ngot:\n%s", env, s)
	}
	// Success/failure is computed from the aggregated needs results, and the run is
	// fail-soft so a broken sink never reds the workflow.
	for _, want := range []string{
		"contains(needs.*.result, 'failure')",
		"event=goal.succeeded",
		"status=failure; event=goal.failed",
		`|| true`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("emit run missing %q\ngot:\n%s", want, s)
		}
	}

	// The goal name is stamped into the envelope (the workflow is named for its goal).
	if !strings.Contains(s, `"source-is-valid"`) {
		t.Errorf("envelope should stamp the goal name source-is-valid\ngot:\n%s", s)
	}
}

// publishSubtree is an opted-in matrixed publish: two series, a build PRODUCER and the
// push CONSUMER that declares `emit:`. Two cells prove the per-cell emission.
const publishSubtree = `{
  "image": "b19/ubuntu-{B19_UBUNTU_SERIES}",
  "matrix": {"axes": {"B19_UBUNTU_SERIES": ["resolute", "noble"]}},
  "tools": {
    "container-build": {"action": "container-build"},
    "oci-push": {"action": "oci-push", "emit": "ci.image.published"}
  },
  "nodes": {
    "image-built": {"matrix": true, "needs": {"container-build": true}},
    "published": {"matrix": true, "goal": true, "needs": {"image-built": true, "oci-push": true}}
  }
}`

// TestToolEmitStepRenders proves the events model's FACT half: a tool declaring `emit:`
// in an opted-in project grows one webhook step after its own, carrying what it
// published — image, tag cascade, digest and cell. Without the events subtree it grows
// nothing, so the tool key alone never opens a network call.
func TestToolEmitStepRenders(t *testing.T) {
	st, err := ci.Parse([]byte(publishSubtree))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rm, err := resolve.Resolve(st)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	// Opted OUT: the manifest names an event, but no org.projectfile.events => no step.
	outOff, err := Workflow(Build(rm, st, &ci.Build{}), Targets[TargetForgejo], ci.Platform{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(outOff), "emit ci.image.published") {
		t.Errorf("emit: without an events subtree must render NO step:\n%s", outOff)
	}

	// Opted IN.
	b := &ci.Build{Events: &ci.Events{WebhookVar: ci.DefaultWebhookVar}}
	out, err := Workflow(Build(rm, st, b), Targets[TargetForgejo], ci.Platform{})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	t.Logf("rendered workflow:\n%s", s)

	for _, want := range []string{
		"- name: emit ci.image.published",
		// Gated on the sink being configured, the push having succeeded, AND the ref
		// being a tag: a downstream router turns this fact into a rebuild dispatch for
		// every consumer, and a preview base is what they must NOT be rebuilt against.
		"if: ${{ success() && vars.EVENTS_WEBHOOK_URL != '' && github.ref_type == 'tag' }}",
		// The cell rides step env; toJSON is multi-line, so it must not be inlined.
		"M6E_EVENT_CELL: ${{ toJSON(matrix) }}",
		// Image and digest are read back from what the action verified, never recomposed.
		`ref=$(cat "${M6E_DIGEST_FILE:-image.digest}")`,
		`"$(cat "${M6E_TAGS_FILE:-image.tags}")"`,
		`"${ref%@*}"`,
		`"${ref#*@}"`,
		// The goal the workflow is named for, stamped like the notify job stamps it.
		`"published"`,
		// Fail-soft, and loud about a missing curl rather than silent under || true.
		"webhook degraded — curl absent",
		"|| true",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("emit step missing %q\ngot:\n%s", want, s)
		}
	}

	// The envelope keeps the frozen field order and fills the reserved payload.
	env := `printf '{"event":"%s","project":"%s","goal":"%s","status":"success","url":"%s","ts":"%s",` +
		`"payload":{"image":"%s","tags":%s,"digest":"%s","cell":%s}}'`
	if !strings.Contains(s, env) {
		t.Errorf("payload shape/order must extend the frozen envelope\nwant substring: %s\ngot:\n%s", env, s)
	}

	// ONE step per publish job, and the matrix gives one job per series — so a matrixed
	// publish emits one event per cell, which is what makes a per-series rebuild possible.
	if got := strings.Count(s, "- name: emit ci.image.published"); got != 1 {
		t.Errorf("want exactly one emit step in the matrixed publish job, got %d\n%s", got, s)
	}
	if !strings.Contains(s, "B19_UBUNTU_SERIES: [\"resolute\", \"noble\"]") {
		t.Errorf("the publish job should stay matrixed (one job per cell)\ngot:\n%s", s)
	}
}

// TestToolEmitFollowsItsTool pins WHERE the fact step lands: directly after the tool that
// produced the fact, not at the end of the job. `emit:` is a TOOL key, so it must fire
// with that tool — and a fused publish group (sign/attest after the push) must not move
// the report of an image that is already published.
func TestToolEmitFollowsItsTool(t *testing.T) {
	st, _ := ci.Parse([]byte(publishSubtree))
	rm, _ := resolve.Resolve(st)
	b := &ci.Build{Events: &ci.Events{WebhookVar: ci.DefaultWebhookVar}}
	out, err := Workflow(Build(rm, st, b), Targets[TargetForgejo], ci.Platform{})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	push := strings.Index(s, "- name: oci-push")
	emit := strings.Index(s, "- name: emit ci.image.published")
	if push < 0 || emit < 0 || emit < push {
		t.Errorf("the emit step must follow its tool (push=%d emit=%d)\ngot:\n%s", push, emit, s)
	}
}

// TestEmitJobIsTargetAgnostic proves the webhook curl is universal — GHA and Forgejo
// render the SAME notify step (the whole point of choosing a generic webhook).
func TestEmitJobIsTargetAgnostic(t *testing.T) {
	st, _ := ci.Parse([]byte(eventsSubtree))
	rm, _ := resolve.Resolve(st)
	m := Build(rm, st, &ci.Build{Events: &ci.Events{WebhookVar: ci.DefaultWebhookVar}})

	gha, err := Workflow(m, Targets[TargetGHA], ci.Platform{})
	if err != nil {
		t.Fatal(err)
	}
	forgejo, err := Workflow(m, Targets[TargetForgejo], ci.Platform{})
	if err != nil {
		t.Fatal(err)
	}
	emitBlock := func(b []byte) string {
		s := string(b)
		i := strings.Index(s, "- name: emit lifecycle event")
		if i < 0 {
			t.Fatalf("no emit step:\n%s", s)
		}
		return s[i:]
	}
	if emitBlock(gha) != emitBlock(forgejo) {
		t.Errorf("emit step must be target-agnostic\nGHA:\n%s\nForgejo:\n%s", emitBlock(gha), emitBlock(forgejo))
	}
}

// supplyChainSubtree mirrors the b19 opt-in supply-chain publish policy
// (m6e/b19/{tools,goals}/supplychain.yaml): container-build produces the OCI tar;
// oci-push carries `fuse: publish`; cosign-sign/attest join that fuse; syft-sbom-tar is
// a DECOUPLED producer (`artifact:`) whose SBOM the fused attest step consumes. It pins
// the two load-bearing laws the design leans on — the fused publish order spine and the
// artifact hand-off into a fused consumer — with NO leaf-name special-casing.
const supplyChainSubtree = `{
  "image": "b19/ubuntu",
  "tools": {
    "container-build": {"action": "container-build"},
    "oci-push":        {"action": "oci-push", "fuse": "publish", "env": ["REGISTRY_USERNAME", "REGISTRY_PASSWORD"]},
    "syft-sbom-tar":   {"image": "d9t/go-tools", "run": "auto-syft image", "artifact": "reports"},
    "cosign-sign":     {"image": "d9t/go-tools", "run": "auto-cosign sign", "fuse": "publish", "env": ["COSIGN_PRIVATE_KEY", "COSIGN_PASSWORD"]},
    "cosign-attest":   {"image": "d9t/go-tools", "run": "auto-cosign attest", "fuse": "publish", "env": ["COSIGN_PRIVATE_KEY", "COSIGN_PASSWORD"]}
  },
  "nodes": {
    "image-built":       {"needs": {"container-build": true}},
    "publish-image":     {"needs": {"image-built": true, "oci-push": true}},
    "image-has-sbom":    {"needs": {"image-built": true, "syft-sbom-tar": true}},
    "image-is-signed":   {"needs": {"publish-image": true, "cosign-sign": true}},
    "image-is-attested": {"needs": {"image-is-signed": true, "image-has-sbom": true, "cosign-attest": true}},
    "published":         {"goal": true, "needs": {"image-is-attested": true}}
  }
}`

// TestSupplyChainPublishFuse pins that oci-push + cosign-sign + cosign-attest COLLAPSE
// into one job at the sink (image-is-attested), rendered as ORDERED steps push→sign→
// attest (so cosign never signs before the push nor attests before it signs), that the
// SBOM producer's artifact is DOWNLOADED into that fused job (the predicate hand-off),
// and that the COSIGN_* credential names ride the sign/attest steps.
func TestSupplyChainPublishFuse(t *testing.T) {
	st, err := ci.Parse([]byte(supplyChainSubtree))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rm, err := resolve.Resolve(st)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	m := Build(rm, st, nil)
	by := map[string]JobView{}
	for _, j := range m.Jobs {
		by[j.Name] = j
	}

	// The three `publish`-fuse leaves collapse to the sink's node-job; the intermediate
	// spine node (image-is-signed) becomes a gate.
	sink, ok := by["image-is-attested"]
	if !ok {
		t.Fatalf("the fused publish job must be the sink node image-is-attested; jobs: %+v", m.Jobs)
	}
	if !by["image-is-signed"].IsGate {
		t.Errorf("the absorbed sign node (image-is-signed) must become an echo gate: %+v", by["image-is-signed"])
	}

	// Ordered steps: oci-push (the push), then cosign-sign, then cosign-attest (sink).
	var order []string
	for _, s := range sink.Steps {
		order = append(order, s.Name)
	}
	want := []string{testOCIPush, "cosign-sign", "cosign-attest"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("fused publish steps must be %v (push→sign→attest), got %v", want, order)
	}

	// The SBOM producer (syft-sbom-tar) stays its OWN job, and the fused attest job pulls
	// its artifact — the CycloneDX predicate hand-off into a fused consumer.
	if jobOf(m, "cosign-attest").Name != "image-is-attested" {
		t.Errorf("cosign-attest must be absorbed into image-is-attested, got %q", jobOf(m, "cosign-attest").Name)
	}
	var gotSBOM bool
	for _, d := range sink.Downloads {
		if d.Name == "syft-sbom-tar"+artifactScopeSuffix {
			gotSBOM = true
		}
	}
	if !gotSBOM {
		t.Errorf("fused publish job must download the syft-sbom-tar artifact (attest predicate), got downloads %+v", sink.Downloads)
	}

	// COSIGN_* credential names ride the sign + attest steps (scoped by `env:`), the
	// key-material surface the credentials overlay binds per job.
	for _, name := range []string{"cosign-sign", "cosign-attest"} {
		var step StepView
		for _, s := range sink.Steps {
			if s.Name == name {
				step = s
			}
		}
		if !contains(step.EnvNames, "COSIGN_PRIVATE_KEY") || !contains(step.EnvNames, "COSIGN_PASSWORD") {
			t.Errorf("%s must forward the COSIGN_* credential names, got %v", name, step.EnvNames)
		}
	}
}

// TestPublishFuseNeverEntersTheImageStore pins the boundary that keeps the publish job
// off the runner's SHARED containers-storage graphroot: `docker load` and the synthetic
// secrets-provision step belong to the LIVE fuse group ALONE, because only its compose
// stack runs the image and reads a `.secrets/` tree. Every publish member acts elsewhere
// — skopeo copies the archive straight to the registry, cosign signs the pushed digest —
// so a load there enters an image no step reads, and the docker-cleanup reap paired with
// it then deletes layers underneath every concurrent job: containers/storage fails the
// blob-reuse path with `layer for blob … not found`, which podman reports as the
// misleading "payload does not match any of the supported image formats" (exit 125). The
// archive itself must still DOWNLOAD — oci-push reads it as a file.
func TestPublishFuseNeverEntersTheImageStore(t *testing.T) {
	st, err := ci.Parse([]byte(supplyChainSubtree))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rm, err := resolve.Resolve(st)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	// secretsBuild carries a declared org.projectfile.ci.secrets subtree, so a publish job
	// that still tested `fuse != ""` would inject the provision step here too.
	m := Build(rm, st, secretsBuild)
	sink := jobOf(m, testOCIPush)
	if sink.Load || sink.Stem != "" || sink.RmiImage != "" || sink.RmNetwork != "" {
		t.Errorf("a publish-fuse job must not enter the image store: Load=%v Stem=%q RmiImage=%q RmNetwork=%q",
			sink.Load, sink.Stem, sink.RmiImage, sink.RmNetwork)
	}
	for _, s := range sink.Steps {
		if s.Action == ActionSecretsProvision {
			t.Errorf("a publish-fuse job runs no compose stack, so it must not provision secrets; steps: %+v", sink.Steps)
		}
	}
	var gotArchive bool
	for _, d := range sink.Downloads {
		if d.Name == "image"+artifactScopeSuffix {
			gotArchive = true
		}
	}
	if !gotArchive {
		t.Errorf("the publish job must still DOWNLOAD the build archive (oci-push reads the tar), got %+v", sink.Downloads)
	}
}

// TestSupplyChainSingletonNoOp pins the OPT-IN safety property: with oci-push the LONE
// `fuse: publish` tool (no cosign tools opted in), the singleton group is NEVER fused —
// oci-push stays exactly where it renders today (a step under publish-image, not
// absorbed elsewhere). This is why adding `fuse: publish` to oci-push is a no-op until a
// project includes b19/supplychain.yaml.
func TestSupplyChainSingletonNoOp(t *testing.T) {
	// The same subtree minus every cosign/syft tool and its wiring: oci-push alone carries
	// the fuse group.
	const soloSubtree = `{
	  "image": "b19/ubuntu",
	  "tools": {
	    "container-build": {"action": "container-build"},
	    "oci-push":        {"action": "oci-push", "fuse": "publish", "env": ["REGISTRY_USERNAME", "REGISTRY_PASSWORD"]}
	  },
	  "nodes": {
	    "image-built":   {"needs": {"container-build": true}},
	    "publish-image": {"goal": true, "needs": {"image-built": true, "oci-push": true}}
	  }
	}`
	st, err := ci.Parse([]byte(soloSubtree))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rm, err := resolve.Resolve(st)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	m := Build(rm, st, nil)

	// oci-push must remain hosted by its own node (publish-image), NOT absorbed into some
	// fused sink — the singleton group is skipped.
	if host := jobOf(m, testOCIPush).Name; host != "publish-image" {
		t.Errorf("a lone fuse:publish oci-push must stay under publish-image (no fusing), got host %q", host)
	}
}

// TestWorkflowEnvLowered proves org.projectfile.ci.env renders as a top-level
// workflow `env:` block every job inherits, with each value make->forge lowered the
// SAME way job env is: a ${REGISTRY} ref rides a bare ${{ vars.· }} expression and the
// tag var keeps its 'latest' fallback. This is how the compose plane's ${D9T_DIND_IMAGE}
// reaches a fused live dc-up-d AND dc-down with no per-tool duplication.
func TestWorkflowEnvLowered(t *testing.T) {
	const src = `{
	  "image": "d9t/opencode",
	  "env": {"D9T_DIND_IMAGE": "${SOURCE_DOCKER_REGISTRY}/d9t/dind:${M6E_BASE_IMAGE_DEFAULT_VERSION}"},
	  "tools": {"build": {"run": "echo"}},
	  "nodes": {"ready": {"goal": true, "needs": {"build": true}}}
	}`
	st, err := ci.Parse([]byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rm, err := resolve.Resolve(st)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	out, err := Workflow(Build(rm, st, nil), Targets[TargetGHA], ci.Platform{})
	if err != nil {
		t.Fatalf("workflow: %v", err)
	}
	s := string(out)
	// Top-level (not per-job) env block.
	if !strings.Contains(s, "\nenv:\n") {
		t.Errorf("missing top-level env: block\n---\n%s", s)
	}
	// Registry ref -> bare vars; tag var -> the FULL tag expression, not the M6E_TAG env
	// indirection lowerMakeExpr emits: a workflow `env:` value cannot read the env context
	// it is itself defining (inlineHoists expands it back at this one site).
	const want = "  D9T_DIND_IMAGE: ${{ vars.SOURCE_DOCKER_REGISTRY }}/d9t/dind:${{ vars.BASE_IMAGE_DEFAULT_VERSION || 'latest' }}"
	if !strings.Contains(s, want) {
		t.Errorf("workflow env not lowered as expected, want %q\n---\n%s", want, s)
	}
}

// TestEnvBlocksNeverReadEnvContext guards the ONE rule the resolverEnv hoists can break:
// `${{ env.* }}` is ILLEGAL inside a workflow-level or job-level `env:` block. Both are
// evaluated before the env context exists, so a forge does not resolve it to an empty
// string — it REJECTS the file (Forgejo: "Unknown Variable Access env" + "the workflow
// file is not usable"; GHA: "Unrecognized named-value: 'env'"), taking every job with it.
// The three producers of env-block values are covered: an authored workflow env carrying
// the tag var, a build-arg FROM ref (hoisted into job env beside the step), and the fused
// live contract vars (tag + run scope). Structure-driven — the rendered YAML is parsed,
// so the guard cannot be fooled by indentation or key order.
func TestEnvBlocksNeverReadEnvContext(t *testing.T) {
	const authoredEnvSubtree = `{
  "image": "d9t/opencode",
  "env": {"D9T_DIND_IMAGE": "${SOURCE_DOCKER_REGISTRY}/d9t/dind:${M6E_BASE_IMAGE_DEFAULT_VERSION}"},
  "tools": {"build": {"run": "echo"}},
  "nodes": {"ready": {"goal": true, "needs": {"build": true}}}
}`
	baseImageBuild := &ci.Build{Args: []ci.BuildInput{
		{Name: testUbuntuBaseImage, Default: "${B19_DOCKER_REGISTRY}/b19/ubuntu:${M6E_BASE_IMAGE_DEFAULT_VERSION}"},
	}}
	cases := []struct {
		name    string
		subtree string
		build   *ci.Build
	}{
		{"authored-workflow-env", authoredEnvSubtree, nil},
		{"build-arg-from-ref", liveSubtree, baseImageBuild},
		{"fused-live-contract-vars", liveSubtree, nil},
	}
	for _, c := range cases {
		st, err := ci.Parse([]byte(c.subtree))
		if err != nil {
			t.Fatalf("%s: parse: %v", c.name, err)
		}
		rm, err := resolve.Resolve(st)
		if err != nil {
			t.Fatalf("%s: resolve: %v", c.name, err)
		}
		m := Build(rm, st, c.build)
		for _, key := range []string{TargetGHA, TargetForgejo} { // the workflow lowerings (lefthook is a hook file)
			out, err := Workflow(m, Targets[key], ci.Platform{})
			if err != nil {
				t.Fatalf("%s/%s: workflow: %v", c.name, key, err)
			}
			var wf struct {
				Env  map[string]string `yaml:"env"`
				Jobs map[string]struct {
					Env map[string]string `yaml:"env"`
				} `yaml:"jobs"`
			}
			if err := yaml.Unmarshal(out, &wf); err != nil {
				t.Fatalf("%s/%s: rendered workflow is not valid YAML: %v\n%s", c.name, key, err, out)
			}
			blocks := map[string]map[string]string{"workflow": wf.Env}
			for job, j := range wf.Jobs {
				blocks["job "+job] = j.Env
			}
			for where, env := range blocks {
				for name, value := range env {
					if strings.Contains(value, "${{ env.") {
						t.Errorf("%s/%s: %s env %s=%q reads the env context — a forge rejects the whole file\n%s",
							c.name, key, where, name, value, out)
					}
				}
			}
		}
	}
}

// TestRunToolNetwork locks the `network: live` -> run-tool network input mapping: the
// symbolic `live` becomes the live compose stack's own ${M6E_COMPOSE_PROJECT_NAME}-network
// (companion of `fuse: live`), empty stays the default bridge, and any other value passes
// through verbatim as an explicit network name.
func TestRunToolNetwork(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"live", "${{ env.M6E_COMPOSE_PROJECT_NAME }}-network"},
		{"my-net", "my-net"},
	}
	for _, c := range cases {
		if got := (StepView{Network: c.in}).RunToolNetwork(); got != c.want {
			t.Errorf("RunToolNetwork(%q): got %q, want %q", c.in, got, c.want)
		}
	}
}

// TestSelfImageToolStep locks the `image: M6E_IMAGE_FULLNAME` lowering: the project's own
// built image is a JOB-env contract var, not a ci.images entry, so it must read off `env.`
// — as an orphan `vars.` name it resolves to empty and run-tool runs no image at all. It
// also pins `pull: never`, because that image only ever reaches the runner via `docker load`
// of the build artifact and a fetch of the run-scoped tag can only fail.
func TestSelfImageToolStep(t *testing.T) {
	st := &ci.Subtree{Tools: map[string]ci.Manifest{
		"astro-build": {Image: ImageFullnameEnv, Run: "build-static", Network: liveNetwork},
	}}
	step := toolStep(resolve.Job{Name: "astro-build"}, st, &ci.Build{}, nil)

	if !step.SelfImage {
		t.Error("SelfImage not set for an image: M6E_IMAGE_FULLNAME tool")
	}
	if got, want := step.RunToolImage(), "${{ env."+ImageFullnameEnv+" }}"; got != want {
		t.Errorf("RunToolImage: got %q, want %q", got, want)
	}
	if got := step.RunToolPull(); got != "never" {
		t.Errorf("RunToolPull: got %q, want %q", got, "never")
	}
	// A ci.images var name keeps the registry-path lowering and its own pull policy.
	other := toolStep(resolve.Job{Name: "t"},
		&ci.Subtree{Tools: map[string]ci.Manifest{"t": {Image: "NODE_TOOL_IMAGE"}}}, &ci.Build{}, nil)
	if other.SelfImage {
		t.Error("SelfImage set for an ordinary ci.images var name")
	}
}

// TestPublishFansOutOverDestinations pins the destination AXIS: a route naming two
// sinks renders TWO publish cells, not one job looping both, so a registry refusing a
// push fails its own cell. The axis multiplies the build axes rather than replacing
// them, and the archive stays keyed by the BUILD cell — one build is published to
// every destination, so a sink in the artifact name would name a file no build
// ever uploaded.
func TestPublishFansOutOverDestinations(t *testing.T) {
	st, err := ci.Parse([]byte(publishSubtree))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rm, err := resolve.Resolve(st)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	b := &ci.Build{PublishRefs: map[string][]ci.SinkRef{
		ci.LoweringGHA: {
			{Sink: "ghcr", Ref: "ghcr.io/damian-buho/b19/ubuntu-{B19_UBUNTU_SERIES}:latest"},
			{Sink: "hub-main", Ref: "docker.io/damianbuho/b19-ubuntu-{B19_UBUNTU_SERIES}:latest"},
		},
		ci.LoweringForgejo: {{Sink: "kiota", Ref: "kiota.ch/b19/ubuntu-{B19_UBUNTU_SERIES}:latest"}},
	}}
	out, err := Workflow(Build(rm, st, b), Targets[TargetGHA], ci.Platform{})
	if err != nil {
		t.Fatalf("workflow: %v", err)
	}
	s := string(out)
	for _, want := range []string{
		`M6E_PUBLISH_SINK: ["ghcr", "hub-main"]`,                // one cell per destination
		`B19_UBUNTU_SERIES: ["resolute", "noble"]`,              // the build axis SURVIVES
		"sink: ${{ matrix.M6E_PUBLISH_SINK }}",                  // the cell names the one it owns
		"artifact-name: image-${{ matrix.B19_UBUNTU_SERIES }}-", // archive keyed by the BUILD cell alone
	} {
		if !strings.Contains(s, want) {
			t.Errorf("publish job missing %q\n---\n%s", want, s)
		}
	}
	if strings.Contains(s, "image-${{ matrix.M6E_PUBLISH_SINK }}") {
		t.Errorf("the destination axis must not reach artifact naming\n---\n%s", s)
	}
}

// TestPublishCellsAreScopedToTheirLowering pins the half that makes the axis honest:
// a route is a fact about a FORGE, so a GitHub pipeline fans over GitHub's
// destinations and a kiota one over kiota's. One StepView is rendered once per
// target, so this also catches a binding leaking from the previous render.
func TestPublishCellsAreScopedToTheirLowering(t *testing.T) {
	st, err := ci.Parse([]byte(publishSubtree))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rm, err := resolve.Resolve(st)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	m := Build(rm, st, &ci.Build{PublishRefs: map[string][]ci.SinkRef{
		ci.LoweringGHA: {{Sink: "ghcr", Ref: "ghcr.io/damian-buho/p:latest"}},
	}})
	gha, err := Workflow(m, Targets[TargetGHA], ci.Platform{})
	if err != nil {
		t.Fatalf("gha: %v", err)
	}
	if !strings.Contains(string(gha), `M6E_PUBLISH_SINK: ["ghcr"]`) {
		t.Errorf("gha lowering missing its own destination\n---\n%s", gha)
	}
	// Rendered SECOND, from the same model: forgejo declares no route, so it must keep
	// the single-job fan-out — no axis, and no `sink:` left over from the GHA pass.
	forgejo, err := Workflow(m, Targets[TargetForgejo], ci.Platform{})
	if err != nil {
		t.Fatalf("forgejo: %v", err)
	}
	for _, gone := range []string{"M6E_PUBLISH_SINK", "sink:"} {
		if strings.Contains(string(forgejo), gone) {
			t.Errorf("forgejo declares no route but carries %q\n---\n%s", gone, forgejo)
		}
	}
}

// TestJobIfNeverReadsTheMatrixContext pins the forge rule that broke the whole fleet
// once: a JOB-level `if:` may read only github / needs / vars / inputs, and Forgejo
// rejects the ENTIRE workflow file — every job in it — when one names a matrix axis.
// Any per-cell gate therefore belongs on a step. Asserted on the rendered YAML rather
// than on a helper, because the hazard is what reaches the forge.
func TestJobIfNeverReadsTheMatrixContext(t *testing.T) {
	st, err := ci.Parse([]byte(publishSubtree))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rm, err := resolve.Resolve(st)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	// Both lowerings route, so both render the publish cell whose gate is the hazard.
	m := Build(rm, st, &ci.Build{PublishRefs: map[string][]ci.SinkRef{
		ci.LoweringGHA:     {{Sink: "one", Ref: "one.example/p:latest"}},
		ci.LoweringForgejo: {{Sink: "two", Ref: "two.example/p:latest"}},
	}})
	for _, key := range []string{TargetGHA, TargetForgejo} {
		out, err := Workflow(m, Targets[key], ci.Platform{})
		if err != nil {
			t.Fatalf("%s: %v", key, err)
		}
		// Job keys sit at 4 spaces, step keys at 8 — indentation is what separates the
		// two `if:` scopes in the rendered file.
		for _, line := range strings.Split(string(out), "\n") {
			if strings.HasPrefix(line, "    if:") && strings.Contains(line, "matrix.") {
				t.Errorf("%s: job-level if reads matrix: %s", key, line)
			}
		}
	}
	// The `preview` gate is the newest job-level predicate, and the longest — sweep it
	// under the same rule rather than trusting that it reads only `github`.
	pst, err := ci.Parse([]byte(previewSubtree))
	if err != nil {
		t.Fatalf("preview parse: %v", err)
	}
	prm, err := resolve.Resolve(pst)
	if err != nil {
		t.Fatalf("preview resolve: %v", err)
	}
	for _, key := range []string{TargetGHA, TargetForgejo} {
		out, err := Workflow(Build(prm, pst, nil), Targets[key], ci.Platform{})
		if err != nil {
			t.Fatalf("preview %s: %v", key, err)
		}
		for _, line := range strings.Split(string(out), "\n") {
			if strings.HasPrefix(line, "    if:") && strings.Contains(line, "matrix.") {
				t.Errorf("preview %s: job-level if reads matrix: %s", key, line)
			}
		}
	}
}

// TestPublishCellsCarryTheRunTimeSinkGate pins the forge-side switch: a publish cell
// gates on CI_PUBLISH_SINKS, so withholding a destination stays a per-cell skip and
// never widens what the job itself runs on. A lowering that declares no route gains no
// gate, because it has no cell to withhold.
func TestPublishCellsCarryTheRunTimeSinkGate(t *testing.T) {
	st, err := ci.Parse([]byte(publishSubtree))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rm, err := resolve.Resolve(st)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	const sink = "ghcr"
	m := Build(rm, st, &ci.Build{PublishRefs: map[string][]ci.SinkRef{
		ci.LoweringGHA: {{Sink: sink, Ref: "ghcr.io/damian-buho/p:latest"}},
	}})
	gha, err := Workflow(m, Targets[TargetGHA], ci.Platform{})
	if err != nil {
		t.Fatalf("gha: %v", err)
	}
	// Comma-wrapped on both sides: the whole-name match is the point of the gate, so a
	// bare contains() regressing here must fail the test rather than the fleet.
	want := "contains(format(',{0},', vars.CI_PUBLISH_SINKS), format(',{0},', matrix.M6E_PUBLISH_SINK))"
	if !strings.Contains(string(gha), want) {
		t.Errorf("publish cell missing the run-time sink gate\n---\n%s", gha)
	}
	if !strings.Contains(string(gha), "vars.CI_PUBLISH_SINKS == ''") {
		t.Errorf("unset CI_PUBLISH_SINKS must publish everywhere\n---\n%s", gha)
	}
	forgejo, err := Workflow(m, Targets[TargetForgejo], ci.Platform{})
	if err != nil {
		t.Fatalf("forgejo: %v", err)
	}
	if strings.Contains(string(forgejo), PublishSinksVar) {
		t.Errorf("forgejo declares no route but carries the sink gate\n---\n%s", forgejo)
	}
}

// TestReleaseFansOutOverForges pins the binaries half of the destination axis: a
// route naming two forges renders one release CELL per forge, each carrying that
// forge's own URL and its own repository path — the two differ per forge, which is
// why the coordinates ride matrix include rows rather than being derived from the
// axis. The cell also names its destination, which is what selects <SINK>_TOKEN, so
// a foreign forge's credential never reaches the cell releasing on the origin.
func TestReleaseFansOutOverForges(t *testing.T) {
	st, err := ci.Parse([]byte(releaseSubtree))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	man := st.Tools["forgejo-release"]
	man.ReleaseAssetPath = testReleaseAsset
	st.Tools["forgejo-release"] = man
	rm, err := resolve.Resolve(st)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	b := &ci.Build{ReleaseTargets: map[string][]ci.ReleaseTarget{
		ci.LoweringForgejo: {
			{Sink: "kiota", URL: "https://kiota.ch", Repo: "projectfile/bridge"},
			{Sink: "codeberg", URL: "https://codeberg.org", Repo: "damian-buho/projectfile-bridge"},
		},
	}}
	out, err := Workflow(Build(rm, st, b), Targets[TargetForgejo], ci.Platform{})
	if err != nil {
		t.Fatalf("workflow: %v", err)
	}
	s := string(out)
	for _, want := range []string{
		`M6E_PUBLISH_SINK: ["kiota", "codeberg"]`,            // one cell per forge
		`GOOS: ["linux"]`,                                    // the build axes SURVIVE
		`M6E_PUBLISH_SINK: "codeberg"`,                       // include row, matched on the axis
		`M6E_RELEASE_REPO: "damian-buho/projectfile-bridge"`, // the name differs per forge
		`M6E_RELEASE_URL: "https://codeberg.org"`,
		"server-url: ${{ matrix.M6E_RELEASE_URL }}",
		"repo: ${{ matrix.M6E_RELEASE_REPO }}",
		"sink: ${{ matrix.M6E_PUBLISH_SINK }}",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("release job missing %q\n---\n%s", want, s)
		}
	}
}

// TestReleaseWithoutRouteKeepsAmbientForge pins the no-op half, which is what every
// fleet project relies on today: declaring no `release` route renders no axis and no
// coordinates, so the action falls back to the Forgejo context it runs in.
func TestReleaseWithoutRouteKeepsAmbientForge(t *testing.T) {
	st, err := ci.Parse([]byte(releaseSubtree))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rm, err := resolve.Resolve(st)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	out, err := Workflow(Build(rm, st, &ci.Build{}), Targets[TargetForgejo], ci.Platform{})
	if err != nil {
		t.Fatalf("workflow: %v", err)
	}
	for _, gone := range []string{"M6E_PUBLISH_SINK", "server-url:", "repo:", "sink:"} {
		if strings.Contains(string(out), gone) {
			t.Errorf("no release route declared but workflow carries %q\n---\n%s", gone, out)
		}
	}
}

// archSubtree is the real publish shape: the BUILD fans over M6E_ARCH (one single-image
// archive per architecture) while the PUBLISH node drops the axis, so one cell holds every
// architecture's tar and publishes them together as a manifest list. The axis is AUTHORED
// here rather than minted because ci.Parse takes a subtree, not a projectfile — the
// minting from org.projectfile.architecture is pinned in internal/ci.
const archSubtree = `{
  "image": "b19/ubuntu",
  "matrix": {"axes": {"M6E_ARCH": ["amd64", "arm64", "riscv64"]}},
  "tools": {"container-build": {"action": "container-build"}, "oci-push": {"action": "oci-push"}},
  "nodes": {
    "image-built": {"matrix": true, "needs": {"container-build": true}},
    "published": {"matrix": {"without": ["M6E_ARCH"]}, "goal": true, "needs": {"image-built": true, "oci-push": true}}
  }
}`

// TestArchAxisBindsBuildPlatform pins the producer end: each cell must actually BUILD its
// architecture. Without `platform:` every cell emits the host arch and the index that
// follows is a well-formed list of three identical images — a failure nothing downstream
// can see, because each artifact is individually correct.
func TestArchAxisBindsBuildPlatform(t *testing.T) {
	st, err := ci.Parse([]byte(archSubtree))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rm, err := resolve.Resolve(st)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	m := Build(rm, st, nil)
	want := "${{ matrix." + ci.ArchAxis + " }}"
	if got := steps(m)[testContainerBuild].Arch; got != want {
		t.Errorf("%s Arch: want %q, got %q", testContainerBuild, want, got)
	}
	out, err := Workflow(m, Targets[TargetGHA], ci.Platform{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "          platform: "+want) {
		t.Errorf("rendered workflow missing the per-cell build platform:\n%s", out)
	}
}

// TestPublishNodeTakesEveryArchArchive pins the consumer end and the reason this design
// exists. The publish node dropped the arch axis, so its own cell-keyed stem names an
// artifact nobody uploaded; it must instead name each producer cell's tar and hand all of
// them to oci-push at once. The tags it publishes carry NO architecture suffix — that is
// the whole point, because a registry that cannot delete a tag keeps such scaffolding
// forever.
func TestPublishNodeTakesEveryArchArchive(t *testing.T) {
	st, err := ci.Parse([]byte(archSubtree))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rm, err := resolve.Resolve(st)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	m := Build(rm, st, nil)
	got := steps(m)[testOCIPush].Archives
	if len(got) != 3 {
		t.Fatalf("%s Archives: want one per declared arch, got %v", testOCIPush, got)
	}
	for i, arch := range []string{"amd64", "arm64", "riscv64"} {
		if got[i].Arch != arch {
			t.Errorf("%s Archives[%d].Arch: want %q, got %q", testOCIPush, i, arch, got[i].Arch)
		}
		// The name binds the arch as a LITERAL: this job does not fan over the axis, so a
		// ${{ matrix.M6E_ARCH }} here would resolve to empty and 404 the download.
		if !strings.Contains(got[i].Name, "-"+arch+"-") {
			t.Errorf("%s Archives[%d].Name %q must bind arch %q literally", testOCIPush, i, got[i].Name, arch)
		}
		if strings.Contains(got[i].Name, ci.ArchAxis) {
			t.Errorf("%s Archives[%d].Name %q still carries the dropped axis", testOCIPush, i, got[i].Name)
		}
	}
	// Every named archive must actually be downloaded, or the publish reads a missing file.
	for _, j := range m.Jobs {
		if j.Name != "published" {
			continue
		}
		if len(j.Downloads) != 3 {
			t.Errorf("published downloads: want one per arch, got %v", j.Downloads)
		}
		for i, d := range j.Downloads {
			if d.Name != got[i].Name {
				t.Errorf("published download %d %q != archive %q", i, d.Name, got[i].Name)
			}
		}
	}
	out, err := Workflow(m, Targets[TargetGHA], ci.Platform{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "          archives: |") {
		t.Errorf("rendered workflow missing the archives input:\n%s", out)
	}
	// The superseded spelling must be gone: an `arch:` input would suffix every tag.
	if strings.Contains(string(out), "          arch: ") {
		t.Errorf("rendered workflow still carries a per-arch tag suffix:\n%s", out)
	}
}

// TestPublishWithoutArchDeclarationIsUnchanged pins the fleet default. ~110 projects
// declare no architecture, and for them the drop is a no-op: one artifact, the single
// -image path, and not one byte of multi-arch machinery in the rendered workflow.
func TestPublishWithoutArchDeclarationIsUnchanged(t *testing.T) {
	st, err := ci.Parse([]byte(strings.Replace(archSubtree,
		`"matrix": {"axes": {"M6E_ARCH": ["amd64", "arm64", "riscv64"]}},`, "", 1)))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rm, err := resolve.Resolve(st)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	m := Build(rm, st, nil)
	if got := steps(m)[testOCIPush].Archives; len(got) != 0 {
		t.Errorf("%s Archives: want none without a declaration, got %v", testOCIPush, got)
	}
	out, err := Workflow(m, Targets[TargetGHA], ci.Platform{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "archives:") {
		t.Errorf("rendered workflow must not carry archives without a declaration:\n%s", out)
	}
	if !strings.Contains(string(out), "          artifact-name: ") {
		t.Errorf("rendered workflow lost the single-image artifact-name:\n%s", out)
	}
}

// livePinSubtree is Wave 6's shape: a build fanning over every declared arch feeding a
// live fuse that PINS the axis to one. The pin is what a node needs when it must run
// once yet still read a per-cell artifact — the complement of the manifest node's drop.
const livePinSubtree = `{
  "image": "b19/ubuntu",
  "matrix": {"axes": {"M6E_ARCH": ["amd64", "arm64", "riscv64"]}},
  "tools": {
    "container-build": {"action": "container-build"},
    "dc-up-d": {"fuse": "live", "run": "docker compose up --detach"},
    "container-test": {"fuse": "live", "run": "test"},
    "dc-down": {"fuse": "live", "when": "always", "run": "docker compose down --volumes"}
  },
  "nodes": {
    "image-built": {"matrix": true, "needs": {"container-build": true}},
    "container-is-ready": {"matrix": {"pin": {"M6E_ARCH": "amd64"}}, "needs": {"image-built": true, "dc-up-d": true}},
    "container-is-verified": {"goal": true, "matrix": {"pin": {"M6E_ARCH": "amd64"}},
      "needs": {"container-is-ready": true, "container-test": true, "dc-down": true}}
  }
}`

// TestLiveTestPinsOneArchAndStillNamesIt is the property the whole policy rests on: the
// live job runs on ONE arch, and the artifact it downloads is that arch's. Dropping the
// axis would have shortened the stem to a name no build cell ever uploaded, so the skip
// has to narrow the axis rather than remove it.
func TestLiveTestPinsOneArchAndStillNamesIt(t *testing.T) {
	st, err := ci.Parse([]byte(livePinSubtree))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rm, err := resolve.Resolve(st)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	m := Build(rm, st, nil)

	live := jobOf(m, testDCDown)
	if len(live.Matrix) != 1 || live.Matrix[0].Key != ci.ArchAxis {
		t.Fatalf("live job matrix: want the arch axis kept, got %v", live.Matrix)
	}
	if got := strings.Join(live.Matrix[0].Values, " "); got != "amd64" {
		t.Errorf("live job arch values: want amd64 alone, got %q", got)
	}
	if len(live.Downloads) != 1 || !strings.Contains(live.Downloads[0].Name, "${{ matrix."+ci.ArchAxis+" }}") {
		t.Fatalf("live job downloads %v — the stem must still bind the arch axis", live.Downloads)
	}
	// The build keeps every arch: only the live test is narrowed.
	if got := jobOf(m, testContainerBuild).Matrix[0].Values; len(got) != 3 {
		t.Errorf("container-build arch values: want all three built, got %v", got)
	}

	out, err := Workflow(m, Targets[TargetGHA], ci.Platform{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `        `+ci.ArchAxis+`: ["amd64"]`) {
		t.Errorf("rendered workflow missing the pinned single-value axis:\n%s", out)
	}
}

// TestSelfImageRefIsArchScoped pins the pairing that keeps a shared docker store honest.
// Every cell dimension but the arch is already in the image NAME (the basename carries
// its {AXIS} placeholders); the derived arch axis reaches no basename, so without a
// suffix each of a series' arch cells docker-loads a DIFFERENT image under ONE ref and
// the last load wins — on a host-mode runner, where concurrent jobs share one daemon.
// The stamp and the load must move together or the load simply misses.
func TestSelfImageRefIsArchScoped(t *testing.T) {
	st, err := ci.Parse([]byte(livePinSubtree))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rm, err := resolve.Resolve(st)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	m := Build(rm, st, nil)

	archExpr := "${{ matrix." + ci.ArchAxis + " }}"
	live := jobOf(m, testDCDown)
	env := map[string]string{}
	for _, e := range live.Env {
		env[e.Key] = e.Value
	}
	loaded := env[ImageFullnameEnv]
	if !strings.HasSuffix(loaded, "-"+archExpr) {
		t.Errorf("%s = %q, want the cell arch appended", ImageFullnameEnv, loaded)
	}
	if !strings.Contains(env[ComposeProjectEnv], "-"+archExpr+"-") {
		t.Errorf("%s = %q, want the cell arch in the stack identity", ComposeProjectEnv, env[ComposeProjectEnv])
	}
	// The reap targets the ref this job actually loaded, never a sibling cell's.
	if live.RmiImage != loaded {
		t.Errorf("RmiImage %q must be the loaded ref %q", live.RmiImage, loaded)
	}

	out, err := Workflow(m, Targets[TargetGHA], ci.Platform{})
	if err != nil {
		t.Fatal(err)
	}
	stamp := "b19/ubuntu:" + selfImageTagExpr(archExpr)
	if !strings.Contains(string(out), "          image: "+stamp) {
		t.Fatalf("container-build must stamp the arch-scoped ref %q:\n%s", stamp, out)
	}
	// Stamp and load differ only in the tag hoist, which the env block inlines.
	if inlineHoists(stamp) != inlineHoists(loaded) {
		t.Errorf("stamp %q and load %q must be one ref", inlineHoists(stamp), inlineHoists(loaded))
	}
}

// TestNoArchAxisRendersNoArchInputs pins the opt-in from the other side: the ~110
// projects that declare no architecture must render byte-identically to before the axis
// existed, so neither input may appear when nothing minted the axis.
func TestNoArchAxisRendersNoArchInputs(t *testing.T) {
	st, err := ci.Parse([]byte(strings.Replace(archSubtree,
		`"matrix": {"axes": {"M6E_ARCH": ["amd64", "arm64", "riscv64"]}},`, "", 1)))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rm, err := resolve.Resolve(st)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	m := Build(rm, st, nil)
	for _, name := range []string{testContainerBuild, testOCIPush} {
		if got := steps(m)[name].Arch; got != "" {
			t.Errorf("%s Arch: want empty without the axis, got %q", name, got)
		}
	}
	out, err := Workflow(m, Targets[TargetGHA], ci.Platform{})
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{"          platform: ", "          arch: "} {
		if strings.Contains(string(out), line) {
			t.Errorf("rendered workflow must not carry %q without the axis:\n%s", line, out)
		}
	}
}

// archRunnerPlatform routes two of the three arches natively and leaves amd64 unmapped,
// which is exactly the GitHub row of the fleet map: hosted arm64 and riscv64 machines,
// amd64 on the ordinary default.
var archRunnerPlatform = ci.Platform{RunsOnByArch: map[string]string{
	"arm64":   "ubuntu-24.04-arm",
	"riscv64": "ubuntu-26.04-riscv",
}}

// TestArchRunnersRouteEachCell pins the whole routing contract on one workflow: a
// fanning job reads its runner from the matrix, EVERY arch of the axis gets a row
// (an unmapped one carrying the default, since an empty M6E_RUNNER would render an
// invalid runs-on), and a job that dropped the axis keeps the workflow default.
func TestArchRunnersRouteEachCell(t *testing.T) {
	st, err := ci.Parse([]byte(archSubtree))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rm, err := resolve.Resolve(st)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	m := Build(rm, st, nil)
	out, err := Workflow(m, Targets[TargetGHA], archRunnerPlatform)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{
		"runs-on: ${{ matrix.M6E_RUNNER }}",
		`- M6E_ARCH: "amd64"` + "\n            " + `M6E_RUNNER: "ubuntu-latest"`,
		`- M6E_ARCH: "arm64"` + "\n            " + `M6E_RUNNER: "ubuntu-24.04-arm"`,
		`- M6E_ARCH: "riscv64"` + "\n            " + `M6E_RUNNER: "ubuntu-26.04-riscv"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("arch-routed workflow missing %q\n---\n%s", want, s)
		}
	}
	// The assembly job dropped the axis, so it has no cell to route: it must keep the
	// plain runner rather than reading a variable no include row of its own defines.
	if !strings.Contains(s, "published:\n    runs-on: ubuntu-latest") {
		t.Errorf("axis-dropping job must keep the workflow default runner:\n%s", s)
	}
}

// TestArchRunnersAbsentMapChangesNothing is the zero-diff half: a target that declares
// no map must render the SAME bytes as before the routing existed, on a project that
// fans over arch — otherwise the whole fleet's forgejo lowering churns.
func TestArchRunnersAbsentMapChangesNothing(t *testing.T) {
	st, err := ci.Parse([]byte(archSubtree))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rm, err := resolve.Resolve(st)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	m := Build(rm, st, nil)
	out, err := Workflow(m, Targets[TargetGHA], ci.Platform{})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, absent := range []string{"M6E_RUNNER", "matrix.M6E_RUNNER"} {
		if strings.Contains(s, absent) {
			t.Errorf("no runner map declared but workflow carries %q:\n%s", absent, s)
		}
	}
	if !strings.Contains(s, "runs-on: ubuntu-latest") {
		t.Errorf("unmapped target must render the adapter default runner:\n%s", s)
	}
}

// advisorySubtree pairs the two step KINDS a tool lowers to — a host `run:` and an
// image tool through run-tool — with one BLOCKING sibling of each, so the same
// assertion proves both the opt-in and the fail-closed default.
const advisorySubtree = `{
  "tools": {
    "link-validate":  {"image": "reg.example/js-tools:latest", "run": "auto-linkinator", "advisory": true},
    "grype-scan":     {"image": "reg.example/js-tools:latest", "run": "auto-grype"},
    "spell-check":    {"run": "make spell", "advisory": true},
    "site-build":     {"run": "make site"}
  },
  "nodes": {
    "source-is-valid": {"goal": true, "needs": {"link-validate": true, "grype-scan": true, "spell-check": true, "site-build": true}}
  }
}`

// TestAdvisoryLowering pins the non-blocking verdict on both step kinds, and pins that
// it is INERT where nobody wrote it: a host `run:` step takes the runner's own
// `continue-on-error`, a containerised one takes the run-tool `advisory` input (the rc
// decode already lives in that action), and a tool with no `advisory` key renders
// byte-identically to before — fail-closed, so no existing document changes meaning.
func TestAdvisoryLowering(t *testing.T) {
	st, err := ci.Parse([]byte(advisorySubtree))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rm, err := resolve.Resolve(st)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	m := Build(rm, st, nil)

	// Neutral model: only the two declared tools carry the flag.
	for name, want := range map[string]bool{
		"link-validate": true, "spell-check": true,
		"grype-scan": false, "site-build": false,
	} {
		if got := steps(m)[name].Advisory; got != want {
			t.Errorf("%s.Advisory = %v, want %v", name, got, want)
		}
	}

	for _, tgt := range []string{TargetGHA, TargetForgejo} {
		out, err := Workflow(m, Targets[tgt], ci.Platform{})
		if err != nil {
			t.Fatalf("%s: %v", tgt, err)
		}
		s := string(out)
		// Host step: the runner's own knob, directly above the command it guards.
		if !strings.Contains(s, "        continue-on-error: true\n        run: make spell\n") {
			t.Errorf("%s: advisory host step lost its continue-on-error:\n%s", tgt, s)
		}
		// Image step: the action input, beside the run it reports on.
		if !strings.Contains(s, "          advisory: \"true\"\n          run: auto-linkinator\n") {
			t.Errorf("%s: advisory image step lost its run-tool input:\n%s", tgt, s)
		}
		// Inert where absent: neither spelling reaches a blocking sibling.
		if strings.Contains(s, "      - name: site-build\n        continue-on-error") {
			t.Errorf("%s: a blocking host step must render no continue-on-error:\n%s", tgt, s)
		}
		// Counted on the KEY spellings (leading newline + exact indent) so the rationale
		// comments above them, which name both knobs in prose, never satisfy the assertion.
		if n := strings.Count(s, "\n          advisory: "); n != 1 {
			t.Errorf("%s: want exactly 1 advisory input (the one tool that asked), got %d", tgt, n)
		}
		if n := strings.Count(s, "\n        continue-on-error: "); n != 1 {
			t.Errorf("%s: want exactly 1 continue-on-error, got %d", tgt, n)
		}
	}
}
