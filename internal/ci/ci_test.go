// SPDX-FileCopyrightText: 2026 Damián Búho <damian.buho@proton.me>
//
// SPDX-License-Identifier: MIT

package ci

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"kiota.ch/projectfile/core/v2/pkg/genlog"
	"kiota.ch/projectfile/core/v2/pkg/projectfile"
)

// testB19Ubuntu is the canonical basename exercised across the image/interpolate
// tests (goconst).
const testB19Ubuntu = "b19/ubuntu"

// TestImageBasenameExplicit pins the convention-over-configuration rule: an explicit
// org.projectfile.ci.image is read verbatim by Parse (the derivation in Load is the
// fallback ONLY, and never overrides an author's value).
func TestImageBasenameExplicit(t *testing.T) {
	st, err := Parse([]byte(`{
	  "image": "b19/ubuntu",
	  "tools": {"container-build": {"provider": "container-build"}},
	  "nodes": {"ready": {"goal": true, "needs": {"container-build": true}}}
	}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if st.Image != testB19Ubuntu {
		t.Errorf("explicit image must survive Parse verbatim, got %q", st.Image)
	}
}

// TestBuildArgsDecode pins the typed `args` grammar: each NAME maps to a single-key
// {source: ref}, decoded name-sorted into a BuildArg slice with the source kind and
// ref preserved for the render lowering.
func TestBuildArgsDecode(t *testing.T) {
	st, err := Parse([]byte(`{
	  "image": "b19/ubuntu",
	  "tools": {"container-build": {"action": "container-build", "args": {
	    "SOURCE_DOCKER_REGISTRY": {"var": "SOURCE_DOCKER_REGISTRY"},
	    "M6E_NAMESPACE": "${image.namespace}",
	    "M6E_VERSION": {"ci": "version"},
	    "B19_UBUNTU_HASH": {"file": ".container/foundation/deps/ubuntu/{B19_UBUNTU_SERIES}.sha256.deps"}
	  }}},
	  "nodes": {"ready": {"goal": true, "needs": {"container-build": true}}}
	}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	got := st.Tools["container-build"].Args
	// The string form decodes to SourceLiteral with the RAW ${…} ref — Parse keeps
	// it verbatim; interpolation to the literal happens later in Load (needs the doc).
	want := []BuildArg{
		{Name: "B19_UBUNTU_HASH", Source: SourceFile, Ref: ".container/foundation/deps/ubuntu/{B19_UBUNTU_SERIES}.sha256.deps"},
		{Name: "M6E_NAMESPACE", Source: SourceLiteral, Ref: "${image.namespace}"},
		{Name: "M6E_VERSION", Source: SourceCI, Ref: CIKeyVersion},
		{Name: "SOURCE_DOCKER_REGISTRY", Source: SourceVar, Ref: "SOURCE_DOCKER_REGISTRY"},
	}
	if len(got) != len(want) {
		t.Fatalf("args: want %d entries, got %d (%+v)", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("args[%d]: want %+v got %+v", i, want[i], got[i])
		}
	}
}

// TestBuildArgsReject pins the fail-fast: an unknown source (the retired `get:`
// included — it is now the `${…}` STRING form, not an object source), an unknown ci
// key, and a non-singleton source object must each fail the parse — a typo can not
// silently drop a required build-arg (and clobber a Dockerfile default empty).
func TestBuildArgsReject(t *testing.T) {
	for name, args := range map[string]string{
		"unknown-source": `{"X": {"env": "X"}}`,
		"retired-get":    `{"X": {"get": "image.name"}}`,
		"unknown-ci-key": `{"X": {"ci": "sha"}}`,
		"two-sources":    `{"X": {"var": "X", "file": "p"}}`,
	} {
		doc := `{"tools": {"container-build": {"action": "container-build", "args": ` + args + `}},
		  "nodes": {"ready": {"goal": true, "needs": {"container-build": true}}}}`
		if _, err := Parse([]byte(doc)); err == nil {
			t.Errorf("%s: expected a parse error, got nil", name)
		}
	}
}

// TestBuildInputsDecode pins the org.projectfile.build.args wire shapes the
// resolver auto-forwards as container-build build-args: a bare SCALAR (string,
// number, or bool — coerced to its literal string form so `B19_GCC_SERIES: 16` is
// not bamboozled into quoting), an explicit {default: <scalar>}, and a {file}
// input. Decoded name-sorted; UseNumber preserves `3.14` exactly (no float lie).
func TestBuildInputsDecode(t *testing.T) {
	got, err := decodeBuildInputs([]byte(`{
	  "B19_FD_IMAGE": "",
	  "B19_GCC_SERIES": 16,
	  "B19_NODE_FLOAT": 3.14,
	  "B19_DEBUG": true,
	  "B19_UBUNTU_VERSION": {"default": "resolute"},
	  "B19_UBUNTU_SERIES": {"default": 24},
	  "B19_UBUNTU_HASH": {"file": ".container/{B19_UBUNTU_SERIES}/hash"}
	}`))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	want := []BuildInput{
		{Name: "B19_DEBUG", Default: BoolTrue},
		{Name: "B19_FD_IMAGE"},
		{Name: "B19_GCC_SERIES", Default: "16"},
		{Name: "B19_NODE_FLOAT", Default: "3.14"},
		{Name: "B19_UBUNTU_HASH", File: ".container/{B19_UBUNTU_SERIES}/hash"},
		{Name: "B19_UBUNTU_SERIES", Default: "24"},
		{Name: "B19_UBUNTU_VERSION", Default: "resolute"},
	}
	if len(got) != len(want) {
		t.Fatalf("inputs: want %d entries, got %d (%+v)", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("inputs[%d]: want %+v got %+v", i, want[i], got[i])
		}
	}
	// An absent args subtree yields no inputs (graceful, not an error).
	if out, err := decodeBuildInputs(nil); err != nil || out != nil {
		t.Errorf("nil args: want (nil, nil), got (%+v, %v)", out, err)
	}
	// Declaring both default and file is ambiguous — it must fail the parse.
	if _, err := decodeBuildInputs([]byte(`{"X": {"default": "a", "file": "b"}}`)); err == nil {
		t.Error("default+file: expected a parse error, got nil")
	}
	// A non-scalar default (an array) must fail, not silently coerce.
	if _, err := decodeBuildInputs([]byte(`{"X": {"default": [1, 2]}}`)); err == nil {
		t.Error("array default: expected a parse error, got nil")
	}
}

// TestMatrixAxisScalarValues pins that a matrix axis accepts bare YAML/JSON scalars,
// not just quoted strings: an author writing `B19_NODE_SERIES: [24, 26]` (the b19/node
// style) must decode to the literal tokens, and UseNumber must preserve `3.14` exactly
// rather than reformatting the float. This is the b19/{erlang,gcc,java,llvm,node} fix.
func TestMatrixAxisScalarValues(t *testing.T) {
	st, err := Parse([]byte(`{
	  "image": "b19/node",
	  "matrix": {"axes": {"INT": [24, 26], "FLOAT": ["3.14"], "MIXED": [1, "fpm", true]}},
	  "tools": {"container-build": {"action": "container-build"}},
	  "nodes": {"ready": {"goal": true, "matrix": true, "needs": {"container-build": true}}}
	}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	got := map[string][]string{}
	for _, a := range st.Axes {
		got[a.Key] = a.Values
	}
	for key, want := range map[string][]string{
		"INT":   {"24", "26"},
		"FLOAT": {"3.14"},
		"MIXED": {"1", "fpm", "true"},
	} {
		if len(got[key]) != len(want) {
			t.Fatalf("axis %s: want %v got %v", key, want, got[key])
		}
		for i := range want {
			if got[key][i] != want[i] {
				t.Errorf("axis %s[%d]: want %q got %q", key, i, want[i], got[key][i])
			}
		}
	}
}

// TestPerNodeMatrixAxes pins the polymorphic node `matrix`: a bool `true` is a CELL
// over the GLOBAL axes (no own axes), while an object `{axes: {...}}` is a CELL over
// its OWN axes — the two coexist in one subtree, isolated, so binaries fan over
// {GOOS,GOARCH} while the image-build node stays on the global series matrix.
func TestPerNodeMatrixAxes(t *testing.T) {
	st, err := Parse([]byte(`{
	  "matrix": {"axes": {"SERIES": ["resolute", "noble"]}},
	  "tools": {"container-build": {"action": "container-build"}, "build-binaries": {"run": "go build"}},
	  "nodes": {
	    "image-built":  {"matrix": true, "needs": {"container-build": true}},
	    "bins-built":   {"matrix": {"axes": {"GOOS": ["linux", "darwin"], "GOARCH": ["amd64", "arm64"]}}, "needs": {"build-binaries": true}},
	    "ready":        {"goal": true, "needs": {"image-built": true, "bins-built": true}}
	  }
	}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	// The bool node is a cell with NO own axes (falls back to global at resolve time).
	img := st.Nodes["image-built"]
	if !img.Matrix || len(img.Axes) != 0 {
		t.Errorf("image-built: want matrix cell with no own axes, got matrix=%v axes=%v", img.Matrix, img.Axes)
	}
	// The object node is a cell carrying its OWN axes, key-sorted (GOARCH before GOOS).
	bins := st.Nodes["bins-built"]
	if !bins.Matrix {
		t.Fatalf("bins-built: an axes object must imply a matrix cell")
	}
	gotKeys := []string{}
	for _, a := range bins.Axes {
		gotKeys = append(gotKeys, a.Key)
	}
	if len(gotKeys) != 2 || gotKeys[0] != "GOARCH" || gotKeys[1] != "GOOS" {
		t.Errorf("bins-built own axes: want [GOARCH GOOS], got %v", gotKeys)
	}
	// The per-node axes are isolated from the global set (the global stays SERIES).
	if len(st.Axes) != 1 || st.Axes[0].Key != "SERIES" {
		t.Errorf("global axes must stay SERIES-only, got %v", st.Axes)
	}
}

// TestMatrixOverridesDecode pins matrix.overrides: each row splits into MATCH (axis
// keys) and VARS (the rest), bare scalars coerce to strings (an unquoted 21 → "21",
// mirroring axis values), and the extra-var names surface via ExtraVarKeys/MatrixKeys
// so a build-arg or image placeholder referencing one is treated as cell-defined.
func TestMatrixOverridesDecode(t *testing.T) {
	st, err := Parse([]byte(`{
	  "matrix": {
	    "axes": {"B19_ZIG_SERIES": ["0.16", "0.15"]},
	    "overrides": [
	      {"B19_ZIG_SERIES": "0.16", "B19_LLVM_SERIES": 21},
	      {"B19_ZIG_SERIES": "0.15", "B19_LLVM_SERIES": 20}
	    ]
	  },
	  "tools": {"container-build": {"action": "container-build"}},
	  "nodes": {"ready": {"goal": true, "matrix": true, "needs": {"container-build": true}}}
	}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(st.Overrides) != 2 {
		t.Fatalf("overrides: want 2 rows, got %d (%+v)", len(st.Overrides), st.Overrides)
	}
	// Row 0: MATCH B19_ZIG_SERIES=0.16, VAR B19_LLVM_SERIES=21 (coerced from int).
	r0 := st.Overrides[0]
	if len(r0.Match) != 1 || r0.Match[0] != (KV{Key: "B19_ZIG_SERIES", Value: "0.16"}) {
		t.Errorf("row0 match: want {B19_ZIG_SERIES=0.16}, got %+v", r0.Match)
	}
	if len(r0.Vars) != 1 || r0.Vars[0] != (KV{Key: "B19_LLVM_SERIES", Value: "21"}) {
		t.Errorf("row0 vars: want {B19_LLVM_SERIES=21}, got %+v", r0.Vars)
	}
	// The extra var is a matrix key (a cell carries it), alongside the axis.
	if !st.ExtraVarKeys()["B19_LLVM_SERIES"] {
		t.Errorf("ExtraVarKeys must include B19_LLVM_SERIES, got %v", st.ExtraVarKeys())
	}
	if !st.MatrixKeys()["B19_LLVM_SERIES"] || !st.MatrixKeys()["B19_ZIG_SERIES"] {
		t.Errorf("MatrixKeys must include the axis and the extra var, got %v", st.MatrixKeys())
	}
}

// TestMatrixOverridesRejects pins the fail-fast: a row whose axis-value match no cell
// satisfies is refused (a typo'd series never silently drops its vars), and a
// non-scalar field is refused.
func TestMatrixOverridesRejects(t *testing.T) {
	for name, override := range map[string]string{
		"no-match":   `[{"B19_ZIG_SERIES": "0.99", "B19_LLVM_SERIES": 21}]`,
		"non-scalar": `[{"B19_ZIG_SERIES": "0.16", "B19_LLVM_SERIES": [1, 2]}]`,
	} {
		doc := `{"matrix": {"axes": {"B19_ZIG_SERIES": ["0.16", "0.15"]}, "overrides": ` + override + `},
		  "tools": {"container-build": {"action": "container-build"}},
		  "nodes": {"ready": {"goal": true, "matrix": true, "needs": {"container-build": true}}}}`
		if _, err := Parse([]byte(doc)); err == nil {
			t.Errorf("%s: expected a parse error, got nil", name)
		}
	}
}

// TestMatrixExcludeDecode pins matrix.exclude on BOTH matrices: the global one and a
// node's own. Each row is key-sorted axis KEY→value pairs, scalars coerce like axis
// values, and Cells subtracts exactly the matched cells from the product — the count
// every lowering fans over.
func TestMatrixExcludeDecode(t *testing.T) {
	st, err := Parse([]byte(`{
	  "matrix": {
	    "axes": {"GOOS": ["linux", "darwin"], "GOARCH": ["amd64", "arm64", "riscv64"]},
	    "exclude": [{"GOOS": "darwin", "GOARCH": "riscv64"}]
	  },
	  "nodes": {
	    "binaries-built": {"goal": true, "matrix": true, "needs": {"build-binaries": true}},
	    "images-built": {"matrix": {"axes": {"ARCH": ["amd64", "arm64"], "FLAVOUR": ["slim", "full"]},
	                                "exclude": [{"ARCH": "arm64", "FLAVOUR": "full"}]},
	                     "needs": {"container-build": true}}
	  }
	}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(st.Excludes) != 1 || len(st.Excludes[0]) != 2 ||
		st.Excludes[0][0] != (KV{Key: "GOARCH", Value: archRISCV}) ||
		st.Excludes[0][1] != (KV{Key: "GOOS", Value: "darwin"}) {
		t.Fatalf("global exclude: want key-sorted {GOARCH=riscv64, GOOS=darwin}, got %+v", st.Excludes)
	}
	// 2 x 3 = 6 cells, minus the one excluded combination.
	if got := len(Cells(st.Axes, st.Excludes)); got != 5 {
		t.Errorf("global cells: want 5, got %d", got)
	}
	// The excluded cell is the one that is gone, not some neighbour.
	for _, c := range Cells(st.Axes, st.Excludes) {
		if c["GOOS"] == "darwin" && c["GOARCH"] == archRISCV {
			t.Errorf("cell darwin/riscv64 survived the exclusion: %v", c)
		}
	}
	// A per-node matrix carries its OWN exclusions, isolated from the global ones.
	n := st.Nodes["images-built"]
	if got := len(Cells(n.Axes, n.Excludes)); got != 3 {
		t.Errorf("node cells: want 3 (2x2 minus arm64/full), got %d", got)
	}
}

// TestMatrixExcludeRejects pins the fail-fast set: an exclusion that excludes nothing
// (a non-axis field, a value no cell carries) or everything (the whole product) is a
// parse error, because either one is a typo whose only symptom would be a cell count
// nobody expected.
func TestMatrixExcludeRejects(t *testing.T) {
	for name, exclude := range map[string]string{
		"non-axis-field":   `[{"GOOS": "linux", "CGO_ENABLED": "0"}]`,
		"no-match":         `[{"GOOS": "plan9", "GOARCH": "riscv64"}]`,
		"non-scalar":       `[{"GOOS": "darwin", "GOARCH": ["riscv64", "arm64"]}]`,
		"empties-product":  `[{"GOOS": "linux"}, {"GOOS": "darwin"}]`,
		"empty-row":        `[{}]`,
		"exclude-no-axes":  `[{"GOOS": "linux"}]`,
		"node-level-match": `[{"GOOS": "linux", "GOARCH": "riscv64"}]`,
	} {
		axes := `"axes": {"GOOS": ["linux", "darwin"], "GOARCH": ["amd64", "riscv64"]}, `
		if name == "exclude-no-axes" {
			axes = ""
		}
		doc := `{"matrix": {` + axes + `"exclude": ` + exclude + `},
		  "nodes": {"ready": {"goal": true, "matrix": true, "needs": {"build-binaries": true}}}}`
		if name == "node-level-match" { // the same rules bind a per-node matrix
			doc = `{"nodes": {"ready": {"goal": true, "matrix": {"axes": {"GOOS": ["linux"]},
			  "exclude": ` + exclude + `}, "needs": {"build-binaries": true}}}}`
		}
		if _, err := Parse([]byte(doc)); err == nil {
			t.Errorf("%s: expected a parse error, got nil", name)
		}
	}
}

// TestMatrixOverridesRejectsExcludedCell pins the interaction: an override row that
// matches ONLY cells matrix.exclude removed decorates nothing, so it is the same
// no-match error as a typo'd axis value. The two lists are read as one grid, never
// against each other's stale view of it.
func TestMatrixOverridesRejectsExcludedCell(t *testing.T) {
	doc := `{"matrix": {
	    "axes": {"GOOS": ["linux", "darwin"], "GOARCH": ["amd64", "riscv64"]},
	    "exclude": [{"GOOS": "darwin", "GOARCH": "riscv64"}],
	    "overrides": [{"GOOS": "darwin", "GOARCH": "riscv64", "CGO_ENABLED": "1"}]
	  },
	  "nodes": {"ready": {"goal": true, "matrix": true, "needs": {"build-binaries": true}}}}`
	if _, err := Parse([]byte(doc)); err == nil {
		t.Error("an override matching only an excluded cell must be refused, got nil")
	}
}

// TestNodeMatrixRejectsUnhonouredKeys pins that a node matrix REFUSES what it cannot
// honour instead of dropping it. Both spellings used to be accepted and discarded in
// silence, whose symptom is a projectfile that looks changed and workflows that
// regenerate byte for byte identical.
func TestNodeMatrixRejectsUnhonouredKeys(t *testing.T) {
	for name, matrix := range map[string]string{
		"overrides":    `{"axes": {"GOOS": ["linux"]}, "overrides": [{"GOOS": "linux", "CGO_ENABLED": "1"}]}`,
		"unknown":      `{"axess": {"GOOS": ["linux"]}}`,
		"axes+without": `{"axes": {"GOOS": ["linux"]}, "without": ["SERIES"]}`,
		"axes+pin":     `{"axes": {"GOOS": ["linux"]}, "pin": {"SERIES": "noble"}}`,
	} {
		doc := `{"nodes": {"ready": {"goal": true, "matrix": ` + matrix + `, "needs": {"build-binaries": true}}}}`
		if _, err := Parse([]byte(doc)); err == nil {
			t.Errorf("node matrix %s: expected a parse error, got nil", name)
		}
	}
}

// TestEmitDecode pins that a publish tool's `emit` survives Parse onto the manifest —
// the event name the render lowers to a fact emission after that tool's step.
func TestEmitDecode(t *testing.T) {
	st, err := Parse([]byte(`{
	  "tools": {"oci-push": {"action": "oci-push", "emit": "ci.image.published"}},
	  "nodes": {"published": {"goal": true, "needs": {"oci-push": true}}}
	}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := st.Tools["oci-push"].Emit; got != "ci.image.published" {
		t.Errorf("emit: want ci.image.published, got %q", got)
	}
}

// TestEmitRejectsNonPublisher pins the fail-closed pairing: the emitted payload is read
// back from what the publish action wrote, so `emit` on a tool that publishes nothing
// would POST an image fact with nothing in it. Refuse it at Parse instead.
func TestEmitRejectsNonPublisher(t *testing.T) {
	for name, tool := range map[string]string{
		"plain-run":    `{"run": "auto-shellcheck", "emit": "ci.image.published"}`,
		"wrong-action": `{"action": "container-build", "emit": "ci.image.published"}`,
	} {
		doc := `{"tools": {"t": ` + tool + `}, "nodes": {"done": {"goal": true, "needs": {"t": true}}}}`
		if _, err := Parse([]byte(doc)); err == nil {
			t.Errorf("%s: emit on a non-publishing tool must be a parse error, got nil", name)
		}
	}
}

// TestMaxParallelDecode pins that a matrix node's `max-parallel` survives Parse onto
// the model — the per-node cap the render lifts onto strategy.max-parallel.
func TestMaxParallelDecode(t *testing.T) {
	st, err := Parse([]byte(`{
	  "tools": {"container-build": {"action": "container-build"}},
	  "nodes": {"image-built": {"goal": true, "matrix": true, "max-parallel": 1, "needs": {"container-build": true}}}
	}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := st.Nodes["image-built"].MaxParallel; got != 1 {
		t.Errorf("max-parallel: want 1, got %d", got)
	}
}

// TestMaxParallelRejects pins the fail-fast guards: max-parallel is meaningless off a
// matrix node and must be a positive cap — either mistake is a parse error, never a
// silently-dropped field.
func TestMaxParallelRejects(t *testing.T) {
	for name, node := range map[string]string{
		"non-matrix": `{"goal": true, "max-parallel": 1, "needs": {"container-build": true}}`,
		"negative":   `{"goal": true, "matrix": true, "max-parallel": -2, "needs": {"container-build": true}}`,
	} {
		doc := `{"tools": {"container-build": {"action": "container-build"}},
		  "nodes": {"image-built": ` + node + `}}`
		if _, err := Parse([]byte(doc)); err == nil {
			t.Errorf("%s: expected a parse error, got nil", name)
		}
	}
}

// TestSerialiseDecode pins that a matrix node's `serialise` survives Parse onto the
// model — the axis the render walks one job at a time, chained by `needs`.
func TestSerialiseDecode(t *testing.T) {
	st, err := Parse([]byte(`{
	  "matrix": {"axes": {"B19_NODE_SERIES": ["24", "26"]}},
	  "tools": {"container-build": {"action": "container-build"}},
	  "nodes": {"image-built": {"goal": true, "matrix": true, "serialise": "B19_NODE_SERIES", "needs": {"container-build": true}}}
	}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := st.Nodes["image-built"].Serialise; got != "B19_NODE_SERIES" {
		t.Errorf("serialise: want B19_NODE_SERIES, got %q", got)
	}
}

// TestSerialiseRejects pins the fail-fast guards: there is nothing to chain without
// cells, and the field names a GLOBAL axis — a node declaring its OWN (isolated) axes
// cannot mean that, so the ambiguous spelling fails the parse rather than rendering a
// silently un-chained node.
func TestSerialiseRejects(t *testing.T) {
	for name, node := range map[string]string{
		"non-matrix":    `{"goal": true, "serialise": "B19_NODE_SERIES", "needs": {"container-build": true}}`,
		"per-node-axes": `{"goal": true, "matrix": {"axes": {"GOOS": ["linux", "darwin"]}}, "serialise": "GOOS", "needs": {"container-build": true}}`,
	} {
		doc := `{"matrix": {"axes": {"B19_NODE_SERIES": ["24", "26"]}},
		  "tools": {"container-build": {"action": "container-build"}},
		  "nodes": {"image-built": ` + node + `}}`
		if _, err := Parse([]byte(doc)); err == nil {
			t.Errorf("%s: expected a parse error, got nil", name)
		}
	}
}

// TestDispatchBuildArgsFlag pins that `dispatch: {build-args: true}` decodes onto the
// goal's Dispatch (with no explicit inputs), so the render can auto-expose the declared
// build.args as workflow_dispatch inputs. An absent flag leaves it false.
func TestDispatchBuildArgsFlag(t *testing.T) {
	st, err := Parse([]byte(`{
	  "tools": {"container-build": {"action": "container-build"}},
	  "nodes": {"image-built": {"goal": true, "dispatch": {"build-args": true}, "needs": {"container-build": true}}}
	}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	d := st.Nodes["image-built"].Dispatch
	if d == nil {
		t.Fatal("dispatch must be non-nil when build-args is set")
	}
	if !d.BuildArgs {
		t.Errorf("BuildArgs: want true, got false")
	}
	if len(d.Inputs) != 0 {
		t.Errorf("no explicit inputs authored, want 0, got %d", len(d.Inputs))
	}
}

// TestManifestEnvDecode pins that a tool's `env:` name list survives Parse — the
// vendor-neutral credential/passthrough NEED a render binds per target. Names only;
// no value ever lives in the manifest.
func TestManifestEnvDecode(t *testing.T) {
	st, err := Parse([]byte(`{
	  "tools": {"gh-release": {"run": "gh release create", "env": ["GH_TOKEN", "GOPROXY"]}},
	  "nodes": {"published": {"goal": true, "needs": {"gh-release": true}}}
	}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	got := st.Tools["gh-release"].Env
	if len(got) != 2 || got[0] != "GH_TOKEN" || got[1] != "GOPROXY" {
		t.Errorf("env: want [GH_TOKEN GOPROXY], got %v", got)
	}
}

// TestCredentialsOverlayDecode pins the §9-B deployment surface: org.projectfile.ci.
// gha.credentials decodes to the per-target {ENV_NAME: secret-ref} map (the value
// being the only place a `${{ secrets.* }}` ref ever appears), keyed under gha so a
// forgejo render never reads it.
func TestCredentialsOverlayDecode(t *testing.T) {
	st, err := Parse([]byte(`{
	  "gha": {"permissions": {"contents": "write"},
	          "credentials": {"GH_TOKEN": "${{ secrets.GITHUB_TOKEN }}"}},
	  "tools": {"gh-release": {"run": "gh release create", "env": ["GH_TOKEN"]}},
	  "nodes": {"published": {"goal": true, "needs": {"gh-release": true}}}
	}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	p, ok := st.Platforms["gha"]
	if !ok {
		t.Fatal("gha platform overlay missing")
	}
	if got := p.Credentials["GH_TOKEN"]; got != "${{ secrets.GITHUB_TOKEN }}" {
		t.Errorf("credentials[GH_TOKEN]: want secret ref, got %q", got)
	}
	if _, leaked := st.Platforms["forgejo"]; leaked {
		t.Error("forgejo overlay must stay absent — credentials are keyed per target")
	}
}

// TestRunsOnObjectFormDecode pins the third `runs-on` spelling: the arch-keyed object
// that routes each ArchAxis cell to its own runner. `default` is lifted OUT of the map
// into RunsOn — it is the label an unmapped arch falls back to, not an arch of its own,
// and leaving it in would mint a cell for an architecture nobody declares.
func TestRunsOnObjectFormDecode(t *testing.T) {
	st, err := Parse([]byte(`{
	  "gha": {"runs-on": {"default": "ubuntu-latest", "arm64": "ubuntu-24.04-arm", "riscv64": "ubuntu-26.04-riscv"}},
	  "tools": {"shellcheck": {"run": "shellcheck"}},
	  "nodes": {"linted": {"goal": true, "needs": {"shellcheck": true}}}
	}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	p := st.Platforms["gha"]
	if len(p.RunsOn) != 1 || p.RunsOn[0] != "ubuntu-latest" {
		t.Errorf("runs-on default: want [ubuntu-latest], got %v", p.RunsOn)
	}
	if got := p.RunsOnByArch["arm64"]; got != "ubuntu-24.04-arm" {
		t.Errorf("runs-on[arm64]: want the hosted arm label, got %q", got)
	}
	if _, leaked := p.RunsOnByArch[RunsOnDefaultKey]; leaked {
		t.Error("the default key must not survive as an arch entry")
	}
}

// TestRunsOnScalarAndListFormsSurvive is the compatibility half: the two GHA spellings
// still decode to RunsOn and mint no arch map, so every target declaring one of them
// routes nothing and renders as it did before arch routing existed.
func TestRunsOnScalarAndListFormsSurvive(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		want      []string
	}{
		{"scalar", `"docker"`, []string{"docker"}},
		{"list", `["self-hosted", "linux"]`, []string{"self-hosted", "linux"}},
	} {
		st, err := Parse([]byte(`{
		  "forgejo": {"runs-on": ` + tc.raw + `},
		  "tools": {"shellcheck": {"run": "shellcheck"}},
		  "nodes": {"linted": {"goal": true, "needs": {"shellcheck": true}}}
		}`))
		if err != nil {
			t.Fatalf("%s parse: %v", tc.name, err)
		}
		p := st.Platforms["forgejo"]
		if strings.Join(p.RunsOn, ",") != strings.Join(tc.want, ",") {
			t.Errorf("%s runs-on: want %v, got %v", tc.name, tc.want, p.RunsOn)
		}
		if p.RunsOnByArch != nil {
			t.Errorf("%s must mint no arch map, got %v", tc.name, p.RunsOnByArch)
		}
	}
}

// TestRunsOnObjectFormRejectsListValues states the boundary in the one place an author
// meets it: a cell's runner rides a matrix include FIELD, which carries a single string,
// so a per-arch label list has nowhere to go. The multi-label spelling stays available
// as the list form, where it is workflow-wide.
func TestRunsOnObjectFormRejectsListValues(t *testing.T) {
	_, err := Parse([]byte(`{
	  "gha": {"runs-on": {"arm64": ["self-hosted", "arm64"]}},
	  "tools": {"shellcheck": {"run": "shellcheck"}},
	  "nodes": {"linted": {"goal": true, "needs": {"shellcheck": true}}}
	}`))
	if err == nil {
		t.Fatal("a per-arch label LIST must be refused, not silently dropped")
	}
	if !strings.Contains(err.Error(), "runs-on") {
		t.Errorf("error must name the offending key, got %v", err)
	}
}

// TestCheckoutTokenOverlayDecode proves the per-target `checkout-token` opt-in threads
// from the merged subtree JSON into Platforms[target].CheckoutToken. It is a BOOLEAN —
// the secret name is a renderer constant, so no name (and nothing secret — §8) is ever
// spelled in the document.
func TestCheckoutTokenOverlayDecode(t *testing.T) {
	st, err := Parse([]byte(`{
	  "forgejo": {"checkout-token": true},
	  "tools": {"shellcheck": {"run": "shellcheck"}},
	  "nodes": {"linted": {"goal": true, "needs": {"shellcheck": true}}}
	}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	p, ok := st.Platforms["forgejo"]
	if !ok {
		t.Fatal("forgejo platform overlay missing")
	}
	if !p.CheckoutToken {
		t.Error("checkout-token: want the opt-in carried through, got false")
	}
	if _, leaked := st.Platforms["gha"]; leaked {
		t.Error("gha overlay must stay absent — a robot account is keyed per target")
	}
}

// TestActionsOverlayDecode proves the per-target `actions` ref-override map threads
// from the merged subtree JSON into Platforms[target].Actions (the render override
// reads it from there). Keyed per target like every other overlay field.
func TestActionsOverlayDecode(t *testing.T) {
	st, err := Parse([]byte(`{
	  "forgejo": {"actions": {"checkout": "actions/checkout@v7"},
	              "library": "projectfile/actions@v1"},
	  "tools": {"shellcheck": {"run": "shellcheck"}},
	  "nodes": {"linted": {"goal": true, "needs": {"shellcheck": true}}}
	}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	p, ok := st.Platforms["forgejo"]
	if !ok {
		t.Fatal("forgejo platform overlay missing")
	}
	if got := p.Actions["checkout"]; got != "actions/checkout@v7" {
		t.Errorf("actions[checkout]: want the pinned ref, got %q", got)
	}
	if got := p.Library; got != "projectfile/actions@v1" {
		t.Errorf("library: want repo@tag, got %q", got)
	}
	if _, leaked := st.Platforms["gha"]; leaked {
		t.Error("gha overlay must stay absent — action refs are keyed per target")
	}
}

// TestValidateImageAccepts pins the back-compatible / valid shapes ValidateImage must
// PASS: a non-matrix bare basename, a build-only matrix sharing the bare ref (no push),
// a templated ref whose placeholders name declared GLOBAL axes, and one naming a
// PER-NODE axis (the union of both axis sources is the legal placeholder set).
func TestValidateImageAccepts(t *testing.T) {
	for name, doc := range map[string]string{
		"non-matrix-bare": `{
		  "image": "o9s/postgresql",
		  "tools": {"container-build": {"action": "container-build"}},
		  "nodes": {"ready": {"goal": true, "needs": {"container-build": true}}}}`,
		"build-only-matrix-bare": `{
		  "image": "b19/ubuntu",
		  "matrix": {"axes": {"B19_UBUNTU_SERIES": ["noble", "resolute"]}},
		  "tools": {"container-build": {"action": "container-build"}},
		  "nodes": {"image-built": {"matrix": true, "goal": true, "needs": {"container-build": true}}}}`,
		"templated-global-axis": `{
		  "image": "b19/php-{PHP_SAPI}-{B19_PHP_SERIES}",
		  "matrix": {"axes": {"B19_PHP_SERIES": ["8.4", "8.5"], "PHP_SAPI": ["cli", "fpm"]}},
		  "tools": {"container-build": {"action": "container-build"}, "oci-push": {"action": "oci-push"}},
		  "nodes": {"image-built": {"matrix": true, "needs": {"container-build": true}},
		            "published": {"matrix": true, "goal": true, "needs": {"image-built": true, "oci-push": true}}}}`,
		"templated-per-node-axis": `{
		  "image": "b19/cli-{GOARCH}",
		  "tools": {"build-binaries": {"run": "go build"}, "oci-push": {"action": "oci-push"}},
		  "nodes": {"bins-built": {"matrix": {"axes": {"GOARCH": ["amd64", "arm64"]}}, "needs": {"build-binaries": true}},
		            "published": {"goal": true, "needs": {"bins-built": true, "oci-push": true}}}}`,
		// cli shape: a SINGLE image push (publish-image is matrix:true but the GLOBAL
		// axes are empty, so it does NOT fan) coexisting with an UNRELATED per-node
		// binary matrix. The push is one ref, not a collision — the bare basename is fine.
		"single-push-with-unrelated-binary-matrix": `{
		  "image": "projectfile/cli",
		  "tools": {"build-binaries": {"run": "go build"}, "oci-push": {"action": "oci-push"},
		            "container-build": {"action": "container-build"}},
		  "nodes": {"bins-built": {"matrix": {"axes": {"GOARCH": ["amd64", "arm64"]}}, "needs": {"build-binaries": true}},
		            "image-built": {"matrix": true, "needs": {"container-build": true}},
		            "publish-image": {"matrix": true, "needs": {"image-built": true, "oci-push": true}},
		            "published": {"goal": true, "needs": {"bins-built": true, "publish-image": true}}}}`,
		// multiarch shape: the ONLY axis is the derived arch one, and the publish node
		// DROPS it to index every arch's archive into one manifest list. That push is a
		// single ref, so the bare basename is right — a placeholder here would demand a
		// per-arch tag the fleet deliberately never publishes.
		"arch-only-matrix-dropped-at-push": `{
		  "image": "o9s/traefik",
		  "matrix": {"axes": {"M6E_ARCH": ["amd64", "arm64"]}},
		  "tools": {"container-build": {"action": "container-build"}, "oci-push": {"action": "oci-push"}},
		  "nodes": {"image-built": {"matrix": true, "needs": {"container-build": true}},
		            "publish-image": {"matrix": {"without": ["M6E_ARCH"]}, "goal": true,
		                              "needs": {"image-built": true, "oci-push": true}}}}`,
		// The same drop, expressed as a pin: one cell of the axis is still one ref.
		"arch-only-matrix-pinned-at-push": `{
		  "image": "o9s/traefik",
		  "matrix": {"axes": {"M6E_ARCH": ["amd64", "arm64"]}},
		  "tools": {"container-build": {"action": "container-build"}, "oci-push": {"action": "oci-push"}},
		  "nodes": {"image-built": {"matrix": true, "needs": {"container-build": true}},
		            "publish-image": {"matrix": {"pin": {"M6E_ARCH": "amd64"}}, "goal": true,
		                              "needs": {"image-built": true, "oci-push": true}}}}`,
	} {
		st, err := Parse([]byte(doc))
		if err != nil {
			t.Fatalf("%s: parse: %v", name, err)
		}
		if err := st.ValidateImage(); err != nil {
			t.Errorf("%s: ValidateImage must accept, got %v", name, err)
		}
	}
}

// TestValidateImageRejects pins the two fail-fasts, both keyed off DATA (declared
// axes + the oci-push ACTION token), never a node name (Law 1):
//   - unknown-axis: a {KEY} placeholder names an axis the project never declares.
//   - collision: a matrix build CONSUMED by an oci-push but with no {AXIS} placeholder —
//     every cell would push the same ref.
func TestValidateImageRejects(t *testing.T) {
	for name, doc := range map[string]string{
		"unknown-axis": `{
		  "image": "b19/ubuntu-{B19_UBUNTU_SERIESS}",
		  "matrix": {"axes": {"B19_UBUNTU_SERIES": ["noble", "resolute"]}},
		  "tools": {"container-build": {"action": "container-build"}},
		  "nodes": {"image-built": {"matrix": true, "goal": true, "needs": {"container-build": true}}}}`,
		"unknown-axis-no-matrix": `{
		  "image": "acme/app-{NOPE}",
		  "tools": {"container-build": {"action": "container-build"}},
		  "nodes": {"ready": {"goal": true, "needs": {"container-build": true}}}}`,
		"collision-matrix-push": `{
		  "image": "b19/ubuntu",
		  "matrix": {"axes": {"B19_UBUNTU_SERIES": ["noble", "resolute"]}},
		  "tools": {"container-build": {"action": "container-build"}, "oci-push": {"action": "oci-push"}},
		  "nodes": {"image-built": {"matrix": true, "needs": {"container-build": true}},
		            "published": {"matrix": true, "goal": true, "needs": {"image-built": true, "oci-push": true}}}}`,
		// Dropping arch does NOT excuse the series axis: the push still fans one cell
		// per series, and each needs its own ref.
		"collision-survives-the-arch-drop": `{
		  "image": "b19/ubuntu",
		  "matrix": {"axes": {"B19_UBUNTU_SERIES": ["noble", "resolute"], "M6E_ARCH": ["amd64", "arm64"]}},
		  "tools": {"container-build": {"action": "container-build"}, "oci-push": {"action": "oci-push"}},
		  "nodes": {"image-built": {"matrix": true, "needs": {"container-build": true}},
		            "published": {"matrix": {"without": ["M6E_ARCH"]}, "goal": true,
		                          "needs": {"image-built": true, "oci-push": true}}}}`,
	} {
		st, err := Parse([]byte(doc))
		if err != nil {
			t.Fatalf("%s: parse: %v", name, err)
		}
		if err := st.ValidateImage(); err == nil {
			t.Errorf("%s: ValidateImage must reject, got nil", name)
		}
	}
}

// TestWebhookVar pins the webhook-var name resolution off the `{ env: NAME }`
// binding (D6): an explicit env NAME yields that var; an absent binding (empty
// events block, or webhook with no url.env) falls back to the built-in default so
// the sink is still enabled by the mere presence of the events block.
func TestWebhookVar(t *testing.T) {
	cases := map[string]string{
		`{"webhook": {"url": {"env": "MY_HOOK"}}}`:            "MY_HOOK",
		`{"webhook": {"url": {"env": "EVENTS_WEBHOOK_URL"}}}`: "EVENTS_WEBHOOK_URL",
		`{}`:                       DefaultWebhookVar, // empty block opts in with the default
		`{"webhook": {}}`:          DefaultWebhookVar, // webhook present, no url binding
		`{"webhook": {"url": {}}}`: DefaultWebhookVar, // url present, no env NAME
	}
	for in, want := range cases {
		var re rawEvents
		if err := json.Unmarshal([]byte(in), &re); err != nil {
			t.Fatalf("unmarshal %q: %v", in, err)
		}
		if got := webhookVar(re); got != want {
			t.Errorf("webhookVar(%q) = %q, want %q", in, got, want)
		}
	}
}

// declaredImageDoc writes a projectfile declaring the image as PARTS and returns
// its merged document. The parts, not any rule here, are what `${org}`/`${name}`
// resolve to — which is the whole point of the scope.
func declaredImageDoc(t *testing.T) *projectfile.Document {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "projectfile.yaml")
	if err := os.WriteFile(path, []byte(`$schema: https://projectfile.org/schema/v1.json
identity:
  namespace: org.example
  name: ubuntu
org:
  projectfile:
    image:
      org: b19
      name: ${identity.name}
      path: ${org}/${name}
      tag: latest
`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	doc, err := projectfile.Read(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return doc
}

// TestInterpolateSyntax pins the ${…} mechanic: a bare part resolves under the
// image SCOPE, a part whose value is itself a reference resolves recursively,
// `$$` escapes a literal `${…}` (the dc-up-d runtime case), a bare `$VAR` is left
// untouched, and an unresolvable reference collapses to empty (D4).
func TestInterpolateSyntax(t *testing.T) {
	ip := interpolator{doc: declaredImageDoc(t)}
	cases := []struct{ in, want string }{
		{"gsa ${org}", "gsa b19"},                                                   // a declared part, read under the scope
		{"x ${name} y", "x ubuntu y"},                                               // ${identity.name}, resolved recursively
		{"${path}", testB19Ubuntu},                                                  // the composed basename
		{"${org.projectfile.image.path}", testB19Ubuntu},                            // the same value by full address
		{"a ${org.projectfile.nope.here} b", "a  b"},                                // miss → empty (D4)
		{`timeout "$${M6E_TIMEOUT:-300}s" up`, `timeout "${M6E_TIMEOUT:-300}s" up`}, // $$ → literal ${…}
		{"echo $HOME done", "echo $HOME done"},                                      // bare $ untouched
		{"cost is $$5", "cost is $5"},                                               // $$ not before { → literal $
		{"no refs here", "no refs here"},
	}
	for _, c := range cases {
		got, err := ip.interpolate(c.in)
		if err != nil {
			t.Errorf("interpolate(%q): unexpected error %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("interpolate(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestInterpolateGuards pins the fail-loud cases: the retired `[k=v]` bracket
// spelling is refused rather than silently dropped to empty, and an unterminated
// ${ errors.
func TestInterpolateGuards(t *testing.T) {
	ip := interpolator{doc: declaredImageDoc(t)}
	for _, in := range []string{
		"${org.projectfile.artifacts[kind=binary].path}", // `[k=v]` is not the selector spelling
		"${org.projectfile.a[].b[kind=binary].c}",        // …in a LATER span either, past a legal `[]`
		"tail ${unterminated",                            // no closing brace
		"${org.projectfile.artifacts{kind=binary}.path",  // the selector's brace is not the reference's
	} {
		if _, err := ip.interpolate(in); err == nil {
			t.Errorf("interpolate(%q): expected error, got nil", in)
		}
	}
}

// selectorDoc declares the two shapes a reference addresses beyond a plain field:
// a MAP the `{k=v}` selector picks one entry out of, and a LIST the `[]`
// projection takes every element of. Both are what the torrent path writes — one
// asset path per cell, one announce-URL list per project.
func selectorDoc(t *testing.T) *projectfile.Document {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "projectfile.yaml")
	if err := os.WriteFile(path, []byte(`$schema: https://projectfile.org/schema/v1.json
identity:
  namespace: org.example
  name: demo
org:
  projectfile:
    torrent:
      trackers:
        - https://tracker.example.org/announce
        - udp://tracker.example.org:6969
      solo:
        - https://only.example.org/announce
      scalar: a;b
    artifacts:
      bin:
        kind: binary
        path: dist/demo
      img:
        kind: image
        path: dist/demo.tar
`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	doc, err := projectfile.Read(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return doc
}

// TestInterpolateSelectorAndProjection pins the two rules a flat first-`}` scan got
// wrong. A `{k=v}` selector lives INSIDE the reference, so the closing brace has to
// be found by counting: scanning to the first `}` hands core the truncated
// `artifacts{kind=binary`, which core leaves verbatim — the `${…}` then reaches the
// runner and bash answers `bad substitution`. And `[]` is the one spelling that fans
// out: it joins on a space, while a selector that matches SEVERAL entries by accident
// still collapses to empty rather than silently passing two paths as one argument.
func TestInterpolateSelectorAndProjection(t *testing.T) {
	ip := interpolator{doc: selectorDoc(t)}
	cases := []struct{ in, want string }{
		{"--cell ${org.projectfile.artifacts{kind=binary}.path}", "--cell dist/demo"},
		{"${org.projectfile.artifacts{kind=image}.path}", "dist/demo.tar"},
		{"a ${org.projectfile.artifacts{kind=binary}.path} b ${identity.name}", "a dist/demo b demo"},
		{"${org.projectfile.torrent.trackers[]}", "https://tracker.example.org/announce udp://tracker.example.org:6969"},
		{"${org.projectfile.torrent.solo[]}", "https://only.example.org/announce"},
		{"${org.projectfile.torrent.trackers[0]}", "https://tracker.example.org/announce"},
		{"${org.projectfile.torrent.nope[]}", ""},              // a missing list is still a miss → empty
		{"${org.projectfile.torrent.scalar[]}", "a;b"},         // `[]` over a scalar reads it as a list of one
		{"${org.projectfile.artifacts{kind=binary}.nope}", ""}, // a selector hitting no field → empty
		{"${org.projectfile.torrent.trackers}", ""},            // several values WITHOUT `[]` → empty, never joined
	}
	for _, c := range cases {
		got, err := ip.interpolate(c.in)
		if err != nil {
			t.Errorf("interpolate(%q): unexpected error %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("interpolate(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestLoadInterpolatesArtifactRefs is the end-to-end proof: a tool `run` carrying a
// ${org.projectfile.artifacts.<name>.path} reference resolves against the merged doc
// during Load, and an UNresolvable reference collapses to EMPTY (D4 reversed — a
// preset ref to an artifact this project omits leaves the arg unset, never a hard error).
func TestLoadInterpolatesArtifactRefs(t *testing.T) {
	write := func(t *testing.T, run string) string {
		t.Helper()
		dir := t.TempDir()
		p := filepath.Join(dir, "projectfile.yaml")
		doc := `$schema: https://projectfile.org/schema/v1.json
identity:
  namespace: org.example
  name: demo
org:
  projectfile:
    artifacts:
      go-binary:
        kind: binary
        path: dist/demo
    ci:
      tools:
        gsa:
          image: GO_TOOL_IMAGE
          run: ` + run + `
      nodes:
        analyzed:
          goal: true
          needs:
            gsa: true
`
		if err := os.WriteFile(p, []byte(doc), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}

	// Resolvable: the artifact path lands in the run command.
	st, err := Load(write(t, "gsa ${org.projectfile.artifacts.go-binary.path}"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := st.Tools["gsa"].Run; got != "gsa dist/demo" {
		t.Errorf("gsa run = %q, want %q", got, "gsa dist/demo")
	}

	// Unresolvable → empty (D4 reversed): the arg is left unset, Load still succeeds.
	st, err = Load(write(t, "gsa ${org.projectfile.artifacts.missing.path}"))
	if err != nil {
		t.Fatalf("Load (unresolved): unexpected error %v", err)
	}
	if got := st.Tools["gsa"].Run; got != "gsa " {
		t.Errorf("gsa run (unresolved) = %q, want %q", got, "gsa ")
	}
}

// TestLoadResolvesReleaseAssetPath pins the forgejo-release action's binary-path
// resolution: during Load, a forgejo-release tool's ReleaseAssetPath is populated
// from the single org.projectfile.artifacts entry with kind=binary. ZERO leaves it
// empty (the action's create-only route, for a container-only project); >1 still
// errors, because that is an ambiguous attach target rather than an absence.
func TestLoadResolvesReleaseAssetPath(t *testing.T) {
	// write builds a projectfile with one kind=binary artifact and a forgejo-release
	// action tool; extra artifacts can be appended via the artifacts param.
	write := func(t *testing.T, artifacts string) string {
		t.Helper()
		dir := t.TempDir()
		p := filepath.Join(dir, "projectfile.yaml")
		doc := `$schema: https://projectfile.org/schema/v1.json
identity:
  namespace: org.example
  name: demo
org:
  projectfile:
    artifacts:
` + artifacts + `
    ci:
      tools:
        forgejo-release:
          action: forgejo-release
          env: [FORGEJO_TOKEN]
      nodes:
        released:
          goal: true
          needs:
            forgejo-release: true
`
		if err := os.WriteFile(p, []byte(doc), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}

	// Happy path: one kind=binary artifact → path resolved onto the manifest.
	st, err := Load(write(t, "      go-binary:\n        kind: binary\n        path: dist/demo"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := st.Tools["forgejo-release"].ReleaseAssetPath; got != "dist/demo" {
		t.Errorf("ReleaseAssetPath = %q, want %q", got, "dist/demo")
	}

	// Zero kind=binary artifacts is CREATE-ONLY, not an error: a container-only
	// project mints the release its image torrents attach to, and names no binary.
	// The empty path is what the template omits, so the action sees an unset input.
	st, err = Load(write(t, "      docs:\n        kind: website\n        path: dist/docs"))
	if err != nil {
		t.Fatalf("Load with no kind=binary artifact: unexpected error %v", err)
	}
	if got := st.Tools["forgejo-release"].ReleaseAssetPath; got != "" {
		t.Errorf("create-only ReleaseAssetPath = %q, want empty", got)
	}

	// Fail-closed: two kind=binary artifacts (ambiguous attach target).
	two := "      a:\n        kind: binary\n        path: dist/a\n      b:\n        kind: binary\n        path: dist/b"
	if _, err := Load(write(t, two)); err == nil {
		t.Errorf("Load with two kind=binary artifacts: expected error, got nil")
	}

	// The path is a §3.8 string scalar: a shared language fragment declares
	// dist/${identity.name} once and each consumer resolves its own name. Left
	// verbatim, the reference reaches the workflow and the release attaches nothing.
	st, err = Load(write(t, "      bin:\n        kind: binary\n        path: dist/${identity.name}"))
	if err != nil {
		t.Fatalf("Load with interpolated artifact path: %v", err)
	}
	if got := st.Tools["forgejo-release"].ReleaseAssetPath; got != "dist/demo" {
		t.Errorf("interpolated ReleaseAssetPath = %q, want %q", got, "dist/demo")
	}
}

// publishDoc is a document declaring two destinations with DIFFERENT path
// grammars plus the routes that reach them — the shape the publish plane exists
// for. `hub` flattens what `ghcr` nests, and neither shape is known to any code.
func publishDoc(t *testing.T) *projectfile.Document {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "projectfile.yaml")
	if err := os.WriteFile(path, []byte(`$schema: https://projectfile.org/schema/v1.json
identity:
  namespace: org.example
  name: ubuntu
repositories:
  - role: origin
    type: git
    url: ssh://git@kiota.ch/b19/ubuntu.git
org:
  projectfile:
    image:
      org: b19
      name: ${identity.name}
      series: "{B19_UBUNTU_SERIES}"
      path: ${org}/${name}/${series}
      flatpath: ${org}-${name}-${series}
      tag: latest
    sinks:
      ghcr:
        ref: ghcr.io/damian-buho/${path}:${tag}
      hub:
        ref: docker.io/damianbuho/${flatpath}:${tag}
      broken:
        ref: example.test/${nosuchpart}:${tag}
    publish:
      github:
        push: [ghcr, hub, broken]
        pull: hub
      kiota:
        push: [ghcr]
        pull: ghcr
`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	doc, err := projectfile.Read(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return doc
}

// TestPublishRefsComposePerLowering pins the whole publish plane in one read:
//   - a lowering publishes to the route of the forge it RUNS on — `gha` is GitHub
//     Actions, and a Forgejo lowering takes the slug of the origin host, so the
//     kiota route reaches it without anyone restating the mapping;
//   - two sinks with incompatible path grammars compose from ONE declaration,
//     which is what lets a single archive land nested and flattened;
//   - a `{AXIS}` placeholder survives composition verbatim, for the cell to fill;
//   - a template naming an undeclared part is DROPPED, never published with a
//     hole in it.
func TestPublishRefsComposePerLowering(t *testing.T) {
	r := &Reader{doc: publishDoc(t)}
	got, err := r.publishRefs()
	if err != nil {
		t.Fatalf("publishRefs: %v", err)
	}
	want := map[string][]SinkRef{
		LoweringGHA: {
			{Sink: sinkGHCR, Ref: refGHCR},
			{Sink: "hub", Ref: "docker.io/damianbuho/b19-ubuntu-{B19_UBUNTU_SERIES}:latest"},
		},
		LoweringForgejo: {
			{Sink: sinkGHCR, Ref: refGHCR},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("publishRefs:\n got %#v\nwant %#v", got, want)
	}
}

// TestPublishRefsAbsentWithoutRoutes pins the back-compatible half: a project
// that declares no route composes nothing, so oci-push keeps the single
// OUTPUT_REGISTRY destination every project had before this plane existed.
func TestPublishRefsAbsentWithoutRoutes(t *testing.T) {
	r := &Reader{doc: declaredImageDoc(t)}
	got, err := r.publishRefs()
	if err != nil {
		t.Fatalf("publishRefs: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("publishRefs: got %#v, want none", got)
	}
}

// TestPullRefsComposePerLowering pins the READ plane against the same document:
//   - `pull` is its own declaration, not the first `push` entry and not the highest
//     priority — the github route pushes to ghcr FIRST and still reads from hub;
//   - it composes through the same sink templates, so the audit target inherits
//     whatever path grammar the destination declared (hub is FLAT, ghcr NESTS) with
//     no code aware of either shape;
//   - `{AXIS}` survives for the cell to fill, exactly as on the push side.
func TestPullRefsComposePerLowering(t *testing.T) {
	r := &Reader{doc: publishDoc(t)}
	got, err := r.pullRefs()
	if err != nil {
		t.Fatalf("pullRefs: %v", err)
	}
	want := map[string]SinkRef{
		LoweringGHA:     {Sink: "hub", Ref: "docker.io/damianbuho/b19-ubuntu-{B19_UBUNTU_SERIES}:latest"},
		LoweringForgejo: {Sink: sinkGHCR, Ref: refGHCR},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("pullRefs:\n got %#v\nwant %#v", got, want)
	}
}

// libraryDoc is a project on the SHARED metadata include: it inherits the fleet's
// sinks and routes, and declares no image part, because it builds no container. The
// image namespace carries only the org label the namespace include supplies.
func libraryDoc(t *testing.T) *projectfile.Document {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "projectfile.yaml")
	if err := os.WriteFile(path, []byte(`$schema: https://projectfile.org/schema/v1.json
identity:
  namespace: org.projectfile
  name: core
repositories:
  - role: origin
    type: git
    url: ssh://git@kiota.ch/projectfile/core.git
org:
  projectfile:
    image:
      org: projectfile
    sinks:
      ghcr:
        ref: ghcr.io/damian-buho/${path}:${tag}
      hub:
        ref: docker.io/damianbuho/${flatpath}:${tag}
    publish:
      github:
        push: [ghcr]
        pull: ghcr
      kiota:
        push: [hub]
        pull: hub
`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	doc, err := projectfile.Read(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return doc
}

// TestLibraryComposesNoSinkSilently pins the noise half of the drop rule. A project
// that composes NO destination declares no image at all: the inherited routes never
// applied to it, both planes yield nothing, and the run stays silent. Warning here
// fired once per route per plane on every library in the fleet, and its remedy —
// declare an image part — is advice a library must not take.
func TestLibraryComposesNoSinkSilently(t *testing.T) {
	r := &Reader{doc: libraryDoc(t)}

	var log bytes.Buffer
	genlog.SetOutput(&log)
	defer genlog.SetOutput(os.Stderr)

	pub, err := r.publishRefs()
	if err != nil {
		t.Fatalf("publishRefs: %v", err)
	}
	pull, err := r.pullRefs()
	if err != nil {
		t.Fatalf("pullRefs: %v", err)
	}
	if len(pub) != 0 || len(pull) != 0 {
		t.Fatalf("composed %#v / %#v, want none", pub, pull)
	}
	if strings.Contains(log.String(), "unresolved") {
		t.Fatalf("warned about a project with no image:\n%s", log.String())
	}
}

// TestBrokenSinkAmongWorkingOnesStillWarns pins the half that must stay loud: the
// `broken` sink drops while ghcr and hub compose, so this project DOES publish and
// is reaching one destination fewer than it declared.
func TestBrokenSinkAmongWorkingOnesStillWarns(t *testing.T) {
	r := &Reader{doc: publishDoc(t)}

	var log bytes.Buffer
	genlog.SetOutput(&log)
	defer genlog.SetOutput(os.Stderr)

	if _, err := r.publishRefs(); err != nil {
		t.Fatalf("publishRefs: %v", err)
	}
	if !strings.Contains(log.String(), "broken") {
		t.Fatalf("a dropped sink among working ones went unreported:\n%s", log.String())
	}
}

// TestPullRefsAbsentWithoutRoutes pins the fallback half: no route means no composed
// read destination, so the audit keeps the OUTPUT_REGISTRY prefix every project has.
func TestPullRefsAbsentWithoutRoutes(t *testing.T) {
	r := &Reader{doc: declaredImageDoc(t)}
	got, err := r.pullRefs()
	if err != nil {
		t.Fatalf("pullRefs: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("pullRefs: got %#v, want none", got)
	}
}

// sinkKiota is the origin forge's slug in these fixtures — the first domain label
// of kiota.ch, which is how a route names it. sinkGHCR is the nesting destination
// both route planes reach in publishDoc.
const (
	sinkKiota = "kiota"
	// testUbuntuBaseImageVar / testJsToolsImageVar / hubHead name the images var
	// keys and the flat sink head the sink-composition tests repeat.
	testUbuntuBaseImageVar = "B19_UBUNTU_BASE_IMAGE"
	testJsToolsImageVar    = "D9T_JS_TOOLS_IMAGE"
	hubHead                = "docker.io/damianbuho"
	ghcrHead               = "ghcr.io/damian-buho"
	sinkGHCR               = "ghcr"
	archRISCV              = "riscv64"
	// refGHCR is what publishDoc's ghcr template composes to: a NESTED path with the
	// axis left verbatim for the cell to fill. Both route planes expect this one value,
	// which is the point — push and pull compose a sink identically.
	refGHCR = "ghcr.io/damian-buho/b19/ubuntu/{B19_UBUNTU_SERIES}:latest"
)

// releaseDoc declares two source-code links and a route that releases to BOTH — the
// shape the binaries plane exists for. The two repository paths differ, which is the
// whole reason the coordinates cannot be derived from one another.
func releaseDoc(t *testing.T) *projectfile.Document {
	t.Helper()
	path := filepath.Join(t.TempDir(), "projectfile.yaml")
	if err := os.WriteFile(path, []byte(`$schema: https://projectfile.org/schema/v1.json
identity:
  namespace: org.example
  name: bridge
repositories:
  - role: origin
    type: git
    url: ssh://git@kiota.ch/projectfile/bridge.git
links:
  - type: source-code
    url: https://kiota.ch/projectfile/bridge
  - type: source-code
    url: https://codeberg.org/damian-buho/projectfile-bridge
  - type: homepage
    url: https://projectfile.org
org:
  projectfile:
    publish:
      kiota:
        release: [kiota, codeberg, nowhere]
`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	doc, err := projectfile.Read(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return doc
}

// TestReleaseTargetsResolvePerLowering pins the binaries plane's read:
//   - a route names forge SLUGS, and the coordinates come from the source-code links
//     the project already declares — the URL is never restated;
//   - the repository path is carried, not derived: the two forges spell it
//     differently, which is the fact the whole split exists for;
//   - a destination no link declares is DROPPED, because guessing its path would
//     attach a release to a repository nobody named;
//   - a non-source-code link is not a release destination.
func TestReleaseTargetsResolvePerLowering(t *testing.T) {
	r := &Reader{doc: releaseDoc(t)}
	got, err := r.releaseTargets()
	if err != nil {
		t.Fatalf("releaseTargets: %v", err)
	}
	want := map[string][]ReleaseTarget{
		LoweringForgejo: {
			{Sink: sinkKiota, URL: "https://kiota.ch", Repo: "projectfile/bridge"},
			{Sink: "codeberg", URL: "https://codeberg.org", Repo: "damian-buho/projectfile-bridge"},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("releaseTargets:\n got %#v\nwant %#v", got, want)
	}
}

// TestReleaseTargetsAbsentWithoutRoute pins the no-op half every project relies on
// today: a publish route carrying only `push` releases nowhere new, so the action
// keeps the ambient Forgejo context.
func TestReleaseTargetsAbsentWithoutRoute(t *testing.T) {
	r := &Reader{doc: publishDoc(t)}
	got, err := r.releaseTargets()
	if err != nil {
		t.Fatalf("releaseTargets: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("releaseTargets: got %#v, want none", got)
	}
}

// declaredImagesDoc mirrors m6e's tools/images.yaml: two images declared as PARTS,
// one of them reaching its registry through a `$${VAR}` escape (the parts form's
// spelling for "defer this to the plane that reads the reference"), plus a third
// entry whose `path` names a part nothing declares.
func declaredImagesDoc(t *testing.T) *projectfile.Document {
	t.Helper()
	path := filepath.Join(t.TempDir(), "projectfile.yaml")
	if err := os.WriteFile(path, []byte(`$schema: https://projectfile.org/schema/v1.json
identity:
  namespace: org.example
  name: ubuntu
org:
  projectfile:
    images:
      D9T_JS_TOOLS_IMAGE:
        org: d9t
        name: js-tools
        path: ${org}/${name}
        tag: $${M6E_BASE_IMAGE_DEFAULT_VERSION}
        registry: D9T_DOCKER_REGISTRY
        ref: $${D9T_DOCKER_REGISTRY}/${path}:${tag}
      B19_GO_IMAGE:
        org: b19
        name: go
        path: ${org}/${name}
        tag: dev
        registry: B19_DOCKER_REGISTRY
        ref: $${B19_DOCKER_REGISTRY}/${path}:${tag}
      BROKEN_IMAGE:
        org: d9t
        path: ${org}/${undeclared}
        tag: dev
        registry: D9T_DOCKER_REGISTRY
        ref: $${D9T_DOCKER_REGISTRY}/${path}:${tag}
    ci:
      images:
        HADOLINT_IMAGE: hadolint/hadolint:v2.15.1
`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	doc, err := projectfile.Read(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return doc
}

// TestDeclaredImagesComposeFromParts pins the read that carries the parts migration
// onto the forge plane:
//   - an entry composes through its OWN `ref`, scoped to its OWN subtree, so `${path}`
//     and `${tag}` resolve against that image and not against whichever entry answers
//     first;
//   - a `$${VAR}` escape survives as a single-`$` reference, which is what the render
//     lowers to `${{ vars.VAR }}` — the reference this plane published before the
//     migration, byte for byte;
//   - an entry naming a part nothing declares is DROPPED rather than composed with a
//     hole in it, because a half-composed ref pulls the wrong image.
func TestDeclaredImagesComposeFromParts(t *testing.T) {
	r := &Reader{doc: declaredImagesDoc(t)}
	got, heads, err := r.declaredImages("")
	if err != nil {
		t.Fatalf("declaredImages: %v", err)
	}
	want := map[string]string{
		testJsToolsImageVar: "${D9T_DOCKER_REGISTRY}/d9t/js-tools:${M6E_BASE_IMAGE_DEFAULT_VERSION}",
		"B19_GO_IMAGE":      "${B19_DOCKER_REGISTRY}/b19/go:dev",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("declaredImages:\n got %#v\nwant %#v", got, want)
	}
	if len(heads) != 0 {
		t.Fatalf("declaredImages heads: got %#v, want none (no pull route)", heads)
	}
}

// TestDeclaredImagesAbsentIsNoOp pins the zero-diff property the ~110 projects
// declaring no images depend on: no subtree, no map, and LoadBuild still reads the
// ci-plane entries on their own.
func TestDeclaredImagesAbsentIsNoOp(t *testing.T) {
	r := &Reader{doc: publishDoc(t)}
	got, _, err := r.declaredImages("forgejo")
	if err != nil {
		t.Fatalf("declaredImages: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("declaredImages: got %#v, want none", got)
	}
}

// sinkImagesDoc mirrors the FLEET shape: images declared as parts (path AND
// flatpath, the flip-var tag) plus the damian-buho metadata sinks/routes — a
// prefix-shaped nested sink on the origin forge's pull route and a FLAT (Docker
// Hub) sink on github's, so one fixture pins both layouts.
func sinkImagesDoc(t *testing.T) *projectfile.Document {
	t.Helper()
	path := filepath.Join(t.TempDir(), "projectfile.yaml")
	if err := os.WriteFile(path, []byte(`$schema: https://projectfile.org/schema/v1.json
identity:
  namespace: org.example
  name: go
repositories:
  - role: origin
    type: git
    url: ssh://git@kiota.ch/b19/go.git
org:
  projectfile:
    images:
      B19_UBUNTU_BASE_IMAGE:
        org: b19
        name: ubuntu
        series: $${B19_UBUNTU_SERIES}
        path: ${org}/${name}/${series}
        flatpath: ${org}-${name}-${series}
        tag: $${M6E_BASE_IMAGE_DEFAULT_VERSION}
        registry: B19_DOCKER_REGISTRY
        ref: $${B19_DOCKER_REGISTRY}/${path}:${tag}
      D9T_JS_TOOLS_IMAGE:
        org: d9t
        name: js-tools
        path: ${org}/${name}
        flatpath: ${org}-${name}
        tag: $${M6E_BASE_IMAGE_DEFAULT_VERSION}
        registry: D9T_DOCKER_REGISTRY
        ref: $${D9T_DOCKER_REGISTRY}/${path}:${tag}
    sinks:
      ghcr:
        ref: ghcr.io/damian-buho/${path}:${tag}
      hub:
        ref: docker.io/damianbuho/${flatpath}:${tag}
    publish:
      github:
        push: [ghcr]
        pull: hub
      kiota:
        push: [ghcr, hub]
        pull: ghcr
`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	doc, err := projectfile.Read(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return doc
}

// TestDeclaredImagesComposeFromPullSink pins the forge contract this change builds:
// a declared foreign image composes against the TARGET's pull sink, under the
// entry's OWN parts — nested (`${path}`) and flat (`${flatpath}`) layouts alike —
// and a prefix-shaped template records its literal head for the render's
// vars.SOURCE_DOCKER_REGISTRY redirect. Per-lowering: the forgejo file pulls from
// the ORIGIN forge's route, the gha file from github's.
func TestDeclaredImagesComposeFromPullSink(t *testing.T) {
	r := &Reader{doc: sinkImagesDoc(t)}
	got, heads, err := r.declaredImages("forgejo")
	if err != nil {
		t.Fatalf("declaredImages forgejo: %v", err)
	}
	wantRefs := map[string]string{
		testUbuntuBaseImageVar: "ghcr.io/damian-buho/b19/ubuntu/${B19_UBUNTU_SERIES}:${M6E_BASE_IMAGE_DEFAULT_VERSION}",
		testJsToolsImageVar:    "ghcr.io/damian-buho/d9t/js-tools:${M6E_BASE_IMAGE_DEFAULT_VERSION}",
	}
	if !reflect.DeepEqual(got, wantRefs) {
		t.Fatalf("declaredImages forgejo:\n got %#v\nwant %#v", got, wantRefs)
	}
	wantHeads := map[string]string{
		testUbuntuBaseImageVar: ghcrHead,
		testJsToolsImageVar:    ghcrHead,
	}
	if !reflect.DeepEqual(heads, wantHeads) {
		t.Fatalf("declaredImages forgejo heads:\n got %#v\nwant %#v", heads, wantHeads)
	}

	// github's route pulls from the FLAT sink: same entries, Docker Hub layout.
	got, heads, err = r.declaredImages("gha")
	if err != nil {
		t.Fatalf("declaredImages gha: %v", err)
	}
	wantRefs = map[string]string{
		testUbuntuBaseImageVar: "docker.io/damianbuho/b19-ubuntu-${B19_UBUNTU_SERIES}:${M6E_BASE_IMAGE_DEFAULT_VERSION}",
		testJsToolsImageVar:    "docker.io/damianbuho/d9t-js-tools:${M6E_BASE_IMAGE_DEFAULT_VERSION}",
	}
	if !reflect.DeepEqual(got, wantRefs) {
		t.Fatalf("declaredImages gha:\n got %#v\nwant %#v", got, wantRefs)
	}
	wantHeads = map[string]string{
		testUbuntuBaseImageVar: hubHead,
		testJsToolsImageVar:    hubHead,
	}
	if !reflect.DeepEqual(heads, wantHeads) {
		t.Fatalf("declaredImages gha heads:\n got %#v\nwant %#v", heads, wantHeads)
	}
}

// TestSplitSinkPrefix pins the prefix-shape test the head redirect depends on:
// only `<literal head>/${path}:${tag}` and `…/${flatpath}:${tag}` split (the head
// must be literal — a `${part}` in it names no registry a redirect could stand
// for); every other grammar composes whole and bakes.
func TestSplitSinkPrefix(t *testing.T) {
	for _, tc := range []struct{ tmpl, head, tail string }{
		{"kiota.ch/${path}:${tag}", "kiota.ch", "/${path}:${tag}"},
		{"docker.io/damianbuho/${flatpath}:${tag}", hubHead, "/${flatpath}:${tag}"},
		{"", "", ""},
		{"${host}/${path}:${tag}", "", ""},
		{"ghcr.io/${path}:${tag}/${extra}", "", ""},
		{"ghcr.io/${path}", "", ""},
	} {
		head, tail := splitSinkPrefix(tc.tmpl)
		if head != tc.head || tail != tc.tail {
			t.Fatalf("splitSinkPrefix(%q):\n got (%q, %q)\nwant (%q, %q)", tc.tmpl, head, tail, tc.head, tc.tail)
		}
	}
}

// TestSplitForgeURLReadsEveryTransport pins the decomposition against the transports a
// link is really written in. A slug is the first domain label — the same rule the
// origin's own forge is matched by — and a `.git` suffix or a port never reaches it.
func TestSplitForgeURLReadsEveryTransport(t *testing.T) {
	const repo = "o/r"
	for _, tc := range []struct{ raw, slug, base, repo string }{
		{"https://codeberg.org/o/r", "codeberg", "https://codeberg.org", repo},
		{"ssh://git@kiota.ch/o/r.git", sinkKiota, "https://kiota.ch", repo},
		{"git@github.com:o/r.git", "github", "https://github.com", repo},
		{"https://kiota.ch:3000/o/r/", sinkKiota, "https://kiota.ch:3000", repo},
	} {
		slug, base, repo := splitForgeURL(tc.raw)
		if slug != tc.slug || base != tc.base || repo != tc.repo {
			t.Errorf("splitForgeURL(%q) = %q,%q,%q; want %q,%q,%q",
				tc.raw, slug, base, repo, tc.slug, tc.base, tc.repo)
		}
	}
}

// archDoc writes a projectfile declaring an architecture set, optionally with the
// field omitted entirely — the two states the whole opt-in turns on.
func archDoc(t *testing.T, arch string) *projectfile.Document {
	t.Helper()
	path := filepath.Join(t.TempDir(), "projectfile.yaml")
	body := `$schema: https://projectfile.org/schema/v1.json
identity:
  namespace: org.example
  name: ubuntu
org:
  projectfile:
` + arch
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	doc, err := projectfile.Read(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return doc
}

// TestArchitecturesReadsTheDeclaredList pins the read the arch axis is minted from.
// The LIST case is the one that matters: architecture cannot go through Reader.subtree,
// which ends at core's Extension and yields MAP-valued subtrees only — a list there
// fails its type assertion and reports the field as ABSENT rather than erroring, so a
// wrong read mints nothing and still passes every other test.
func TestArchitecturesReadsTheDeclaredList(t *testing.T) {
	r := &Reader{doc: archDoc(t, "    architecture: [amd64, arm64, riscv64]\n")}
	got, err := r.architectures()
	if err != nil {
		t.Fatalf("architectures: %v", err)
	}
	want := []string{"amd64", "arm64", archRISCV}
	if len(got) != len(want) {
		t.Fatalf("architectures: got %#v, want %#v", got, want)
	}
	for i := range want {
		// BARE arch names: the same token is a matrix value, a tag segment and a
		// deps-file stem, so `linux/` must never be baked in here.
		if got[i] != want[i] {
			t.Errorf("architectures[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestArchitecturesAbsentMintsNothing pins the opt-in: spec §4.8a reads an absent set
// as unconstrained, so a project naming none must yield nil and render the workflow it
// rendered before the field existed.
func TestArchitecturesAbsentMintsNothing(t *testing.T) {
	r := &Reader{doc: archDoc(t, "    image:\n      path: b19/ubuntu\n")}
	got, err := r.architectures()
	if err != nil {
		t.Fatalf("architectures: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("architectures: got %#v, want none", got)
	}
}

// TestGlobalMatrixRejectsWithout pins that `without` is a per-node feature. On the
// global matrix it would mean deleting the axis outright, which is what the axes map
// is for — accepting it there would give one behaviour two spellings, and the wrong
// one would be silent.
func TestGlobalMatrixRejectsWithout(t *testing.T) {
	doc := `{"matrix": {"axes": {"SERIES": ["noble"]}, "without": ["SERIES"]},
	         "nodes": {"ready": {"goal": true, "matrix": true, "needs": {"build-binaries": true}}}}`
	if _, err := Parse([]byte(doc)); err == nil {
		t.Error("global matrix.without: expected a parse error, got nil")
	}
}

// TestGlobalMatrixRejectsPin is the same rule for `pin`: narrowing an axis globally is
// just declaring it with one value, so the second spelling would only ever be the
// mistaken one.
func TestGlobalMatrixRejectsPin(t *testing.T) {
	doc := `{"matrix": {"axes": {"SERIES": ["noble", "resolute"]}, "pin": {"SERIES": "noble"}},
	         "nodes": {"ready": {"goal": true, "matrix": true, "needs": {"build-binaries": true}}}}`
	if _, err := Parse([]byte(doc)); err == nil {
		t.Error("global matrix.pin: expected a parse error, got nil")
	}
}

// TestNodeMatrixPinDecodes pins the object form onto the node: a `pin` block makes the
// node a CELL (like every other matrix object) and survives Parse, which is what the
// make-plane reader mirrors when it treats any `matrix.*` subkey as a cell flag.
func TestNodeMatrixPinDecodes(t *testing.T) {
	st, err := Parse([]byte(`{
	  "matrix": {"axes": {"M6E_ARCH": ["amd64", "arm64"]}},
	  "nodes": {"verified": {"goal": true, "matrix": {"pin": {"M6E_ARCH": "amd64"}}, "needs": {"container-test": true}}}
	}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	n := st.Nodes["verified"]
	if !n.Matrix {
		t.Error("a node carrying matrix.pin must be a cell")
	}
	if got := n.Pin[ArchAxis]; got != "amd64" {
		t.Errorf("pin[%s]: want amd64, got %q", ArchAxis, got)
	}
}

// sinkRefNested is the fleet's own ghcr grammar: a forced account, then the
// image's own path.
const sinkRefNested = ghcrHead + "/${path}:${tag}"

// selfSinkDoc declares one sink reached by BOTH planes, with a `selfref` that
// differs from its `ref` — the shape a project takes when the destination's
// literal account already spells the org its own `${path}` would repeat.
func selfSinkDoc(t *testing.T, ref string) *projectfile.Document {
	t.Helper()
	path := filepath.Join(t.TempDir(), "projectfile.yaml")
	if err := os.WriteFile(path, []byte(`$schema: https://projectfile.org/schema/v1.json
identity:
  namespace: me.example
  name: textlint-server
repositories:
  - role: origin
    type: git
    url: ssh://git@kiota.ch/damian-buho/textlint-server.git
org:
  projectfile:
    image:
      name: ${identity.name}
      org: damian-buho
      path: ${org}/${name}
      flatpath: ${org}-${name}
      tag: latest
    images:
      D9T_JS_TOOLS_IMAGE:
        org: d9t
        name: js-tools
        path: ${org}/${name}
        flatpath: ${org}-${name}
        tag: $${M6E_BASE_IMAGE_DEFAULT_VERSION}
        registry: D9T_DOCKER_REGISTRY
        ref: $${D9T_DOCKER_REGISTRY}/${path}:${tag}
    sinks:
      ghcr:
        ref: `+ref+`
        selfref: ghcr.io/damian-buho/${name}:${tag}
    publish:
      kiota:
        push: [ghcr]
        pull: ghcr
`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	doc, err := projectfile.Read(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return doc
}

// TestSelfRefSplitsTheTwoPlanes is the whole point of `selfref`: one sink, two
// subjects. The project's OWN artifact drops the repeated org segment, while a
// foreign image pulled through the SAME sink keeps its own nesting.
func TestSelfRefSplitsTheTwoPlanes(t *testing.T) {
	r := &Reader{doc: selfSinkDoc(t, sinkRefNested)}

	refs, err := r.publishRefs()
	if err != nil {
		t.Fatalf("publishRefs: %v", err)
	}
	want := []SinkRef{{Sink: sinkGHCR, Ref: "ghcr.io/damian-buho/textlint-server:latest"}}
	if !reflect.DeepEqual(refs[LoweringForgejo], want) {
		t.Fatalf("publishRefs forgejo:\n got %#v\nwant %#v", refs[LoweringForgejo], want)
	}

	images, heads, err := r.declaredImages(LoweringForgejo)
	if err != nil {
		t.Fatalf("declaredImages: %v", err)
	}
	const wantImage = "ghcr.io/damian-buho/d9t/js-tools:${M6E_BASE_IMAGE_DEFAULT_VERSION}"
	if images[testJsToolsImageVar] != wantImage {
		t.Fatalf("declaredImages: got %q, want %q", images[testJsToolsImageVar], wantImage)
	}
	if heads[testJsToolsImageVar] != ghcrHead {
		t.Fatalf("declaredImages head: got %q, want %q", heads[testJsToolsImageVar], ghcrHead)
	}
}

// TestPullSinkSpendingNoImagePathIsRefused pins the loud half: a sink `ref` that
// names neither ${path} nor ${flatpath} cannot address a foreign image, so the
// entry falls back to its own ref instead of composing a well-formed wrong one.
func TestPullSinkSpendingNoImagePathIsRefused(t *testing.T) {
	r := &Reader{doc: selfSinkDoc(t, "ghcr.io/damian-buho/${name}:${tag}")}
	images, heads, err := r.declaredImages(LoweringForgejo)
	if err != nil {
		t.Fatalf("declaredImages: %v", err)
	}
	const wantImage = "${D9T_DOCKER_REGISTRY}/d9t/js-tools:${M6E_BASE_IMAGE_DEFAULT_VERSION}"
	if images[testJsToolsImageVar] != wantImage {
		t.Fatalf("declaredImages: got %q, want %q", images[testJsToolsImageVar], wantImage)
	}
	if len(heads) != 0 {
		t.Fatalf("declaredImages heads: got %#v, want none", heads)
	}
}
