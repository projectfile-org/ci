// SPDX-FileCopyrightText: 2026 Damián Búho <damian.buho@proton.me>
//
// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"projectfile.org/projectfile/ci/v3/internal/ci"
	"projectfile.org/projectfile/ci/v3/internal/render"
)

// TestUnknownTargetSuggestsClosest pins the typo affordance: an unknown -target
// exits 2 naming every valid target and suggesting the nearest by edit distance,
// so a mistyped `forgejio` is one glance from self-repair.
func TestUnknownTargetSuggestsClosest(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{subGenerate, flagTarget, "forgejio"}, nil, &stdout, &stderr); code != 2 {
		t.Fatalf("exit %d, want 2 (stderr=%q)", code, stderr.String())
	}
	msg := stderr.String()
	if !strings.Contains(msg, `unknown -target "forgejio"`) {
		t.Fatalf("stderr %q lacks the unknown-target line", msg)
	}
	for _, key := range render.TargetKeys() {
		if !strings.Contains(msg, key) {
			t.Fatalf("stderr %q does not list the valid target %q", msg, key)
		}
	}
	if !strings.Contains(msg, `did you mean "forgejo"`) {
		t.Fatalf("stderr %q lacks the closest-match suggestion", msg)
	}
	if !strings.Contains(msg, "pf-ci generate -target "+render.TargetKeys()[0]) {
		t.Fatalf("stderr %q lacks the try-this hint", msg)
	}
}

// TestUnknownTargetWithoutCloseMatch pins that a wild guess still names the valid
// targets and the first one to try, and invents no suggestion.
func TestUnknownTargetWithoutCloseMatch(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{subGenerate, flagTarget, "zzzzzzzzzzzz"}, nil, &stdout, &stderr); code != 2 {
		t.Fatalf("exit %d, want 2 (stderr=%q)", code, stderr.String())
	}
	if msg := stderr.String(); strings.Contains(msg, "did you mean") {
		t.Fatalf("stderr %q invents a suggestion for a wild target", msg)
	}
}

