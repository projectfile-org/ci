// SPDX-FileCopyrightText: 2026 Damián Búho <damian.buho@proton.me>
//
// SPDX-License-Identifier: MIT

// Command pf-ci lowers an org.projectfile.ci signal DAG into vendor CI workflows.
package main

//nolint:goconst

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"slices"
	"sort"
	"strconv"
	"strings"

	"kiota.ch/projectfile/core/v2/pkg/genlog"
	"projectfile.org/projectfile/ci/v2/internal/ci"
	"projectfile.org/projectfile/ci/v2/internal/render"
	"projectfile.org/projectfile/ci/v2/internal/resolve"

	"github.com/spf13/cobra"

	"charm.land/lipgloss/v2"
)

var version = devVersion

const devVersion = "dev"

func moduleVersion(stamped string, info *debug.BuildInfo, ok bool) string {
	if stamped != devVersion || !ok || info.Main.Version == "" || info.Main.Version == "(devel)" {
		return stamped
	}
	return info.Main.Version
}

type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }

type driftError struct{ msg string }

func (e driftError) Error() string { return e.msg }

type inputError struct{ msg string }

func (e inputError) Error() string { return e.msg }

func errUsage(msg string) error { return usageError{msg: msg} }
func errDrift(msg string) error { return driftError{msg: msg} }
func errInput(msg string) error { return inputError{msg: msg} }

func exitCodeFor(err error) int {
	var ue usageError
	var de driftError
	var ie inputError
	switch {
	case errors.As(err, &ue):
		return 2
	case errors.As(err, &de):
		return 3
	case errors.As(err, &ie):
		return 4
	}
	return 1
}

func classifyDispatchError(err error) error {
	msg := err.Error()
	if strings.HasPrefix(msg, "unknown command") ||
		strings.HasPrefix(msg, "unknown shorthand flag") ||
		strings.HasPrefix(msg, "unknown flag") {
		return usageError{msg: msg}
	}
	return err
}

// opts holds every subcommand flag.
type opts struct {
	pf, target, out string
	check, verbose  bool
	quiet           bool
	dryRun, force   bool
}

var o opts

var commands = map[string]struct {
	summary string
	run     func(o *opts, stdout io.Writer) error
}{
	//nolint:goconst
	"resolve": {"Emit...", nil},
	//nolint:goconst
	"generate": {"Render...", nil},
}

var lastTarget, lastPfPath string

