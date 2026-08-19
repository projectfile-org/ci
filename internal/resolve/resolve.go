// SPDX-FileCopyrightText: 2026 Damián Búho <damian.buho@proton.me>
//
// SPDX-License-Identifier: MIT

// Package resolve lowers an org.projectfile.ci signal DAG into a vendor-neutral
// JOB MODEL: one job per tool, `needs` derived from the graph edges, matrix
// fan-out attached to CELL jobs. This is the SECOND lowering of the spec (m6e is
// the first, to make); both must satisfy the SAME conformance vectors, so this
// package is what `spec/conformance/ci/*.yaml` pins — render targets (GHA,
// Forgejo, Tekton) are dumb templates over the model produced here.
//
// The lowering, step by step (each mirrors a documented reader rule):
//   - preflight   : refuse a node→node cycle BEFORE producing anything;
//   - goals       : flagged goal nodes, else every sink (a node no node needs);
//   - reach       : the needs-closure of the goals — only those nodes' tools run;
//   - partition   : classify each node SOURCE / CELL / JOIN for matrix fan-out;
//   - contraction : a node has no recipe, so a tool's `needs` skips over upstream
//     nodes to the real tool-jobs they gate (the abstract-node frontier).
package resolve

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"kiota.ch/projectfile/core/v2/pkg/genlog"
	"projectfile.org/projectfile/ci-resolver/internal/ci"
)

// Class is a node's matrix timing class (Model 1 — membership is a per-node flag).
type Class string

const (
	ClassSource Class = "source" // outside every cell's closure — runs once, before
	ClassCell   Class = "cell"   // flagged matrix:true — runs once PER product cell
	ClassJoin   Class = "join"   // downstream of a cell, not flagged — once, after
)

// Job is one tool execution in the lowered model. A tool is a global singleton:
// a tool named by several nodes lowers to ONE job whose `needs` is the union.
type Job struct {
	Name  string    // tool name (== the make target / auto-<tool> wrapper)
	Args  string    // resolved {args:"…"} string, empty if none
	Needs []string  // upstream tool-job names (contracted past abstract nodes), sorted
	Axes  []ci.Axis // matrix axes when the owning node is a CELL, else nil
	// Excludes are the cells Axes mint that this job must NOT run (matrix.exclude of
	// the SAME matrix the axes come from). Empty => the full grid.
	Excludes []ci.Exclusion
	Class    Class // timing class inherited from the owning node
}

// Cells returns this job's per-run multiplicity: a CELL job runs once per cell of
// the axes' cartesian product MINUS the excluded ones; SOURCE/JOIN once. This is
// the `counts` the conformance vectors assert (the matrix fan-out/fan-in).
func (j Job) Cells() int {
	if j.Class != ClassCell || len(j.Axes) == 0 {
		return 1
	}
	return len(ci.Cells(j.Axes, j.Excludes))
}

// Model is the resolved job model: the goals and the jobs that run for them.
type Model struct {
	Goals []string // resolved terminal nodes (explicit or inferred sinks), sorted
	Jobs  []Job    // one per running tool, name-sorted (deterministic output)
}

// RejectError is a semantic defect a resolver MUST refuse before running any
// tool. Reason is the vendor-neutral token the conformance vectors enumerate.
type RejectError struct{ Reason string }

func (e *RejectError) Error() string { return "org.projectfile.ci: " + e.Reason }

// Resolve performs the full lowering. A nil subtree (no CI declared) yields a
// nil model and no error — a project with no CI runs no CI.
func Resolve(st *ci.Subtree) (*Model, error) {
	if st == nil {
		return nil, nil
	}
	g := newGraph(st)

	if err := g.preflight(); err != nil {
		return nil, err
	}
	goals := g.goals()
	reach := g.reachable(goals)
	g.classify()

	jobs := g.buildJobs(reach)
	sort.Strings(goals)
	return &Model{Goals: goals, Jobs: jobs}, nil
}

// graph is the working view: node set + the node→node and node→tool partitions
// of every node's needs, plus the matrix partition once classify() has run.
type graph struct {
	st        *ci.Subtree
	isNode    map[string]bool
	nodeDeps  map[string][]string  // node -> upstream NODE names (ordering edges)
	nodeTools map[string][]ci.Need // node -> TOOL needs (the leaves that run)
	cell      map[string]bool      // node -> flagged matrix:true
	inClosure map[string]bool      // node -> in a cell's closure (CELL or needs one)
}