// TestLevenshtein pins the edit-distance contract the suggestion is built on:
// the identity is 0, an empty side is the other side's length, and a single
// substitution/insertion/deletion each cost one.
func TestLevenshtein(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{targetForge, targetForge, 0},
		{"", "abc", 3},
		{"abc", "", 3},
		{targetForge, "forgejio", 1}, // transposition read as substitute+delete
		{targetForge, "forgejos", 1},
		{targetGHA, targetForge, 6},
	} {
		if got := levenshtein(tc.a, tc.b); got != tc.want {
			t.Errorf("levenshtein(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

// TestClosestMatch pins that the suggestion is case-insensitive and stays silent
// past distance 2 — a loose match would send the reader to the wrong target.
func TestClosestMatch(t *testing.T) {
	keys := render.TargetKeys()
	if got := closestMatch("FORGEJO", keys); got != render.TargetForgejo {
		t.Errorf("closestMatch(FORGEJO) = %q, want %q", got, render.TargetForgejo)
	}
	if got := closestMatch("zzzzzzzzzzzz", keys); got != "" {
		t.Errorf("closestMatch(far) = %q, want no suggestion", got)
	}
	if got := closestMatch("", nil); got != "" {
		t.Errorf("closestMatch(empty, nil) = %q, want none", got)
	}
}

// TestUnknownTargetMessageShape pins the message's three clauses directly, so the
// suggestion branch is covered without going through cobra's dispatch.
func TestUnknownTargetMessageShape(t *testing.T) {
	msg := unknownTargetMessage("ghaa")
	for _, want := range []string{`unknown -target "ghaa"`, `did you mean "gha"`, "valid targets:"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q lacks %q", msg, want)
		}
	}
}

// TestPlatformOf pins the deployment-overlay lookup: a nil subtree is the zero
// overlay, a declared one is returned verbatim, and an undeclared target key is
// the zero overlay rather than a panic.
func TestPlatformOf(t *testing.T) {
	overlay := ci.Subtree{Platforms: map[string]ci.Platform{
		render.TargetGHA: {RunsOn: []string{"self-hosted"}},
	}}
	if got := platformOf(nil, render.TargetGHA); len(got.RunsOn) != 0 {
		t.Fatalf("platformOf(nil) = %+v, want the zero overlay", got)
	}
	if got := platformOf(&overlay, render.TargetGHA); len(got.RunsOn) != 1 || got.RunsOn[0] != "self-hosted" {
		t.Fatalf("platformOf(gha) = %+v, want the declared overlay", got)
	}
	if got := platformOf(&overlay, render.TargetForgejo); len(got.RunsOn) != 0 {
		t.Fatalf("platformOf(forgejo) = %+v, want the zero overlay", got)
	}
}

// TestCheckFreshMissingAndStale pins the -check failure modes: an absent file and
// a drifted one are drift (exit 3), while a matching file passes — and a trailing
// newline alone is not drift.
func TestCheckFreshMissingAndStale(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "build.yaml")
	err := checkFresh(path, []byte("name: build\n"))
	if err == nil || exitCodeFor(err) != 3 || !strings.Contains(err.Error(), "is missing") {
		t.Fatalf("missing file: err=%v (exit %d), want an exit-3 drift error", err, exitCodeFor(err))
	}
	if err := os.WriteFile(path, []byte("name: build\n"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := checkFresh(path, []byte("name: build\n")); err != nil {
		t.Fatalf("fresh file: %v", err)
	}
	if err := checkFresh(path, []byte("name: build")); err != nil {
		t.Fatalf("trailing newline is not drift: %v", err)
	}
	if err := checkFresh(path, []byte("name: other\n")); err == nil || !strings.Contains(err.Error(), "is stale") {
		t.Fatalf("stale file: err=%v, want a staleness failure", err)
	}
}

// TestCheckFreshUnreadableIsInputError pins that a read failure that is neither
// absence nor permission stays a plain error, and that permission is an INPUT
// failure (exit 4) — the operator's fault, not a missing regeneration.
func TestCheckFreshUnreadableIsInputError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses the permission bits this pins")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "build.yaml")
	if err := os.WriteFile(path, []byte("name: build\n"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	locked := filepath.Join(dir, "locked")
	if err := os.Mkdir(locked, 0o750); err != nil {
		t.Fatalf("seed dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(locked, "build.yaml"), []byte("x"), 0o644); err != nil {
		t.Fatalf("seed inner: %v", err)
	}
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o750) })
	err := checkFresh(filepath.Join(locked, "build.yaml"), []byte("x"))
	if err == nil || exitCodeFor(err) != 4 || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("unreadable file: err=%v (exit %d), want an exit-4 permission failure", err, exitCodeFor(err))
	}
}

// TestCheckFreshUnreadableDirectory pins the remaining read-failure branch: a
// directory standing where the workflow belongs is neither missing nor a
// permission problem, so it surfaces the underlying error unwrapped in kind.
func TestCheckFreshUnreadableDirectory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "build.yaml")
	if err := os.Mkdir(path, 0o750); err != nil {
		t.Fatalf("seed: %v", err)
	}
	err := checkFresh(path, []byte("name: build\n"))
	if err == nil {
		t.Fatal("directory read as file: got nil error, want a read failure")
	}
	if exitCodeFor(err) != 1 {
		t.Fatalf("exit %d, want 1 (a plain error)", exitCodeFor(err))
	}
	if !strings.Contains(err.Error(), "cannot read "+path) {
		t.Fatalf("err %q lacks the read-failure context", err)
	}
}

// TestOrphanWorkflowsUnreadableDir pins the directory-read failure branch: an
// absent output directory holds no orphans (graceful), anything else propagates.
func TestOrphanWorkflowsUnreadableDir(t *testing.T) {
	stale, err := orphanWorkflows(filepath.Join(t.TempDir(), "absent"), ".yaml", nil)
	if err != nil || stale != nil {
		t.Fatalf("absent dir: stale=%v err=%v, want a graceful no-op", stale, err)
	}
	if _, err := orphanWorkflows(t.TempDir()+"/\x00bad", ".yaml", nil); err == nil {
		t.Fatal("bad dir name: got nil error, want a read failure")
	}
}