func main() {
	code := 0
	func() {
		defer func() {
			if rec := recover(); rec != nil {
				_, _ = fmt.Fprintf(os.Stderr, "pf-ci: unexpected error: %v\n", rec)
				debug.PrintStack()
				_, _ = fmt.Fprintf(os.Stderr, "debug state: version=%s target=%s projectfile=%s args=%v\n", version, lastTarget, lastPfPath, os.Args[1:])
				_, _ = fmt.Fprintf(os.Stderr, "report bugs at the project's issue tracker\n")
				code = 1
			}
		}()
		code = run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	}()
	os.Exit(code)
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	root := newRootCmd()
	root.SetIn(stdin)
	root.SetOut(stdout)
	root.SetArgs(normalizeArgs(args))
	root.SetErr(stderr)
	root.SilenceUsage = true
	root.SilenceErrors = true
	cmd, err := root.ExecuteC()
	_ = cmd
	if err != nil {
		err = classifyDispatchError(err)
		// cobra may have already printed; ensure our voice.
		code := exitCodeFor(err)
		// Only print if not already? We'll print once.
		_, _ = fmt.Fprintf(stderr, "pf-ci: %v\n", err)
		if code == 2 {
			// print usage for the relevant subcommand
			name := ""
			if len(args) > 0 {
				name = args[0]
			}
			if name == "resolve" || name == "generate" {
				subUsage(stderr, name)
			} else {
				usage(stderr)
			}
		}
		genlog.FlushDebug()
		return code
	}
	genlog.FlushDebug()
	return 0
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "pf-ci [command]",
		Short: "lower org.projectfile.ci to vendor CI workflows",
		Long:  "pf-ci — lower org.projectfile.ci to vendor CI workflows",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				return errUsage(fmt.Sprintf("unknown command %q", args[0]))
			}
			usage(cmd.OutOrStdout())
			return nil
		},
		Version: versionTemplate(),
	}
	root.SetVersionTemplate("pf-ci {{.Version}}\n")
	root.Flags().BoolP("version", "v", false, "print the version")
	root.PersistentFlags().BoolVar(&o.verbose, "verbose", false, "show operational log lines (e.g. interpolation lookups); also PF_CLI_VERBOSE=1")
	root.PersistentFlags().BoolVar(&o.quiet, "quiet", false, "mute progress lines; warnings, errors and the verdict still print")
	root.PersistentFlags().BoolVar(&o.quiet, "q", false, "shorthand for -quiet")
	// resolve
	resolveCmd := &cobra.Command{
		Use:   "resolve",
		Short: "Emit the vendor-neutral job model as JSON (the template input).",
		RunE: func(cmd *cobra.Command, _ []string) error {
			genlog.SetVerbose(verboseFromFlagOrEnv(o.verbose))
			genlog.SetQuiet(o.quiet)
			cleanup, err := stdinProjectfile(&o, cmd.InOrStdin())
			if err != nil {
				return err
			}
			defer cleanup()
			lastPfPath = o.pf
			st, err := ci.Load(o.pf)
			if err != nil {
				return classifyLoadError(o.pf, err)
			}
			model, err := lower(st, o.pf)
			if err != nil {
				return err
			}
			out, err := model.JSON()
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), string(out))
			return err
		},
	}
	// generate
	generateCmd := &cobra.Command{
		Use:   "generate",
		Short: "Render the job model to a target’s workflow YAML, or (-check) verify the committed workflow still matches the source DAG.",
		RunE:  runGenerate,
	}
	generateCmd.Flags().StringVar(&o.target, "target", "", "render target: "+fmt.Sprint(render.TargetKeys()))
	generateCmd.Flags().StringVar(&o.out, "o", "", "output directory for the per-goal workflow files (default: the target’s vendor dir); a single path for lefthook; - is refused, as generate writes files")
	generateCmd.Flags().BoolVar(&o.check, "check", false, "freshness gate: exit non-zero if the committed workflow drifted")
	generateCmd.Flags().BoolVar(&o.dryRun, "dry-run", false, "print the write/remove plan on stdout and change nothing")
	generateCmd.Flags().BoolVar(&o.dryRun, "n", false, "shorthand for -dry-run")
	generateCmd.Flags().BoolVar(&o.force, "force", true, "remove orphaned generated workflows; -force=false reports them and exits non-zero instead")
	generateCmd.Flags().BoolVar(&o.force, "f", true, "shorthand for -force")
	// version
	versionCmd := &cobra.Command{
		Use:   "version",
		Short: "Print the resolver version.",
		Run: func(cmd *cobra.Command, _ []string) {
			info, ok := debug.ReadBuildInfo()
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "pf-ci", moduleVersion(version, info, ok))
		},
	}
	// help
	helpCmd := &cobra.Command{Use: "help", Short: "Print help."}
	_ = helpCmd

	root.AddCommand(resolveCmd, generateCmd, versionCmd)
	// pf flag on root persistent? ci.Load uses o.pf; make -pf available to both
	root.PersistentFlags().StringVar(&o.pf, "pf", "", "projectfile path, or - for a YAML/JSON document on stdin (default: auto-discover)")
	return root
}

func versionTemplate() string {
	info, ok := debug.ReadBuildInfo()
	return moduleVersion(version, info, ok)
}