func newGraph(st *ci.Subtree) *graph {
	g := &graph{
		st:        st,
		isNode:    make(map[string]bool, len(st.NodeOrder)),
		nodeDeps:  make(map[string][]string),
		nodeTools: make(map[string][]ci.Need),
		cell:      make(map[string]bool),
		inClosure: make(map[string]bool),
	}
	for _, n := range st.NodeOrder {
		g.isNode[n] = true
	}
	// Partition each node's needs into node-deps (an entry naming a declared
	// node — a pure ordering edge) and tools (everything else — a real leaf).
	// The spec is explicit that the two are not distinguished syntactically; the
	// node set is the only discriminator.
	for _, n := range st.NodeOrder {
		for _, need := range st.Nodes[n].Needs {
			if g.isNode[need.Target] {
				g.nodeDeps[n] = append(g.nodeDeps[n], need.Target)
			} else {
				g.nodeTools[n] = append(g.nodeTools[n], need)
			}
		}
	}
	return g
}

// preflight refuses a defective graph: a node→node cycle (no topological order).
// A dangling need is NOT a defect — an entry that names no node is simply a tool,
// per the spec. An unknown goal is now impossible: a goal is a node flagged
// `goal: true`, so it is always a declared node by construction.
func (g *graph) preflight() error {
	if g.hasCycle() {
		return &RejectError{Reason: "cycle"}
	}
	return nil
}

// hasCycle runs Kahn's algorithm over the node→node edges: if any node never
// reaches in-degree zero, an edge loop remains.
func (g *graph) hasCycle() bool {
	indeg := make(map[string]int, len(g.st.NodeOrder))
	for _, n := range g.st.NodeOrder {
		indeg[n] = 0
	}
	for _, n := range g.st.NodeOrder {
		// edge dep -> n: n depends on dep, so dep must precede n (n's in-degree++).
		indeg[n] += len(g.nodeDeps[n])
	}
	queue := make([]string, 0, len(indeg))
	for _, n := range g.st.NodeOrder {
		if indeg[n] == 0 {
			queue = append(queue, n)
		}
	}
	seen := 0
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		seen++
		// Removing n decrements every node that depends ON n.
		for _, m := range g.st.NodeOrder {
			for _, dep := range g.nodeDeps[m] {
				if dep == n {
					indeg[m]--
					if indeg[m] == 0 {
						queue = append(queue, m)
					}
				}
			}
		}
	}
	return seen != len(g.st.NodeOrder)
}

// goals returns the flagged goal nodes, else every sink (a node no other node
// needs). Leaving mutating nodes (e.g. a *-fix `formatted` sink) unflagged keeps
// them out of the closure — how a project checks-but-never-rewrites under CI.
func (g *graph) goals() []string {
	if g.st.GoalsExplicit {
		out := append([]string(nil), g.st.Goals...)
		sort.Strings(out)
		return out
	}
	needed := make(map[string]bool)
	for _, n := range g.st.NodeOrder {
		for _, dep := range g.nodeDeps[n] {
			needed[dep] = true
		}
	}
	var sinks []string
	for _, n := range g.st.NodeOrder {
		if !needed[n] {
			sinks = append(sinks, n)
		}
	}
	sort.Strings(sinks)
	return sinks
}

// reachable is the transitive needs-closure of the goals over node→node edges —
// the set of nodes whose tools actually run. Tools of an off-closure node (an
// excluded sink) never appear in the model.
func (g *graph) reachable(goals []string) map[string]bool {
	reach := make(map[string]bool)
	var visit func(string)
	visit = func(n string) {
		if reach[n] || !g.isNode[n] {
			return
		}
		reach[n] = true
		for _, dep := range g.nodeDeps[n] {
			visit(dep)
		}
	}
	for _, goal := range goals {
		visit(goal)
	}
	return reach
}

// classify computes the CELL flag and the closure membership of every node.
// Closure propagates UPWARD: a node is in the closure iff it is a CELL or it
// (transitively) needs a node in the closure. A CELL's dependencies are NOT
// pulled in — they stay SOURCE and run once.
func (g *graph) classify() {
	for _, n := range g.st.NodeOrder {
		if g.st.Nodes[n].Matrix {
			g.cell[n] = true
			g.inClosure[n] = true
		}
	}
	for changed := true; changed; {
		changed = false
		for _, n := range g.st.NodeOrder {
			if g.inClosure[n] {
				continue
			}
			for _, dep := range g.nodeDeps[n] {
				if g.inClosure[dep] {
					g.inClosure[n] = true
					changed = true
					break
				}
			}
		}
	}
}

