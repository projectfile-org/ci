// SPDX-FileCopyrightText: 2026 Damián Búho <damian.buho@proton.me>
//
// SPDX-License-Identifier: MIT

package ci

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// loadArtifactDoc writes a projectfile with one binary artifact and one node fanning over it, then loads it.
func loadArtifactDoc(t *testing.T, artifact, matrix string) (*Subtree, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "projectfile.yaml")
	body := `$schema: https://projectfile.org/schema/v1.json
identity:
  namespace: org.example
  name: demo
org:
  projectfile:
    architecture: [amd64, arm64, riscv64]
    operating-system: [linux, darwin]
    artifacts:
      cli:
        kind: binary
        path: dist/demo
` + artifact + `
    ci:
      nodes:
        binaries-are-built:
          matrix: ` + matrix + `
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return Load(path)
}

func TestArtifactMatrixLowersRecordToTargetAxes(t *testing.T) {
	st, err := loadArtifactDoc(t, "        exclude: [{os: darwin, arch: riscv64}]", "{artifact: cli}")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	n := st.Nodes["binaries-are-built"]
	got := make([]string, 0, len(n.Axes))
	for _, a := range n.Axes {
		got = append(got, a.Key+"="+strings.Join(a.Values, ","))
	}
	if want := "TARGET_ARCH=amd64,arm64,riscv64 TARGET_OS=linux,darwin"; strings.Join(got, " ") != want {
		t.Fatalf("axes = %v, want %s", got, want)
	}
	if cells := Cells(n.Axes, n.Excludes); len(cells) != 5 {
		t.Fatalf("cells = %d (%v), want 5 with darwin/riscv64 excluded", len(cells), cells)
	}
}

func TestArtifactMatrixPrefersTheArtifactOwnList(t *testing.T) {
	st, err := loadArtifactDoc(t, "        architecture: [amd64]", "{artifact: cli}")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cells := Cells(st.Nodes["binaries-are-built"].Axes, nil); len(cells) != 2 {
		t.Fatalf("cells = %v, want amd64 on linux and darwin", cells)
	}
}

func TestArtifactMatrixRefusesUnknownArtifactAndMixedForm(t *testing.T) {
	for matrix, want := range map[string]string{
		"{artifact: nope}":                     `no such org.projectfile.artifacts entry`,
		"{artifact: cli, without: [M6E_ARCH]}": `matrix.artifact is exclusive`,
	} {
		if _, err := loadArtifactDoc(t, "", matrix); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("matrix %s: err = %v, want %q", matrix, err, want)
		}
	}
}
