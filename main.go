// SPDX-FileCopyrightText: 2026 Damián Búho <damian.buho@proton.me>
//
// SPDX-License-Identifier: MIT

// Command pf-ci lowers an org.projectfile.ci signal DAG into vendor CI workflows.
//
// It is the SECOND lowering of the projectfile CI spec (m6e is the first, to
// make). It stands on core + the spec: core (imported as a library) merges
// includes and projects the subtree; pf-ci computes the graph (closure, matrix
// partition, abstract-node contraction) and renders it to GitHub Actions /
// Forgejo Actions YAML.
//
//	pf-ci resolve  [-pf PATH]                 # emit the job-model JSON
//	pf-ci generate -target gha [-o PATH]      # render the workflow YAML
//	pf-ci generate -target gha -check         # fail if the committed file drifted
//
// The freshness gate (-check) is itself meant to run AS a DAG tool, so it fires
// under both m6e and the generated workflow — the "codegen can't silently lie"
// guarantee.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"kiota.ch/projectfile/core/v2/pkg/genlog"
	"projectfile.org/projectfile/ci/internal/ci"
	"projectfile.org/projectfile/ci/internal/render"
	"projectfile.org/projectfile/ci/internal/resolve"
)

// version is stamped at build time (-ldflags "-X main.version=…"); "dev" on a
// plain `go build`. Surfaced via `pf-ci version` so a committed workflow can be
// traced to the resolver that produced it.
var version = "dev"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "resolve":
		err = cmdResolve(os.Args[2:])
	case "generate":
		err = cmdGenerate(os.Args[2:])
	case "version", "--version", "-v":
		fmt.Println("pf-ci", version)
		return
	case "-h", "--help", "help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "pf-ci: unknown command %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "pf-ci: %v\n", err)
		genlog.FlushDebug()
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `pf-ci — lower org.projectfile.ci to vendor CI workflows

Usage:
  pf-ci resolve  [-pf PATH] [-verbose]
  pf-ci generate -target gha|forgejo|lefthook [-pf PATH] [-o PATH] [-check] [-verbose]
  pf-ci version

resolve   Emit the vendor-neutral job model as JSON (the template input).
generate  Render the job model to a target's workflow YAML, or (-check) verify
          the committed workflow still matches the source DAG.
version   Print the resolver version (the codegen provenance stamp).

Environment:
  PF_CI_WORKFLOW_EXT   Committed-workflow file extension, ".yaml" or ".yml"
                       (default: ".yaml"). Set to ".yml" to render the legacy
                       form; m6e’s make act-* derives its default from the same
                       var, so one knob keeps the generator and consumer in sync.
  PF_CLI_VERBOSE        Same effect as -verbose, for a run that cannot pass flags.
`)
}

// verboseFromFlagOrEnv resolves the effective verbose setting: the -verbose
// flag, or PF_CLI_VERBOSE=1 when the flag was not set — the same two-source
// rule pf-cli and pf-bridge apply, so one env var reaches every projectfile
// binary uniformly.
func verboseFromFlagOrEnv(flagVal bool) bool {
	if flagVal {
		return true
	}
	v, _ := strconv.ParseBool(os.Getenv("PF_CLI_VERBOSE"))
	return v
}

// lower runs the read+lower pipeline on an already-target-resolved subtree:
// resolve the graph, then join the tool manifests into the renderable model.
// Callers decide whether the subtree is the neutral one (`resolve`) or a
// target-pruned view (`generate`) — lowering itself is target-agnostic.
// pfPath is forwarded to ci.LoadBuild so the resolver can read the run-image map
// (ci.images) + container-build args (build.args). No target here, so foreign
// images keep their own refs (sink composition is a per-lowering fact).
func lower(st *ci.Subtree, pfPath string) (render.Model, error) {
	rm, err := resolve.Resolve(st)
	if err != nil {
		return render.Model{}, err
	}
	b, err := ci.LoadBuild(pfPath, "")
	if err != nil {
		return render.Model{}, fmt.Errorf("ci: reading build inputs: %w", err)
	}
	m := render.Build(rm, st, b)
	return m, m.Err
}

// platformOf returns the deployment overlay for a target (zero value when the
// project declares none — the overlay is optional and purely additive).
func platformOf(st *ci.Subtree, key string) ci.Platform {
	if st == nil {
		return ci.Platform{}
	}
	return st.Platforms[key]
}

