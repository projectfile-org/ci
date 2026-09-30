// SPDX-FileCopyrightText: 2026 Damián Búho <damian.buho@proton.me>
//
// SPDX-License-Identifier: MIT

package render

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"projectfile.org/projectfile/ci/internal/ci"
)

// imageVector is one spec/conformance/image fixture.
type imageVector struct {
	Input struct {
		Image map[string]any `yaml:"image"`
		Build map[string]any `yaml:"build"`
		CI    struct {
			Matrix struct {
				Axes map[string][]string `yaml:"axes"`
			} `yaml:"matrix"`
		} `yaml:"ci"`
	} `yaml:"input"`
	Publish struct {
		Version string `yaml:"version"`
		Branch  string `yaml:"branch"`
		Trunk   bool   `yaml:"trunk"`
	} `yaml:"publish"`
	Expect struct {
		Reject string `yaml:"reject"`
		Cells  []struct {
			Cell map[string]string `yaml:"cell"`
			Tags []string          `yaml:"tags"`
		} `yaml:"cells"`
	} `yaml:"expect"`
}

// findUp returns the first ancestor-relative path to rel from this file, or "" when none exists.
func findUp(rel string) string {
	_, self, _, _ := runtime.Caller(0)
	for dir := filepath.Dir(self); dir != filepath.Dir(dir); dir = filepath.Dir(dir) {
		if p := filepath.Join(dir, rel); fileExists(p) {
			return p
		}
	}
	return ""
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// TestImageConformance binds each vector through namingView and expands it with the oci-push tags.sh that consumes it.
func TestImageConformance(t *testing.T) {
	dir := findUp("specification/spec/conformance/image")
	tagsSh := findUp("actions/oci/tags.sh")
	if dir == "" || tagsSh == "" {
		t.Skipf("spec vectors (%q) or actions tags.sh (%q) not found", dir, tagsSh)
	}
	paths, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no vectors in %s: %v", dir, err)
	}
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var vec imageVector
			if err := yaml.Unmarshal(raw, &vec); err != nil {
				t.Fatal(err)
			}
			got, reject := publishVector(t, vec, tagsSh)
			if vec.Expect.Reject != "" {
				if !strings.Contains(reject, vec.Expect.Reject) {
					t.Fatalf("want reject %q, got %q (tags %v)", vec.Expect.Reject, reject, got)
				}
				return
			}
			if reject != "" {
				t.Fatalf("accept vector refused: %s", reject)
			}
			want := map[string][]string{}
			for _, c := range vec.Expect.Cells {
				want[cellKey(vec, c.Cell)] = sortedCopy(c.Tags)
			}
			if len(got) != len(want) {
				t.Fatalf("cells: want %v got %v", want, got)
			}
			for k, tags := range want {
				if strings.Join(got[k], " ") != strings.Join(tags, " ") {
					t.Fatalf("cell [%s]: want %v got %v", k, tags, got[k])
				}
			}
		})
	}
}

// publishVector renders the vector's naming inputs and runs tags.sh once per matrix cell, keyed by part values.
func publishVector(t *testing.T, vec imageVector, tagsSh string) (map[string][]string, string) {
	t.Helper()
	pf := filepath.Join(t.TempDir(), "projectfile.yaml")
	doc := map[string]any{
		"$schema":  "https://projectfile.org/schema/v1.json",
		"identity": map[string]string{"namespace": "org.test", "name": "vec"},
		"license":  map[string]string{"spdx": "MIT"},
		"org":      map[string]any{"projectfile": map[string]any{"image": vec.Input.Image, "build": vec.Input.Build}},
	}
	data, err := yaml.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pf, data, 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := ci.LoadBuild(pf, ci.LoweringForgejo)
	if err != nil || b == nil || b.Naming == nil {
		t.Fatalf("LoadBuild: build=%v err=%v", b, err)
	}
	var axes []ci.Axis
	for k, v := range vec.Input.CI.Matrix.Axes {
		axes = append(axes, ci.Axis{Key: k, Values: v})
	}
	sort.Slice(axes, func(i, j int) bool { return axes[i].Key < axes[j].Key })
	view, err := namingView(b.Naming, axes, axes, nil)
	if err != nil {
		t.Fatalf("namingView: %v", err)
	}
	cells := ci.Cells(axes, nil)
	if len(cells) == 0 {
		cells = []map[string]string{{}}
	}
	got := map[string][]string{}
	for _, cell := range cells {
		variant := strings.Join(view.Variant, "\n")
		for k, v := range cell {
			variant = strings.ReplaceAll(variant, "${{ matrix."+k+" }}", v)
		}
		env := []string{
			"OCI_ACTION=oci-push", "VERSION=" + vec.Publish.Version, "PREVIEW=" + vec.Publish.Branch,
			"HEADS=" + strings.Join(view.Heads, "\n"), "VARIANT=" + variant,
			"VARIANT_CELLS=" + strings.Join(view.Cells, "\n"), "VARIANT_JOIN=" + view.Join,
			"VARIANT_ALIASES=" + view.Aliases, "VARIANT_TAG=" + view.Tag, "VARIANT_BARE=" + view.Bare,
		}
		if vec.Publish.Trunk {
			env = append(env, "PRIMARY="+vec.Publish.Branch)
		}
		cmd := exec.Command("bash", "-c", `source "$1" >/dev/null && printf '%s\n' "${tags[@]}"`, "tags", tagsSh)
		cmd.Env = append(os.Environ(), env...)
		var stderr strings.Builder
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			return got, stderr.String()
		}
		key := variantKey(view, variant)
		got[key] = sortedCopy(strings.Fields(string(out)))
	}
	return got, ""
}

// variantKey is a cell's part values, space-joined, read back from its bound `variant` input.
func variantKey(view *NamingView, variant string) string {
	if len(view.Variant) == 0 {
		return ""
	}
	var vals []string
	for _, line := range strings.Split(variant, "\n") {
		vals = append(vals, strings.Fields(line)[0])
	}
	return strings.Join(vals, " ")
}

// cellKey spells an expected cell in the vector's declared part order.
func cellKey(vec imageVector, cell map[string]string) string {
	variant, _ := vec.Input.Image["variant"].(map[string]any)
	parts, _ := variant["parts"].([]any)
	vals := make([]string, 0, len(parts))
	for _, p := range parts {
		vals = append(vals, cell[p.(string)])
	}
	return strings.Join(vals, " ")
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}
