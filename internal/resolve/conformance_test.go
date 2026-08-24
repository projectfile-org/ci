// SPDX-FileCopyrightText: 2026 Damián Búho <damian.buho@proton.me>
//
// SPDX-License-Identifier: MIT

// Replay the spec's org.projectfile.ci conformance vectors against THIS lowering.
//
// This is what makes "ci-resolver is a second, faithful implementation of
// org.projectfile.ci" honest: every behaviour vector the spec ships
// (spec/conformance/ci/*.yaml) is run through the SAME resolver the generator
// uses — the identical vectors m6e replays through make. With no shared lowering
// code, these vectors ARE the fidelity contract ("breaks in make ⟺ breaks in
// github"). The path mirrors conformance-ci.py: materialise the vector's input
// (and any inlined includes) as real projectfile documents, let core merge them
// (base wins, §4.9a) through the library seam, then assert runs/counts/before/
// args (accept) or the refusal reason (reject).
package resolve

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"

	"go.yaml.in/yaml/v3"

	"projectfile.org/projectfile/ci-resolver/internal/ci"
)

type vector struct {
	Description string                   `yaml:"description"`
	Includes    []map[string]interface{} `yaml:"includes"`
	Input       map[string]interface{}   `yaml:"input"`
	Expect      struct {
		Runs   []string          `yaml:"runs"`
		Counts map[string]int    `yaml:"counts"`
		Before [][]string        `yaml:"before"`
		Args   map[string]string `yaml:"args"`
		Reject string            `yaml:"reject"`
	} `yaml:"expect"`
}

// vectorsDir locates spec/conformance/ci relative to this test file, so the
// suite runs from a normal checkout with projectfile/ as the workspace root.
func vectorsDir(t *testing.T) string {
	t.Helper()
	_, self, _, _ := runtime.Caller(0)
	// internal/resolve/ -> ci-resolver -> projectfile
	dir := filepath.Join(filepath.Dir(self), "..", "..", "..",
		"specification", "spec", "conformance", "ci")
	if _, err := os.Stat(dir); err != nil {
		t.Skipf("conformance vectors not found at %s: %v", dir, err)
	}
	return dir
}

// writePF materialises a CI subtree as a minimal valid projectfile document on
// disk (the only language the reader speaks); ci.Load resolves `includes:` and
// deep-merges via core exactly as in production.
func writePF(t *testing.T, path string, ciSubtree map[string]interface{}, includes []string) {
	t.Helper()
	doc := map[string]interface{}{
		"$schema":  "https://projectfile.org/schema/v1.json",
		"identity": map[string]string{"namespace": "org.test", "name": "vec"},
		"license":  map[string]string{"spdx": "MIT"},
		"org":      map[string]interface{}{"projectfile": map[string]interface{}{"ci": ciSubtree}},
	}
	if len(includes) > 0 {
		doc["includes"] = includes
	}
	data, err := yaml.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal projectfile: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestConformance(t *testing.T) {
	dir := vectorsDir(t)

	entries, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var ran int
	for _, path := range entries {
		if filepath.Base(path) == "fixture.schema.json" {
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		var vec vector
		if err := yaml.Unmarshal(raw, &vec); err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		if vec.Input == nil {
			continue // not a vector file (e.g. a schema doc)
		}
		ran++
		name := filepath.Base(path)
		t.Run(name, func(t *testing.T) {
			td := t.TempDir()
			var incPaths []string
			for i, inc := range vec.Includes {
				p := filepath.Join(td, "inc"+itoa(i)+".yaml")
				writePF(t, p, inc, nil)
				incPaths = append(incPaths, "./inc"+itoa(i)+".yaml")
			}
			pfPath := filepath.Join(td, "projectfile.yaml")
			writePF(t, pfPath, vec.Input, incPaths)

			st, loadErr := ci.Load(pfPath)
			var model *Model
			var resErr error
			if loadErr == nil {
				model, resErr = Resolve(st)
			} else {
				resErr = loadErr
			}

			if vec.Expect.Reject != "" {
				assertReject(t, vec.Expect.Reject, resErr)
				return
			}
			if resErr != nil {
				t.Fatalf("resolver rejected an accept vector: %v", resErr)
			}
			assertAccept(t, vec, model)
		})
	}
	if ran == 0 {
		t.Fatal("no conformance vectors ran")
	}
}

func assertReject(t *testing.T, reason string, err error) {
	t.Helper()
	var rej *RejectError
	if !errors.As(err, &rej) {
		t.Fatalf("want reject %q, got err=%v", reason, err)
	}
	// The vector's reason is a prefix (the reader may append an offending name,
	// e.g. a future "dangling-tool (foo)"); a bare "cycle" matches itself.
	if !hasPrefix(rej.Reason, reason) {
		t.Fatalf("want reject reason %q, got %q", reason, rej.Reason)
	}
}

func assertAccept(t *testing.T, vec vector, model *Model) {
	t.Helper()
	if model == nil {
		t.Fatal("accept vector produced a nil model")
	}
	// runs — exact (closed-world) set of tool jobs.
	got := make([]string, 0, len(model.Jobs))
	jobByName := make(map[string]Job, len(model.Jobs))
	for _, j := range model.Jobs {
		got = append(got, j.Name)
		jobByName[j.Name] = j
	}
	if !sameSet(vec.Expect.Runs, got) {
		t.Fatalf("runs: want %v got %v", sortedCopy(vec.Expect.Runs), sortedCopy(got))
	}
	// counts — per-tool multiplicity (the matrix fan-out/fan-in); default 1.
	for tool, want := range vec.Expect.Counts {
		j, ok := jobByName[tool]
		if !ok {
			t.Fatalf("counts[%s]: tool did not run", tool)
		}
		if j.Cells() != want {
			t.Fatalf("counts[%s]: want %d got %d (class=%s)", tool, want, j.Cells(), j.Class)
		}
	}
	// before — [X,Y] means X completes strictly before Y starts, i.e. Y
	// transitively needs X in the job graph.
	for _, pair := range vec.Expect.Before {
		x, y := pair[0], pair[1]
		if _, ok := jobByName[y]; !ok {
			t.Fatalf("before [%s,%s]: %s did not run", x, y, y)
		}
		if !transitivelyNeeds(jobByName, y, x) {
			t.Fatalf("before [%s,%s]: %s does not (transitively) wait on %s; needs=%v",
				x, y, y, x, jobByName[y].Needs)
		}
	}
	// args — the resolved {args:"…"} binding the consumer must deliver.
	for tool, want := range vec.Expect.Args {
		if jobByName[tool].Args != want {
			t.Fatalf("args[%s]: want %q got %q", tool, want, jobByName[tool].Args)
		}
	}
}

// transitivelyNeeds reports whether job `from` reaches `target` through needs.
func transitivelyNeeds(jobs map[string]Job, from, target string) bool {
	seen := make(map[string]bool)
	var walk func(string) bool
	walk = func(n string) bool {
		for _, dep := range jobs[n].Needs {
			if dep == target {
				return true
			}
			if !seen[dep] {
				seen[dep] = true
				if walk(dep) {
					return true
				}
			}
		}
		return false
	}
	return walk(from)
}

// --- tiny stdlib-only helpers (no test deps beyond yaml) --------------------

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	return equalSorted(sortedCopy(a), sortedCopy(b))
}

func equalSorted(a, b []string) bool {
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}