// effectiveMatrix returns the axes a CELL node fans over AND the cells those axes
// mint but nothing builds: the node's OWN pair when it declares a per-node matrix
// (isolating one artifact class's dimensions — binaries over {GOOS,GOARCH}), else
// the GLOBAL pair (the common case, e.g. b19's series shared by every matrix node).
// The two travel together because per-node axes are isolated: taking the global
// exclusions against own axes would subtract cells that grid never minted.
func (g *graph) effectiveMatrix(node string) ([]ci.Axis, []ci.Exclusion) {
	n := g.st.Nodes[node]
	if len(n.Axes) > 0 {
		return n.Axes, n.Excludes
	}
	axes, excludes := g.st.Axes, g.st.Excludes
	if len(n.Without) > 0 {
		axes, excludes = dropAxes(node, axes, excludes, n.Without)
	}
	if len(n.Pin) > 0 {
		axes, excludes = pinAxes(node, axes, excludes, n.Pin)
	}
	return axes, excludes
}

// dropAxes subtracts the named GLOBAL axes from a node's fan-out (matrix.without).
// An exclusion that constrains a dropped axis goes with it: the cells it named no
// longer exist, and keeping it would subtract survivors that merely share the rest of
// its coordinates. Dropping every axis is legal and leaves a single un-fanned job —
// the assembly shape, one job joining all the cells.
func dropAxes(node string, axes []ci.Axis, excludes []ci.Exclusion, without []string) ([]ci.Axis, []ci.Exclusion) {
	drop := make(map[string]bool, len(without))
	for _, k := range without {
		drop[k] = true
	}
	kept := make([]ci.Axis, 0, len(axes))
	for _, a := range axes {
		if drop[a.Key] {
			genlog.Info("matrix.without: node drops an axis", "node", node, "axis", a.Key, "values", a.Values)
			continue
		}
		kept = append(kept, a)
	}
	// A `without` naming an axis this project does not declare is a no-op by design:
	// M6E_ARCH exists only where org.projectfile.architecture does, and the same node
	// declaration has to render byte-identically on the projects that declare nothing.
	if len(kept) == len(axes) {
		genlog.Info("matrix.without: no axis matched, node keeps the global fan-out",
			"node", node, "without", without, "declared", len(axes))
		return axes, excludes
	}
	keptEx := make([]ci.Exclusion, 0, len(excludes))
	for _, ex := range excludes {
		constrained := false
		for _, kv := range ex {
			if drop[kv.Key] {
				constrained = true
				break
			}
		}
		if constrained {
			genlog.Info("matrix.without: dropping an exclusion that constrains a dropped axis",
				"node", node, "exclusion", ex)
			continue
		}
		keptEx = append(keptEx, ex)
	}
	return kept, keptEx
}

// pinAxes narrows the named GLOBAL axes to one value each (matrix.pin), the
// complement of dropAxes: the node runs once over that dimension yet still carries it,
// which is what a node needs when its artifacts are named per cell. An exclusion
// constraining a pinned axis to a DIFFERENT value can never match the surviving cells,
// so it goes; one naming the pinned value keeps working unchanged.
// A pin naming an axis this project does not declare, or a value that axis does not
// carry, leaves the fan-out ALONE — a pinned cell the grid never minted would send the
// node looking for an artifact no sibling produced, so the miss fails towards full
// coverage instead.
func pinAxes(node string, axes []ci.Axis, excludes []ci.Exclusion, pin map[string]string) ([]ci.Axis, []ci.Exclusion) {
	pinned := make(map[string]string, len(pin))
	kept := make([]ci.Axis, 0, len(axes))
	for _, a := range axes {
		v, ok := pin[a.Key]
		if !ok {
			kept = append(kept, a)
			continue
		}
		if !slices.Contains(a.Values, v) {
			genlog.Warn("matrix.pin: value is not among the axis values, node keeps the full fan-out",
				"node", node, "axis", a.Key, "value", v, "values", a.Values)
			kept = append(kept, a)
			continue
		}
		// A Decision row, not an Info line: this is where cells a reader expected to see
		// stop existing, so it must be visible in an ordinary generate — naming the
		// values it skipped and the node knob that brings them back.
		skipped := make([]string, 0, len(a.Values)-1)
		for _, s := range a.Values {
			if s != v {
				skipped = append(skipped, s)
			}
		}
		value := node + " " + a.Key + " -> " + v
		if len(skipped) > 0 {
			value += " (skipped " + strings.Join(skipped, ", ") + ")"
		}
		genlog.Decision("matrix_pin", value, "matrix.pin."+a.Key, "nodes."+node+".matrix")
		pinned[a.Key] = v
		kept = append(kept, ci.Axis{Key: a.Key, Values: []string{v}})
	}
	if len(pinned) == 0 {
		genlog.Info("matrix.pin: no axis matched, node keeps the global fan-out",
			"node", node, "pin", pin, "declared", len(axes))
		return axes, excludes
	}
	keptEx := make([]ci.Exclusion, 0, len(excludes))
	for _, ex := range excludes {
		unreachable := false
		for _, kv := range ex {
			if v, ok := pinned[kv.Key]; ok && v != kv.Value {
				unreachable = true
				break
			}
		}
		if unreachable {
			genlog.Info("matrix.pin: dropping an exclusion no surviving cell can match",
				"node", node, "exclusion", ex)
			continue
		}
		keptEx = append(keptEx, ex)
	}
	return kept, keptEx
}

