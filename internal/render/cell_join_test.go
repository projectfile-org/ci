// SPDX-FileCopyrightText: 2026 Damián Búho <damian.buho@proton.me>
//
// SPDX-License-Identifier: MIT

package render

import (
	"strings"
	"testing"

	"projectfile.org/projectfile/ci/v3/internal/ci"
	"projectfile.org/projectfile/ci/v3/internal/resolve"
)

// testGOOS is the Go OS axis key the artifact-join fixtures fan over.
const testGOOS = "GOOS"

// heldArchSubtree is archSubtree over two more axes, with arch exclusions that hit some publish cells and spare one.
const heldArchSubtree = `{
  "image": "b19/java",
  "matrix": {
    "axes": {"DISTRO": ["oracle", "temurin"], "SERIES": ["25", "27"], "M6E_ARCH": ["amd64", "arm64", "riscv64"]},
    "exclude": [{"DISTRO": "oracle", "M6E_ARCH": "riscv64"}, {"DISTRO": "temurin", "SERIES": "25", "M6E_ARCH": "riscv64"}]
  },
  "tools": {"container-build": {"action": "container-build"}, "oci-push": {"action": "oci-push"}},
  "nodes": {
    "image-built": {"matrix": true, "needs": {"container-build": true}},
    "published": {"matrix": {"without": ["M6E_ARCH"]}, "goal": true, "needs": {"image-built": true, "oci-push": true}}
  }
}`

// heldArtifactSubtree drops one producer axis in an artifact join, the generic shape of the same hole.
const heldArtifactSubtree = `{
  "tools": {"go-build": {"run": "go-build.sh", "artifact": "dist"}, "gh-release": {"run": "gh-release.sh"}},
  "nodes": {
    "binaries-built": {"matrix": {"axes": {"GOOS": ["linux", "darwin"], "GOARCH": ["amd64", "riscv64"]},
      "exclude": [{"GOOS": "darwin", "GOARCH": "riscv64"}]}, "needs": {"go-build": true}},
    "binaries-released": {"matrix": {"axes": {"GOOS": ["linux", "darwin"]}}, "goal": true, "needs": {"binaries-built": true, "gh-release": true}}
  }
}`

func buildSubtree(t *testing.T, doc string) Model {
	t.Helper()
	st, err := ci.Parse([]byte(doc))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rm, err := resolve.Resolve(st)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	return Build(rm, st, nil)
}

// includeVar returns the value a job's include row binds for the cell, empty when no row binds it.
func includeVar(j JobView, key string, cell map[string]string) string {
	for _, row := range j.Include {
		match, val := true, ""
		for _, f := range row.Fields {
			if f.Key == key {
				val = f.Value
				continue
			}
			if want, ok := cell[f.Key]; !ok || want != f.Value {
				match = false
			}
		}
		if match && val != "" {
			return val
		}
	}
	return ""
}

// TestPublishWithholdsExcludedArchPerCell pins that each publish cell is told which arches its grid never built, and only those.
func TestPublishWithholdsExcludedArchPerCell(t *testing.T) {
	m := buildSubtree(t, heldArchSubtree)
	pub := jobOf(m, testOCIPush)
	const v = "M6E_ARCH_WITHHELD"
	for _, tc := range []struct {
		distro, series, want string
	}{
		{"oracle", "25", testRiscv64},
		{"oracle", "27", testRiscv64},
		{"temurin", "25", testRiscv64},
		{"temurin", "27", ""},
	} {
		if got := includeVar(pub, v, map[string]string{"DISTRO": tc.distro, "SERIES": tc.series}); got != tc.want {
			t.Errorf("%s/%s %s = %q, want %q", tc.distro, tc.series, v, got, tc.want)
		}
	}
	if len(pub.Include) != 3 {
		t.Errorf("include rows: want one per withholding cell (3), got %d %v", len(pub.Include), pub.Include)
	}
	held := 0
	for _, d := range pub.Downloads {
		if d.HeldVar == "" {
			continue
		}
		held++
		if d.Arch != testRiscv64 || d.HeldVar != v || d.HeldToken != testRiscv64 {
			t.Errorf("held download %+v, want riscv64 on %s", d, v)
		}
	}
	if held != 1 || len(pub.Downloads) != 3 {
		t.Errorf("downloads: want 3 with only riscv64 held, got %d held of %v", held, pub.Downloads)
	}
	if got := steps(m)[testOCIPush].WithholdArches; got != "${{ matrix."+v+" }}" {
		t.Errorf("oci-push WithholdArches = %q", got)
	}
	out, err := Workflow(m, Targets[TargetGHA], ci.Platform{})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"          withhold-arches: ${{ matrix." + v + " }}",
		"!contains(format(',{0},', matrix." + v + "), ',riscv64,')",
	} {
		if !strings.Contains(string(out), want) {
			t.Errorf("rendered workflow missing %q:\n%s", want, out)
		}
	}
}

// TestPublishWithNoExclusionHoldsNothing pins the zero-diff side: a full grid renders no withheld row, gate or input.
func TestPublishWithNoExclusionHoldsNothing(t *testing.T) {
	m := buildSubtree(t, archSubtree)
	out, err := Workflow(m, Targets[TargetGHA], ci.Platform{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "WITHHELD") || strings.Contains(string(out), "withhold-arches") {
		t.Errorf("full grid renders withholding machinery:\n%s", out)
	}
}

// TestArtifactJoinWithholdsExcludedCell pins the generic join: a dropped axis value one kept cell lacks is gated there, not downloaded everywhere.
func TestArtifactJoinWithholdsExcludedCell(t *testing.T) {
	m := buildSubtree(t, heldArtifactSubtree)
	rel := jobOf(m, "gh-release")
	const v = "GOARCH_WITHHELD"
	if got := includeVar(rel, v, map[string]string{testGOOS: "darwin"}); got != testRiscv64 {
		t.Errorf("darwin %s = %q, want riscv64", v, got)
	}
	if got := includeVar(rel, v, map[string]string{testGOOS: testLinux}); got != "" {
		t.Errorf("linux %s = %q, want none", v, got)
	}
	for _, d := range rel.Downloads {
		riscv := strings.Contains(d.Name, "-"+testRiscv64)
		if riscv != (d.HeldVar == v && d.HeldToken == testRiscv64) {
			t.Errorf("download %q held=%q/%q, want held exactly when riscv64", d.Name, d.HeldVar, d.HeldToken)
		}
	}
}

// TestUnfannedJoinDropsWithheldOutright pins that a job fanning nothing never asks for a tar no cell built, with no include row to gate it.
func TestUnfannedJoinDropsWithheldOutright(t *testing.T) {
	doc := strings.Replace(heldArchSubtree, `{"without": ["M6E_ARCH"]}`, `{"without": ["M6E_ARCH", "DISTRO", "SERIES"]}`, 1)
	doc = strings.Replace(doc, `{"DISTRO": "temurin", "SERIES": "25", "M6E_ARCH": "riscv64"}`, `{"DISTRO": "temurin", "M6E_ARCH": "riscv64"}`, 1)
	m := buildSubtree(t, doc)
	pub := jobOf(m, testOCIPush)
	for _, d := range pub.Downloads {
		if d.Arch == testRiscv64 {
			t.Errorf("unfanned publish still downloads %q", d.Name)
		}
	}
	if len(pub.Include) != 0 {
		t.Errorf("unfanned publish carries include rows %v", pub.Include)
	}
}
