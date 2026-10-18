// SPDX-FileCopyrightText: 2026 Damián Búho <damian.buho@proton.me>
//
// SPDX-License-Identifier: MIT

package render

import (
	"encoding/json"
	"strings"
	"testing"

	"projectfile.org/projectfile/ci/v3/internal/ci"
)

// Local constants for the literals these suites repeat (goconst).
const (
	axisSeriesKey  = "SERIES"
	axisArchKey    = "M6E_ARCH"
	testTagRefType = "github.ref_type == 'tag'"
	testEventRef   = "github.ref == 'refs/heads/dev'"
)

// TestTargetKeysAreSorted pins the listing every error and the -target help read:
// the registered targets, sorted, so the same document always produces the same
// message and the same flag description.
func TestTargetKeysAreSorted(t *testing.T) {
	got := TargetKeys()
	want := []string{TargetForgejo, TargetGHA, TargetLefthook}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("TargetKeys() = %v, want %v", got, want)
	}
	for _, k := range got {
		if _, ok := Targets[k]; !ok {
			t.Errorf("TargetKeys lists %q, which Targets does not carry", k)
		}
	}
	if len(got) != len(Targets) {
		t.Fatalf("TargetKeys has %d entries, Targets %d", len(got), len(Targets))
	}
}

// TestModelJSONRoundTrips pins the `resolve` contract: the model serialises to
// indented JSON carrying the goals, and the bytes are valid JSON a template can
// read back.
func TestModelJSONRoundTrips(t *testing.T) {
	m, _ := mustModel(t)
	out, err := m.JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	for _, want := range []string{`"name": "ready-to-publish"`, `"jobs"`, "image-built"} {
		if !strings.Contains(string(out), want) {
			t.Fatalf("JSON lacks %q:\n%s", want, out)
		}
	}
	if !strings.Contains(string(out), "\n  \"") {
		t.Fatalf("JSON is not indented as documented:\n%s", out)
	}
}

// TestAxisMapPreservesAxisOrder pins why AxisMap has its own marshaller: a plain
// map would re-sort, and the matrix block in a generated workflow must stay
// byte-stable across runs.
func TestAxisMapPreservesAxisOrder(t *testing.T) {
	a := AxisMap{{Key: "ZETA", Values: []string{"z"}}, {Key: "ALPHA", Values: []string{"a", "b"}}}
	out, err := a.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}
	want := `{"ZETA":["z"],"ALPHA":["a","b"]}`
	if string(out) != want {
		t.Fatalf("MarshalJSON = %s, want %s", out, want)
	}
	if got, err := json.Marshal(a); err != nil || string(got) != want {
		t.Fatalf("json.Marshal(AxisMap) = %s (%v), want %s", got, err, want)
	}
	// An empty axis set is the empty object, not `null`.
	if got, _ := (AxisMap{}).MarshalJSON(); string(got) != "{}" {
		t.Fatalf("empty AxisMap = %s, want {}", got)
	}
}

// TestCIContextWithoutReleaseTag pins the plain project: every CI key lowers to
// its own ref expression, and nothing reads a release-tag node.
func TestCIContextWithoutReleaseTag(t *testing.T) {
	for key, want := range ciContextExpr {
		for _, b := range []*ci.Build{nil, {}, {ReleaseTag: false}} {
			if got := ciContext(b, key); got != want {
				t.Errorf("ciContext(%v, %q) = %q, want %q", b, key, got, want)
			}
		}
	}
}