func runGenerate(cmd *cobra.Command, _ []string) error {
	genlog.SetVerbose(verboseFromFlagOrEnv(o.verbose))
	genlog.SetQuiet(o.quiet)
	cleanup, err := stdinProjectfile(&o, cmd.InOrStdin())
	if err != nil {
		return err
	}
	defer cleanup()
	lastTarget = o.target
	lastPfPath = o.pf
	if o.out == "-" {
		return errUsage(fmt.Sprintf("-o %s: generate writes one file per goal, not a stream; name a directory (or a file for lefthook)", o.out))
	}
	target, ok := render.Targets[o.target]
	if !ok {
		return errUsage(unknownTargetMessage(o.target))
	}
	st, err := ci.Load(o.pf)
	if err != nil {
		return classifyLoadError(o.pf, err)
	}
	if target.Key == render.TargetLefthook {
		rendered, err := render.Lefthook(st, target)
		if err != nil {
			return err
		}
		dest := o.out
		if dest == "" {
			dest = target.OutPath
		}
		if o.check {
			return checkFresh(dest, rendered)
		}
		if o.dryRun {
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "write %s\n", dest)
			return err
		}
		if err := writeWorkflow(dest, rendered); err != nil {
			return err
		}
		genlog.Success(fmt.Sprintf("pf-ci: wrote %s (%d hooks)", dest, len(render.HookNodes(st))))
		return nil
	}
	files, dir, err := workflowFiles(st, target, o.out, o.pf)
	if err != nil {
		return err
	}
	if o.check {
		return checkFreshDir(dir, target.Ext, files)
	}
	return writeWorkflowDir(dir, target.Ext, files, o.dryRun, o.force, cmd.OutOrStdout())
}

// stdinProjectfile unchanged? handle -pf - in cmd? Need implement. We'll wrap ci.Load? Simpler: if o.pf == "-", stage before ci.Load. But runGenerate/cobra RunE doesn't have stdin easily except cmd.InOrStdin. Let's handle in a PersistentPreRun? Too complex. Keep stdinProjectfile call by detecting "-" in RunE and stage. Implement staging inside RunE before ci.Load. We'll add to runGenerate and resolve RunE.

const generatedMarker = "Generated by pf-ci"

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

func platformOf(st *ci.Subtree, key string) ci.Platform {
	if st == nil {
		return ci.Platform{}
	}
	return st.Platforms[key]
}

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

func sortedPaths(files map[string][]byte) []string {
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return paths
}

func checkFresh(path string, want []byte) error {
	got, err := os.ReadFile(path) // #nosec G304 G703 — operator-chosen output path
	if err != nil {
		if os.IsNotExist(err) {
			return errDrift(fmt.Sprintf("freshness check: %s is missing — regenerate with `pf-ci generate`", path))
		}
		if os.IsPermission(err) {
			return errInput(fmt.Sprintf("freshness check: cannot read %s: permission denied", path))
		}
		return fmt.Errorf("freshness check: cannot read %s: %w", path, err)
	}
	if !bytes.Equal(bytes.TrimRight(got, "\n"), bytes.TrimRight(want, "\n")) {
		return errDrift(fmt.Sprintf("freshness check: %s is stale — regenerate with `pf-ci generate`", path))
	}
	genlog.Success(fmt.Sprintf("pf-ci: %s is up to date", path))
	return nil
}

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
		return errDrift(fmt.Sprintf("freshness check: %s is an orphan — no goal produces it; "+
			"remove the file or restore the goal", stale[0]))
	}
	return nil
}

func orphanWorkflows(dir, ext string, want map[string][]byte) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("cannot read directory %s: %w", dir, err)
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
			return nil, fmt.Errorf("cannot read %s: %w", path, err)
		}
		if bytes.Contains(data, []byte(generatedMarker)) {
			stale = append(stale, path)
		}
	}
	return stale, nil
}

func writeWorkflowDir(dir, ext string, files map[string][]byte, dryRun, force bool, stdout io.Writer) error {
	stale, err := orphanWorkflows(dir, ext, files)
	if err != nil {
		return err
	}
	if dryRun {
		for _, path := range sortedPaths(files) {
			_, _ = fmt.Fprintf(stdout, "write %s\n", path)
		}
		for _, path := range stale {
			_, _ = fmt.Fprintf(stdout, "remove %s\n", path)
		}
		genlog.Success(fmt.Sprintf("pf-ci: dry run, nothing changed in %s (%d writes, %d removals)", dir, len(files), len(stale)))
		return nil
	}
	for _, path := range sortedPaths(files) {
		if err := writeWorkflow(path, files[path]); err != nil {
			return err
		}
		genlog.Success(fmt.Sprintf("pf-ci: wrote %s", path))
	}
	if len(stale) == 0 {
		return nil
	}
	if !force {
		return fmt.Errorf("%d orphaned generated workflow(s) left in place, pass -force to remove: %s", len(stale), strings.Join(stale, ", "))
	}
	for _, path := range stale {
		if err := os.Remove(path); err != nil {
			return err
		}
	}
	genlog.Success(fmt.Sprintf("pf-ci: removed %d orphan(s): %s", len(stale), strings.Join(stale, ", ")))
	return nil
}