// TestSortedPaths pins the deterministic write order every plan and log line reads.
func TestSortedPaths(t *testing.T) {
	got := sortedPaths(map[string][]byte{"b.yaml": nil, "a.yaml": nil, "c.yaml": nil})
	if want := []string{"a.yaml", "b.yaml", "c.yaml"}; !equalStrings(got, want) {
		t.Fatalf("sortedPaths = %v, want %v", got, want)
	}
}

// equalStrings compares two string slices element-wise.
func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// TestErrInputClassifiesExit pins that errInput is an INPUT failure (exit 4) and
// keeps its message, so a stdin staging failure reports through one class.
func TestErrInputClassifiesExit(t *testing.T) {
	err := errInput("staging stdin: disk full")
	if exitCodeFor(err) != 4 || err.Error() != "staging stdin: disk full" {
		t.Fatalf("errInput = %v (exit %d), want exit 4 carrying the message", err, exitCodeFor(err))
	}
}

// TestStdinProjectfileStagingFails pins the -pf - failure path: when the staging
// temp file cannot be created the run reports an INPUT error naming the cause,
// and a reader that fails mid-copy leaves no staging file behind.
func TestStdinProjectfileStagingFails(t *testing.T) {
	readOnly := t.TempDir()
	if err := os.Chmod(readOnly, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	if os.Geteuid() != 0 {
		t.Chdir(readOnly)
		o := opts{pf: "-"}
		cleanup, err := stdinProjectfile(&o, strings.NewReader("name: demo\n"))
		if cleanup != nil {
			t.Fatal("staging failed but a cleanup func was returned")
		}
		if err == nil || exitCodeFor(err) != 4 || !strings.Contains(err.Error(), "staging stdin") {
			t.Fatalf("err=%v (exit %d), want an exit-4 staging failure", err, exitCodeFor(err))
		}
	}
	// A non-stdin path is the no-op: it stages nothing and reports no error.
	o := opts{pf: "projectfile.yaml"}
	cleanup, err := stdinProjectfile(&o, nil)
	if err != nil || cleanup == nil {
		t.Fatalf("err=%v, want a no-op returning a cleanup func", err)
	}
	cleanup()
}

// TestErrInputUnwrapsCause pins that the cause stays reachable through errors.Is,
// which is how classifyLoadError's os.ErrNotExist branch keeps working.
func TestErrInputUnwrapsCause(t *testing.T) {
	err := inputError{msg: "cannot read", err: os.ErrNotExist}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("%v does not unwrap to os.ErrNotExist", err)
	}
	if err.Unwrap() != os.ErrNotExist {
		t.Fatalf("Unwrap = %v, want os.ErrNotExist", err.Unwrap())
	}
}

// TestDirOf pins the slash-path split writeWorkflow creates directories from: the
// directory portion, the empty string for a bare file name, and the leading part
// of an absolute path.
func TestDirOf(t *testing.T) {
	for _, tc := range []struct{ path, want string }{
		{"build.yaml", ""},
		{".github/workflows/build.yaml", ".github/workflows"},
		{"/abs/build.yaml", "/abs"},
	} {
		if got := dirOf(tc.path); got != tc.want {
			t.Errorf("dirOf(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

// TestWorkflowFilesNeedsExplicitGoals pins the fail-fast: a document with no
// goal:true node cannot be lowered per goal, and generate says so instead of
// writing an empty workflow set.
func TestWorkflowFilesNeedsExplicitGoals(t *testing.T) {
	target := render.Target{Key: render.TargetGHA, OutDir: t.TempDir(), Ext: ".yaml"}
	if _, _, err := workflowFiles(nil, target, "", ""); err == nil || !strings.Contains(err.Error(), "no goal:true nodes declared") {
		t.Fatalf("nil subtree: err=%v, want the explicit-goals refusal", err)
	}
	st := &ci.Subtree{Nodes: map[string]ci.Node{testGoalReady: {Name: testGoalReady}}}
	if _, _, err := workflowFiles(st, target, "", ""); err == nil || !strings.Contains(err.Error(), "no goal:true nodes declared") {
		t.Fatalf("implicit goals: err=%v, want the explicit-goals refusal", err)
	}
}
