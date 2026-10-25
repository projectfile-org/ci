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

// loadDerivedExcludeDoc writes a two-axis container project with the given architecture block and global exclusions, then loads it.
func loadDerivedExcludeDoc(t *testing.T, architecture, exclude string) (*Subtree, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "projectfile.yaml")
	body := `$schema: https://projectfile.org/schema/v1.json
identity:
  namespace: org.example
  name: demo
org:
  projectfile:
` + architecture + `
    ci:
      matrix:
        axes:
          DISTRO: [oracle, temurin]
          SERIES: ["25", "27"]
        exclude: ` + exclude + `
      tools:
        container-build: {action: container-build}
      nodes:
        image-built: {goal: true, matrix: true, needs: {container-build: true}}
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return Load(path)
}

const derivedArches = "    architecture: [amd64, arm64, riscv64]"

// TestExcludeNamesDerivedArchAxis pins that a row may combine authored axes with the minted arch axis, and removes exactly those cells.
func TestExcludeNamesDerivedArchAxis(t *testing.T) {
	st, err := loadDerivedExcludeDoc(t, derivedArches,
		`[{DISTRO: oracle, M6E_ARCH: riscv64}, {DISTRO: temurin, SERIES: "25", M6E_ARCH: riscv64}]`)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	cells := Cells(st.Axes, st.Excludes)
	if len(cells) != 9 {
		t.Fatalf("cells: want 2x2x3 - 3 = 9, got %d %v", len(cells), cells)
	}
	riscv := 0
	for _, c := range cells {
		if c[ArchAxis] != "riscv64" {
			continue
		}
		riscv++
		if c["DISTRO"] != "temurin" || c["SERIES"] != "27" {
			t.Errorf("riscv64 survived in %v, want only temurin/27", c)
		}
	}
	if riscv != 1 {
		t.Errorf("riscv64 cells: want 1, got %d", riscv)
	}
}

// TestExcludeAuthoredOnlyRowsStillParseEarly pins that rows naming only authored axes keep their parse-time meaning beside a deferred one.
func TestExcludeAuthoredOnlyRowsStillParseEarly(t *testing.T) {
	st, err := loadDerivedExcludeDoc(t, derivedArches, `[{DISTRO: oracle, SERIES: "25"}, {DISTRO: temurin, M6E_ARCH: amd64}]`)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := len(Cells(st.Axes, st.Excludes)); got != 12-3-2 {
		t.Errorf("cells: want 12 - 3 (oracle/25) - 2 (temurin/amd64) = 7, got %d", got)
	}
}

// TestExcludeDerivedAxisRejects pins the fail-closed edges: an underived arch axis and an undeclared arch value are load errors.
func TestExcludeDerivedAxisRejects(t *testing.T) {
	for name, tc := range map[string]struct{ architecture, exclude, want string }{
		"no-architecture":  {"", `[{DISTRO: oracle, M6E_ARCH: riscv64}]`, ArchAxis},
		"undeclared-value": {derivedArches, `[{DISTRO: oracle, M6E_ARCH: s390x}]`, "s390x"},
		"typo-axis":        {derivedArches, `[{DISTRO: oracle, M6E_ARCHX: riscv64}]`, "M6E_ARCHX"},
	} {
		_, err := loadDerivedExcludeDoc(t, tc.architecture, tc.exclude)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: want an error naming %q, got %v", name, tc.want, err)
		}
	}
}