func writeWorkflow(path string, data []byte) error {
	if slices.Contains(strings.Split(filepath.ToSlash(path), "/"), "-") {
		return fmt.Errorf("refusing to write %s: a path element named - is the stdin/stdout marker, not a directory", path)
	}
	if dir := dirOf(path); dir != "" {
		if err := os.MkdirAll(dir, 0o750); err != nil { // #nosec G703 — operator-chosen output dir
			return fmt.Errorf("cannot create directory %s: %w", dir, err)
		}
	}
	if err := os.WriteFile(path, data, 0o644); err != nil { // #nosec G306 G703 — an operator-chosen committed workflow file
		return fmt.Errorf("cannot write %s: %w", path, err)
	}
	return nil
}

func dirOf(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[:i]
		}
	}
	return ""
}

func classifyLoadError(pf string, err error) error {
	cwd, _ := os.Getwd()
	if pf == "" {
		if errors.Is(err, os.ErrNotExist) {
			return errInput(fmt.Sprintf("no projectfile found in %s; pass -pf PATH to name one explicitly", cwd))
		}
		return errInput(fmt.Sprintf("cannot read a projectfile in %s: %v; pass -pf PATH to name one explicitly", cwd, err))
	}
	if errors.Is(err, os.ErrNotExist) {
		return errInput(fmt.Sprintf("-pf %s: file not found; check the path exists and is a projectfile", pf))
	}
	if errors.Is(err, os.ErrPermission) {
		return errInput(fmt.Sprintf("-pf %s: permission denied; check the file permissions", pf))
	}
	return errInput(fmt.Sprintf("-pf %s: cannot read projectfile: %v; check the path exists and is a projectfile", pf, err))
}

func unknownTargetMessage(got string) string {
	keys := render.TargetKeys()
	msg := fmt.Sprintf("unknown -target %q; valid targets: %s", got, strings.Join(keys, ", "))
	if sug := closestMatch(got, keys); sug != "" {
		msg += fmt.Sprintf(" (did you mean %q?)", sug)
	}
	if len(keys) > 0 {
		msg += fmt.Sprintf("; try: pf-ci generate -target %s", keys[0])
	}
	return msg
}

func closestMatch(got string, keys []string) string {
	best := ""
	bestDist := 3
	for _, k := range keys {
		d := levenshtein(strings.ToLower(got), strings.ToLower(k))
		if d < bestDist {
			bestDist = d
			best = k
		}
	}
	return best
}

func levenshtein(a, b string) int {
	if a == b {
		return 0
	}
	if len(a) == 0 {
		return len(b)
	}
	if len(b) == 0 {
		return len(a)
	}
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min3(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}

func min3(a, b, c int) int {
	if a < b {
		if a < c {
			return a
		}
		return c
	}
	if b < c {
		return b
	}
	return c
}

func stdinProjectfile(o *opts, stdin io.Reader) (func(), error) {
	if o.pf != "-" {
		return func() {}, nil
	}
	f, err := os.CreateTemp(".", ".pf-ci-stdin-*.yaml")
	if err != nil {
		return nil, errInput(fmt.Sprintf("-pf -: staging stdin: %v", err))
	}
	cleanup := func() { _ = os.Remove(f.Name()) }
	n, err := io.Copy(f, stdin)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		cleanup()
		return nil, errInput(fmt.Sprintf("-pf -: reading stdin into %s: %v", f.Name(), err))
	}
	genlog.Debug("projectfile read from stdin", "path", f.Name(), "bytes", n)
	o.pf = f.Name()
	return cleanup, nil
}