// TestCIContextReadsTheDetectedTag pins the stable-branch project: the version and
// the preview are read off the tag the release-tag node detected, because on main
// the git ref carries no version to stamp.
func TestCIContextReadsTheDetectedTag(t *testing.T) {
	b := &ci.Build{ReleaseTag: true}
	if got := ciContext(b, ci.CIKeyVersion); !strings.Contains(got, ReleaseTagNode) {
		t.Errorf("version = %q, want it read off the %s node", got, ReleaseTagNode)
	}
	preview := ciContext(b, ci.CIKeyPreview)
	if !strings.Contains(preview, ReleaseTagNode) || !strings.Contains(preview, "github.ref_name") {
		t.Errorf("preview = %q, want the detected tag gating the branch name", preview)
	}
	// Any other key keeps its own expression: the release-tag overlay is not blanket.
	for key, want := range ciContextExpr {
		if key == ci.CIKeyVersion || key == ci.CIKeyPreview {
			continue
		}
		if got := ciContext(b, key); got != want {
			t.Errorf("ciContext(release, %q) = %q, want %q untouched", key, got, want)
		}
	}
}

// TestReleaseTagValueAndIf pins the paired release decision: on a stable-branch
// project the version comes from the detected tag and the run releases only when
// that tag is non-empty; every other project releases on any tag push.
func TestReleaseTagValueAndIf(t *testing.T) {
	for _, b := range []*ci.Build{nil, {}, {ReleaseTag: true}} {
		got := releaseTagValue(b)
		if b != nil && b.ReleaseTag {
			if !strings.Contains(got, ReleaseTagNode) {
				t.Errorf("release-tag value = %q, want the detected tag", got)
			}
			if cond := releaseIf(b); !strings.Contains(cond, ReleaseTagNode) || !strings.Contains(cond, "!= ''") {
				t.Errorf("release-if = %q, want the detected tag being non-empty", cond)
			}
			continue
		}
		if !strings.Contains(got, "github.ref_name") {
			t.Errorf("tag value = %q, want the pushed ref", got)
		}
		if cond := releaseIf(b); cond != testTagRefType {
			t.Errorf("release-if = %q, want the tag-ref test", cond)
		}
	}
}

// TestComposeImage pins the self-image composition: an image already carrying a
// tag is passed through untouched, and one without gets the registry prefix and the
// cell tag — except with no registry at all, where the bare image is the whole
// reference.
func TestComposeImage(t *testing.T) {
	const tagged = "reg.example/app:1.2.3"
	if got := composeImage("ghcr.io/org", tagged, "", ""); got != tagged {
		t.Errorf("tagged image = %q, want it passed through", got)
	}
	cell, arch := testResolute, testAmd64
	bare := composeImage("", "org/app", cell, arch)
	if strings.HasPrefix(bare, "${{") {
		t.Errorf("registry-less image = %q, want the bare name with its tag", bare)
	}
	if !strings.Contains(bare, "org/app:") {
		t.Errorf("registry-less image = %q, want the tag appended", bare)
	}
	full := composeImage("ghcr.io/org", "org/app", cell, arch)
	if !strings.HasPrefix(full, "${{ vars.") || !strings.Contains(full, "}}/org/app:") {
		t.Errorf("registry image = %q, want the vars-scoped prefix", full)
	}
	if !strings.Contains(full, cell) || !strings.Contains(full, arch) {
		t.Errorf("registry image = %q, want the cell and arch in the tag", full)
	}
}

// TestComposeImageCellBoundTag pins that the self-image tag names the cell the job
// fans over and the arch it builds for, so a matrix job references its own artifact
// and never a sibling's.
func TestComposeImageCellBoundTag(t *testing.T) {
	onlyArch := composeImage("", "org/app", "", "riscv64")
	if !strings.Contains(onlyArch, "riscv64") {
		t.Fatalf("arch-only tag = %q, want the arch named", onlyArch)
	}
	if strings.Contains(onlyArch, "matrix.") {
		t.Fatalf("empty cell still emitted a matrix ref: %q", onlyArch)
	}
}