func cmdResolve(args []string) error {
	fs := flag.NewFlagSet("resolve", flag.ContinueOnError)
	pf := fs.String("pf", "", "projectfile path (default: auto-discover)")
	verbose := fs.Bool("verbose", false, "show operational log lines (e.g. interpolation lookups); also PF_CLI_VERBOSE=1")
	if err := fs.Parse(args); err != nil {
		return err
	}
	genlog.SetVerbose(verboseFromFlagOrEnv(*verbose))
	// resolve emits the VENDOR-NEUTRAL model: no target, so no membership prune —
	// every declared tool appears (it is the shared contract, not one vendor's view).
	st, err := ci.Load(*pf)
	if err != nil {
		return err
	}
	model, err := lower(st, *pf)
	if err != nil {
		return err
	}
	out, err := model.JSON()
	if err != nil {
		return err
	}
	fmt.Println(string(out))
	return nil
}

func cmdGenerate(args []string) error {
	fs := flag.NewFlagSet("generate", flag.ContinueOnError)
	targetKey := fs.String("target", "", "render target: "+fmt.Sprint(render.TargetKeys()))
	pf := fs.String("pf", "", "projectfile path (default: auto-discover)")
	out := fs.String("o", "", "output directory for the per-goal workflow files (default: the target's vendor dir); a single path for lefthook")
	check := fs.Bool("check", false, "freshness gate: exit non-zero if the committed workflow drifted")
	verbose := fs.Bool("verbose", false, "show operational log lines (e.g. interpolation lookups); also PF_CLI_VERBOSE=1")
	if err := fs.Parse(args); err != nil {
		return err
	}
	genlog.SetVerbose(verboseFromFlagOrEnv(*verbose))
	target, ok := render.Targets[*targetKey]
	if !ok {
		return fmt.Errorf("unknown -target %q (have %v)", *targetKey, render.TargetKeys())
	}
	st, err := ci.Load(*pf)
	if err != nil {
		return err
	}
	// The git-hook lowering is a NODE-level projection (the hook nodes are non-goals
	// that never become cloud jobs), so it bypasses the per-goal job pipeline entirely
	// and renders a single committed file.
	if target.Key == render.TargetLefthook {
		rendered, err := render.Lefthook(st, target)
		if err != nil {
			return err
		}
		dest := *out
		if dest == "" {
			dest = target.OutPath
		}
		if *check {
			return checkFresh(dest, rendered)
		}
		if err := writeWorkflow(dest, rendered); err != nil {
			return err
		}
		genlog.Success(fmt.Sprintf("pf-ci: wrote %s (%d hooks)", dest, len(render.HookNodes(st))))
		return nil
	}

	// Per-goal workflow files: one `<dir>/<goal><Ext>` per goal:true node (Ext defaults
	// to .yaml; see render.workflowExt). Strict — no goals means there is nothing to
	// name a file after (the inferred-sink fallback is deliberately not used here, so a
	// `test` sink never becomes a workflow).
	files, dir, err := workflowFiles(st, target, *out, *pf)
	if err != nil {
		return err
	}
	if *check {
		return checkFreshDir(dir, target.Ext, files)
	}
	return writeWorkflowDir(dir, target.Ext, files)
}

// workflowFiles renders every goal of the subtree to its own committed workflow path,
// returning the path→bytes map and the resolved output directory. The build inputs
// (ci.images / build.args) are read ONCE per target — the pull route that composes
// foreign images is a fact of the target's forge — and shared across goals. dir is
// the -o override when set, else the target's vendor-fixed workflows directory.
func workflowFiles(st *ci.Subtree, target render.Target, outDir, pfPath string) (map[string][]byte, string, error) {
	if st == nil || !st.GoalsExplicit {
		return nil, "", fmt.Errorf("generate %s: no goal:true nodes declared — "+
			"per-goal workflow generation needs explicit goals", target.Key)
	}
	dir := outDir
	if dir == "" {
		dir = target.OutDir
	}
	if !st.RendersTarget(target.Key) {
		genlog.Debug("ci.targets: target not declared, rendering nothing", "target", target.Key, "declared", st.Targets)
		return nil, dir, nil
	}
	b, err := ci.LoadBuild(pfPath, target.Key)
	if err != nil {
		return nil, "", fmt.Errorf("ci: reading build inputs: %w", err)
	}
	files := make(map[string][]byte, len(st.Goals))
	for _, goal := range st.Goals {
		// Pin to ONE goal, then prune tools disabled for this target — the resolver's
		// reachability narrows the model to exactly this goal's closure.
		gst := st.ForGoal(goal).ForTarget(target.Key)
		rm, err := resolve.Resolve(gst)
		if err != nil {
			return nil, "", fmt.Errorf("goal %q: %w", goal, err)
		}
		m := render.Build(rm, gst, b)
		if m.Err != nil {
			return nil, "", fmt.Errorf("goal %q: %w", goal, m.Err)
		}
		rendered, err := render.Workflow(m, target, platformOf(gst, target.Key))
		if err != nil {
			return nil, "", fmt.Errorf("goal %q: %w", goal, err)
		}
		files[filepath.Join(dir, goal+target.Ext)] = rendered
	}
	return files, dir, nil
}