func (g *graph) classOf(n string) Class {
	switch {
	case g.cell[n]:
		return ClassCell
	case g.inClosure[n]:
		return ClassJoin
	default:
		return ClassSource
	}
}

// buildJobs materialises one Job per running tool. A tool reused across nodes is
// one job: its needs are the union of every owning node's frontier, its args the
// last non-empty binding, its class CELL if any owning node is a CELL (so a
// shared tool fans out if any owner does — conformance only exercises clean
// single-owner tools, but the union rule keeps the singleton honest).
func (g *graph) buildJobs(reach map[string]bool) []Job {
	type acc struct {
		args     string
		needs    map[string]bool
		class    Class
		axes     []ci.Axis
		excludes []ci.Exclusion
	}
	tools := make(map[string]*acc)
	order := []string{} // first-seen order before the final name sort

	for _, n := range g.st.NodeOrder {
		if !reach[n] {
			continue
		}
		nodeClass := g.classOf(n)
		// The frontier this node's tools must wait for: the contracted tool-jobs
		// gating each upstream node.
		var upstream []string
		for _, dep := range g.nodeDeps[n] {
			if reach[dep] {
				upstream = append(upstream, g.frontier(dep, reach)...)
			}
		}
		for _, need := range g.nodeTools[n] {
			a, ok := tools[need.Target]
			if !ok {
				a = &acc{needs: make(map[string]bool), class: ClassSource}
				tools[need.Target] = a
				order = append(order, need.Target)
			}
			if need.HasArgs {
				a.args = need.Args
			}
			for _, u := range upstream {
				if u != need.Target { // never self-gate
					a.needs[u] = true
				}
			}
			if nodeClass == ClassCell {
				a.class = ClassCell
				a.axes, a.excludes = g.effectiveMatrix(n)
			} else if a.class != ClassCell && nodeClass == ClassJoin {
				a.class = ClassJoin
			}
		}
	}

	jobs := make([]Job, 0, len(order))
	for _, name := range order {
		a := tools[name]
		needs := make([]string, 0, len(a.needs))
		for u := range a.needs {
			needs = append(needs, u)
		}
		sort.Strings(needs)
		jobs = append(jobs, Job{Name: name, Args: a.args, Needs: needs, Axes: a.axes, Excludes: a.excludes, Class: a.class})
	}
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].Name < jobs[j].Name })
	return jobs
}

// frontier returns the real tool-jobs that represent "node n is done" — the
// abstract-node contraction. A node has no recipe of its own, so a downstream
// tool cannot depend on the node directly; it depends on the tools the node
// gates. If n owns tools, those tools already (via their own gate) wait on n's
// upstreams, so the frontier is exactly n's tools. A pure-join node (no tools)
// contributes its upstreams' frontiers instead — recursively skipping every
// recipe-less node until real work is reached.
func (g *graph) frontier(n string, reach map[string]bool) []string {
	seen := make(map[string]bool)
	var out []string
	var walk func(string)
	walk = func(node string) {
		if seen[node] || !reach[node] {
			return
		}
		seen[node] = true
		if tools := g.nodeTools[node]; len(tools) > 0 {
			for _, t := range tools {
				out = append(out, t.Target)
			}
			return // this node's tools are the frontier; their gate covers upstreams
		}
		for _, dep := range g.nodeDeps[node] {
			walk(dep)
		}
	}
	walk(n)
	sort.Strings(out)
	return dedupe(out)
}