// TestVariantCellWithoutVariant pins the graceful no-op: a project declaring no
// variant contributes no variant segment, so every render is byte-identical to the
// one before the field existed.
func TestVariantCellWithoutVariant(t *testing.T) {
	for _, b := range []*ci.Build{nil, {}, {Naming: &ci.ImageNaming{}}, {Naming: &ci.ImageNaming{Variant: &ci.Variant{}}}} {
		if got := variantCell(b, nil); got != "" {
			t.Errorf("variantCell(%+v) = %q, want empty", b, got)
		}
	}
}

// TestVariantCellJoinsDeclaredParts pins the positive half: each part is filled from
// the cell's own axis value and the parts join with the declared separator.
func TestVariantCellJoinsDeclaredParts(t *testing.T) {
	b := &ci.Build{Naming: &ci.ImageNaming{Variant: &ci.Variant{
		Join:  "-",
		Parts: []ci.VariantPart{{Name: "series", Template: "{SERIES}"}, {Name: "distro", Template: "{DISTRO}"}},
	}}}
	got := variantCell(b, []ci.Axis{{Key: axisSeriesKey, Values: []string{testResolute}}})
	if got != "{SERIES}-{DISTRO}" {
		t.Fatalf("variantCell = %q, want the parts joined with the declared separator", got)
	}
	// The DISTRO axis is absent from this job, so its placeholder survives verbatim:
	// the composition names every axis, and the cell substitutes the ones it fans over.
	if strings.Contains(got, testResolute) {
		t.Fatalf("variantCell = %q, want the axis PLACEHOLDER, not its value", got)
	}
}

// TestRowsMatchingFiltersToThePinnedValue pins the serialise filter: a row naming
// the serialised axis survives only for ITS value, a row silent about the axis
// applies to every link, and rows matching nothing yield no rows at all — so an
// include row for another value cannot mint a cell the chain took apart.
func TestRowsMatching(t *testing.T) {
	rows := []MatrixRowView{
		{Fields: []KVView{{Key: axisSeriesKey, Value: testResolute}}},
		{Fields: []KVView{{Key: axisSeriesKey, Value: testNoble}}},
		{Fields: []KVView{{Key: axisArchKey, Value: testAmd64}}},
		{},
	}
	got := rowsMatching(rows, axisSeriesKey, testResolute)
	if len(got) != 3 {
		t.Fatalf("rowsMatching = %+v, want the resolute row, the arch row and the silent row", got)
	}
	if got[0].Fields[0].Value != testResolute || got[1].Fields[0].Key != axisArchKey || len(got[2].Fields) != 0 {
		t.Fatalf("rowsMatching kept the wrong rows: %+v", got)
	}
	if got := rowsMatching(rows, axisSeriesKey, "focal"); len(got) != 2 {
		t.Fatalf("rowsMatching(unmatched value) = %+v, want the two rows silent about the axis", got)
	}
	if got := rowsMatching(nil, axisSeriesKey, testResolute); got != nil {
		t.Fatalf("rowsMatching(nil) = %+v, want nil", got)
	}
	if got := rowsMatching([]MatrixRowView{{Fields: []KVView{{Key: axisSeriesKey, Value: testNoble}}}}, axisSeriesKey, testResolute); got != nil {
		t.Fatalf("rowsMatching(nothing matches) = %+v, want nil", got)
	}
}

// TestWithEventClauses pins the gate composition: an event alone, a gate alone, and
// both together (parenthesised so the forge parses the conjunction as intended).
func TestWithEvent(t *testing.T) {
	const gate = "success()"
	const event = testEventRef
	for _, tc := range []struct{ event, gate, want string }{
		{"", gate, gate},               // no event: the gate stands alone
		{event, "", "(" + event + ")"}, // no gate: the event stands alone, parenthesised
		{event, gate, "(" + event + ") && " + gate},
		{"", "", ""}, // neither: no clause at all
	} {
		if got := withEvent(tc.event, tc.gate); got != tc.want {
			t.Errorf("withEvent(%q, %q) = %q, want %q", tc.event, tc.gate, got, tc.want)
		}
	}
}