// sortedPaths returns a map's keys sorted, so the per-goal writes (and their log
// lines) are deterministic.
func sortedPaths(files map[string][]byte) []string {
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return paths
}

// checkFresh is the "codegen can't silently lie" guarantee: regenerate, then
// compare against the committed file. A mismatch (or a missing file) fails.
func checkFresh(path string, want []byte) error {
	// #nosec G304 — path is the resolver's own output target (the -o flag / the
	// target's canonical workflow path), operator-supplied tooling config, not
	// untrusted input.
	got, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("freshness check: cannot read %s (run `pf-ci generate`): %w", path, err)
	}
	if !bytes.Equal(bytes.TrimRight(got, "\n"), bytes.TrimRight(want, "\n")) {
		return fmt.Errorf("freshness check: %s is stale — regenerate with `pf-ci generate`", path)
	}
	genlog.Success(fmt.Sprintf("pf-ci: %s is up to date", path))
	return nil
}

// generatedMarker is the banner every rendered workflow carries (see workflow.yaml.
// tmpl). checkFreshDir uses it to tell a STALE generated file (a goal that was removed)
// from a hand-written workflow living in the same vendor dir — only the former is an
// orphan, so a project's own non-pf-ci workflows are never touched.
const generatedMarker = "Generated by pf-ci"

// checkFreshDir is the multi-file freshness gate: it verifies every expected per-goal
// file matches AND that no STALE generated file lingers (a goal deleted from the DAG,
// or a target dropped from org.projectfile.ci.targets, must not leave an orphan
// workflow behind). Without the orphan sweep the "codegen can't silently lie"
// guarantee would leak once one DAG maps to many files.
func checkFreshDir(dir, ext string, want map[string][]byte) error {
	for _, path := range sortedPaths(want) {
		if err := checkFresh(path, want[path]); err != nil {
			return err
		}
	}
	stale, err := orphanWorkflows(dir, ext, want)
	if err != nil {
		return err
	}
	if len(stale) > 0 {
		return fmt.Errorf("freshness check: %s is an orphan — no goal produces it; "+
			"remove the file or restore the goal", stale[0])
	}
	return nil
}

// orphanWorkflows lists the committed <ext> files in dir that carry generatedMarker
// but are not in want, sorted by path (os.ReadDir already returns entries sorted).
func orphanWorkflows(dir, ext string, want map[string][]byte) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var stale []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ext) {
			continue
		}
		path := filepath.Join(dir, e.Name())
		if _, ok := want[path]; ok {
			continue // an expected file, verified by checkFresh/writeWorkflow instead
		}
		// #nosec G304 — path is inside the resolver's own output dir, not untrusted input.
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		if bytes.Contains(data, []byte(generatedMarker)) {
			stale = append(stale, path)
		}
	}
	return stale, nil
}

// writeWorkflowDir writes every file in files, then removes any orphanWorkflows left
// in dir — a goal removed from the DAG, or a target dropped from ci.targets, leaves
// no stale file behind instead of only failing the next -check.
func writeWorkflowDir(dir, ext string, files map[string][]byte) error {
	for _, path := range sortedPaths(files) {
		if err := writeWorkflow(path, files[path]); err != nil {
			return err
		}
		genlog.Success(fmt.Sprintf("pf-ci: wrote %s", path))
	}
	stale, err := orphanWorkflows(dir, ext, files)
	if err != nil {
		return err
	}
	for _, path := range stale {
		if err := os.Remove(path); err != nil {
			return err
		}
		genlog.Success(fmt.Sprintf("pf-ci: removed orphan %s", path))
	}
	return nil
}

func writeWorkflow(path string, data []byte) error {
	if dir := dirOf(path); dir != "" {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return err
		}
	}
	return os.WriteFile(path, data, 0o644) // #nosec G306 — a committed workflow file
}

// dirOf returns the directory portion of a slash path, or "" when there is none.
func dirOf(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[:i]
		}
	}
	return ""
}