// NodeView materialises one reachable DAG node for a render target that lacks
// native node grouping (GHA/Forgejo have only job→job edges). It is rendered as a
// no-op GATE job that links the graph: its `needs` are its own tool members plus
// its upstream node-gates, so the emitted workflow's edges mirror the AUTHORED DAG
// (a tool reads `needs: [<upstream-node>]`, not the contracted leaf it gates).
type NodeView struct {
	Name     string   // node name == the gate job name
	Goal     bool     // flagged goal:true — a milestone (e.g. a branch-protection target)
	NodeDeps []string // reachable upstream NODE names, sorted+deduped
	Tools    []string // this node's own reachable TOOL members, sorted+deduped
	When     []string // trigger predicate (ci.Node.When): events this node runs on, empty => all
	// MaxParallel is ci.Node.MaxParallel carried through: the cap on concurrent matrix
	// cells the render lifts onto this node's strategy block. 0 => the forge default.
	MaxParallel int
	// Serialise is ci.Node.Serialise carried through: the GLOBAL axis whose values the
	// render walks one job at a time (chained by `needs`). Empty => one job, full fan-out.
	Serialise string
	// Concurrency is ci.Node.Concurrency carried through: the node's serialisation group
	// the render lifts onto the job's concurrency block. nil => no job-level guard.
	Concurrency *ci.Concurrency
}

// Nodes is the node-materialised view: the reachable nodes as gate vertices plus,
// per tool, the upstream NODE names that tool waits on (the union over every node
// that owns it). A grouping-less target renders each NodeView as a gate job and
// points tools at ToolDeps INSTEAD of the contracted tool→tool needs; the
// conformance-pinned Model is unchanged, so a native-grouping lowering (Tekton,
// m6e) ignores this view entirely. nil subtree => nil (a project with no CI).
type Nodes struct {
	Views    []NodeView          // reachable nodes, in deterministic NodeOrder
	ToolDeps map[string][]string // tool name -> upstream NODE names, sorted+deduped
}

// NodeModel derives the node-materialised view. It reuses the same reachability the
// job lowering uses (flagged goals, else sinks), so a node excluded from the run
// (an unflagged mutating sink) gets no gate. Cycle rejection is Resolve's job — the
// caller pairs the two, so NodeModel assumes an acyclic graph.
func NodeModel(st *ci.Subtree) *Nodes {
	if st == nil {
		return nil
	}
	g := newGraph(st)
	reach := g.reachable(g.goals())

	out := &Nodes{ToolDeps: map[string][]string{}}
	toolDeps := map[string]map[string]bool{} // tool -> set of upstream node names
	for _, n := range st.NodeOrder {
		if !reach[n] {
			continue
		}
		var deps []string
		for _, d := range g.nodeDeps[n] {
			if reach[d] {
				deps = append(deps, d)
			}
		}
		sort.Strings(deps)
		deps = dedupe(deps)

		seen := map[string]bool{}
		var tools []string
		for _, need := range g.nodeTools[n] {
			if !seen[need.Target] {
				seen[need.Target] = true
				tools = append(tools, need.Target)
			}
		}
		sort.Strings(tools)

		out.Views = append(out.Views, NodeView{Name: n, Goal: st.Nodes[n].Goal, NodeDeps: deps, Tools: tools, When: st.Nodes[n].When, MaxParallel: st.Nodes[n].MaxParallel, Serialise: st.Nodes[n].Serialise, Concurrency: st.Nodes[n].Concurrency})

		// Every tool this node owns waits on this node's upstream node-gates.
		for _, need := range g.nodeTools[n] {
			set := toolDeps[need.Target]
			if set == nil {
				set = map[string]bool{}
				toolDeps[need.Target] = set
			}
			for _, d := range deps {
				set[d] = true
			}
		}
	}
	for t, set := range toolDeps {
		ds := make([]string, 0, len(set))
		for d := range set {
			ds = append(ds, d)
		}
		sort.Strings(ds)
		out.ToolDeps[t] = ds
	}
	return out
}

func dedupe(in []string) []string {
	if len(in) < 2 {
		return in
	}
	out := in[:1]
	for _, s := range in[1:] {
		if s != out[len(out)-1] {
			out = append(out, s)
		}
	}
	return out
}

// String renders a job's needs for diagnostics.
func (j Job) String() string {
	return fmt.Sprintf("%s(class=%s cells=%d needs=%v)", j.Name, j.Class, j.Cells(), j.Needs)
}