func newFlagSet(name string) (*flag.FlagSet, *opts, bool) {
	if name != "resolve" && name != "generate" {
		return nil, nil, false
	}
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.Usage = func() {}
	o := &opts{}
	fs.StringVar(&o.pf, "pf", "", "projectfile path, or - for a YAML/JSON document on stdin (default: auto-discover)")
	fs.BoolVar(&o.verbose, "verbose", false, "show operational log lines (e.g. interpolation lookups); also PF_CLI_VERBOSE=1")
	fs.BoolVar(&o.quiet, "quiet", false, "mute progress lines; warnings, errors and the verdict still print")
	fs.BoolVar(&o.quiet, "q", false, "shorthand for -quiet")
	if name == "generate" {
		fs.StringVar(&o.target, "target", "", "render target: "+fmt.Sprint(render.TargetKeys()))
		fs.StringVar(&o.out, "o", "", "output directory for the per-goal workflow files (default: the target’s vendor dir); a single path for lefthook; - is refused, as generate writes files")
		fs.BoolVar(&o.check, "check", false, "freshness gate: exit non-zero if the committed workflow drifted")
		fs.BoolVar(&o.dryRun, "dry-run", false, "print the write/remove plan on stdout and change nothing")
		fs.BoolVar(&o.dryRun, "n", false, "shorthand for -dry-run")
		fs.BoolVar(&o.force, "force", true, "remove orphaned generated workflows; -force=false reports them and exits non-zero instead")
		fs.BoolVar(&o.force, "f", true, "shorthand for -force")
	}
	return fs, o, true
}

func subUsage(w io.Writer, name string) {
	purpose := map[string]string{
		"resolve":  "Emit the vendor-neutral job model as JSON (the template input).",
		"generate": "Render the job model to a target’s workflow YAML, or (-check) verify the committed workflow still matches the source DAG.",
	}
	_, _ = fmt.Fprintf(w, "Usage: pf-ci %s [flags]\n\n%s\n", name, purpose[name])
}

func usage(w io.Writer) {
	title := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212")).Render("pf-ci")
	_, _ = fmt.Fprintf(w, "%s — lower org.projectfile.ci to vendor CI workflows", title)
	_, _ = fmt.Fprint(w, `

Usage:
  pf-ci resolve  [-pf PATH] [-verbose|-q]
  pf-ci generate -target gha|forgejo|lefthook [-pf PATH] [-o PATH] [-check|-n] [-force=false] [-verbose|-q]
  pf-ci help [COMMAND]
  pf-ci version

Try one:
  pf-ci resolve
  pf-ci generate -target gha
  pf-ci generate -target gha -check

Common flags:
  -q, -quiet           Mute progress lines; warnings, errors and the verdict still print.
  -verbose             Show operational log lines; also PF_CLI_VERBOSE=1.

resolve   Emit the vendor-neutral job model as JSON (the template input).
generate  Render the job model to a target’s workflow YAML, or (-check) verify
          the committed workflow still matches the source DAG.
help      Print this help, or one command’s flags (also: pf-ci COMMAND -h).
version   Print the resolver version (the codegen provenance stamp).

Environment:
  PF_CI_WORKFLOW_EXT   Committed-workflow file extension, ".yaml" or ".yml"
                       (default: ".yaml"). Set to ".yml" to render the legacy
                       form; m6e’s make act-* derives its default from the same
                       var, so one knob keeps the generator and consumer in sync.
  PF_CLI_VERBOSE        Same effect as -verbose, for a run that cannot pass flags.

Docs:   https://projectfile.org
Issues: https://github.com/projectfile-org/ci/issues

Run pf-ci --help for every command and flag.
`)
}

func verboseFromFlagOrEnv(flagVal bool) bool {
	if flagVal {
		return true
	}
	v, _ := strconv.ParseBool(os.Getenv("PF_CLI_VERBOSE"))
	return v
}

func normalizeArgs(args []string) []string {
	if len(args) >= 2 && (args[0] == "-h" || args[0] == "--help") {
		// `pf-ci --help <sub>` is the same as `pf-ci help <sub>`.
		return append([]string{"help", args[1]}, args[2:]...)
	}
	long := map[string]bool{
		"verbose": true, "quiet": true, "pf": true, "target": true,
		"check": true, "dry-run": true, "force": true,
	}
	out := make([]string, len(args))
	for i, a := range args {
		if strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") {
			name := a[1:]
			val := ""
			if idx := strings.Index(name, "="); idx >= 0 {
				val = name[idx:]
				name = name[:idx]
			}
			if long[name] {
				if val != "" {
					a = "--" + name + val
				} else {
					a = "--" + name
				}
			}
		}
		out[i] = a
	}
	return out
}
