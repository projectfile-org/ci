// SPDX-FileCopyrightText: 2026 Damián Búho <damian.buho@proton.me>
//
// SPDX-License-Identifier: MIT

// Package ci reads the (includes-merged) org.projectfile.ci signal subtree.
//
// The read path is core-as-a-library (see read.go): core resolves root
// `includes:` and deep-merges them (base/local wins, spec §4.9a); ci-resolver
// normalises the one already-merged view into a Go model the resolver can lower,
// never re-implementing the merge. Contract validation lives in tests that drive
// the built binary, not in a production subprocess.
package ci

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"kiota.ch/projectfile/core/v2/pkg/genlog"
	"kiota.ch/projectfile/core/v2/pkg/interp"
)

// Need is one enabled `needs` entry — a Make-target name plus optional args.
// A node-vs-tool distinction is NOT made here: the spec says every needs entry
// is just a target and the consumer decides. The resolver classifies them later
// against the node set (an entry naming a declared node is an ordering edge; any
// other entry is a tool leaf that actually runs).
type Need struct {
	Target  string
	Args    string
	HasArgs bool
}

// Node is one DAG node — an opaque join label with `goal`/`matrix` flags and its
// enabled needs. Needs order is irrelevant to the lowering (ordering is derived
// structurally from the graph, never declaration order), so it is sorted for
// determinism.
type Node struct {
	Name   string
	Goal   bool // opt-in default CI target (goal: true)
	Matrix bool // CELL: fans over Axes if set, else the global subtree Axes
	// Axes are this node's OWN matrix dimensions (per-node matrix). Empty => a
	// matrix node falls back to the GLOBAL subtree Axes (the common case). A node
	// declares its own axes only to fan over a DIFFERENT dimension than the rest of
	// the pipeline — e.g. a `build-binaries` node fanning {GOOS,GOARCH} while the
	// image-build nodes stay on their own (or no) matrix. Set => Matrix is true.
	Axes []Axis
	// Excludes are the cells THIS node's own axes mint but must not build (see
	// Exclusion). Meaningful only alongside Axes: a node on the GLOBAL axes takes the
	// global exclusions instead, never a mix — per-node axes are isolated, so their
	// exclusions are too.
	Excludes []Exclusion
	// Without are GLOBAL axis keys this node does NOT fan over (matrix.without). It
	// subtracts from the global set rather than restating what is left, which is the
	// only form that works for a DERIVED axis: M6E_ARCH exists exactly when the
	// project declares an architecture set, so a node that authored its own axes to
	// avoid arch would re-introduce the declaration/CI drift the derivation closes.
	// Naming an axis that does not exist is a NO-OP, not an error — the arch axis is
	// absent on ~110 projects, and those must render byte-identically.
	Without []string
	// MaxParallel caps how many of this node's matrix cells run at once
	// (strategy.max-parallel). 0 => unset (the forge's full-parallel default). Set on
	// a matrix node whose cells are individually resource-hungry — e.g. an image build
	// where two concurrent container builds starve the runner — so the author throttles
	// THAT node without touching cheap sibling matrices. Meaningful only when Matrix is
	// set (enforced at parse); a per-cell scheduling concern, never WHAT a gate means.
	MaxParallel int
	// Concurrency is this node's serialisation group (nil => none). Like MaxParallel it is
	// a where/how-it-runs concern, never WHAT a gate means: a second run in the same group
	// queues (or cancels, if CancelInProgress). Set on the publish node so two releases of
	// one image cannot race the shared daemon tag onto a single digest.
	Concurrency *Concurrency
	Needs       []Need
	// When is this node's TRIGGER predicate: the abstract event tokens on which the
	// node (its gate + the tools it owns) is allowed to run. Empty => the node runs
	// on EVERY event the workflow fires for (today's behaviour, back-compatible). A
	// non-empty set both NARROWS the workflow trigger surface and gates the node's
	// jobs per event. The tokens are vendor-NEUTRAL (see WhenEvents); the lowering
	// maps them to the target's `on:`/`if:` spelling — the model never names one.
	// Sorted+deduped in decodeWhen for byte-stable output.
	When []string
	// Schedule and Dispatch carry the workflow-level trigger DATA a node's `when`
	// event tokens can only narrow to: the cron timer and the manual-run button.
	// They are meaningful on a GOAL node (each goal lowers to its OWN file, so the
	// file's `on:` is THAT goal's surface); an interior node may still `when`-gate on
	// `schedule`/`dispatch` but carries no data of its own. Empty/nil => the goal has
	// no timer / no button (the push/PR default). Lifted here from the old global
	// `triggers:` block so one DAG → many per-goal workflows, each with its own `on:`.
	Schedule []string  // cron expressions (POSIX 5-field, UTC), authored order; empty => no timer
	Dispatch *Dispatch // manual-run button (+typed inputs); nil => no button
}

// WhenEvents is the closed vocabulary of abstract trigger tokens a node's `when`
// may carry — each a genuinely distinct CI event, kept vendor-neutral so the same
// predicate lowers onto GHA, Forgejo, or a future engine:
//   - "tag"            a tag push (any tag) — the version-release trigger;
//   - "push:<branch>"  a push to the named branch (e.g. push:main);
//   - "dispatch"       a manual run (the dispatch button) — workflow_dispatch;
//   - "schedule"       a timed run — one of triggers.schedule's crons fired.
//
// `dispatch`/`schedule` only NARROW which nodes run on those events; the events
// themselves are added to the workflow `on:` surface by the `triggers:` block (see
// Triggers), so gating a node on one the block never declares is a parse error
// (validateEventGates) — the gate would otherwise be a dead branch.
//
// The set is deliberately small and EXTENSIBLE (a new token is one entry here +
// one render mapping). validEvent rejects anything else so a typo fails the parse
// rather than silently widening (or muting) a trigger.
const (
	EventTag        = "tag"      // a tag push (any tag)
	EventPushPrefix = "push:"    // push:<branch> — a push to the named branch
	EventDispatch   = "dispatch" // a manual run (the dispatch button)
	EventSchedule   = "schedule" // a scheduled run (one of triggers.schedule fired)
)

// StepWhenAlways is the sole accepted value of a tool's `when` field — the per-STEP
// ordering predicate (Manifest.When), distinct from a node's event `when` above: the
// step runs even when an earlier step in the same job failed (→ `if: ${{ always() }}`).
const StepWhenAlways = "always"

// validEvent reports whether e is a recognised trigger token (see WhenEvents). A
// `push:` token must name a non-empty branch; "tag"/"dispatch"/"schedule" stand alone.
func validEvent(e string) bool {
	switch e {
	case EventTag, EventDispatch, EventSchedule:
		return true
	}
	return strings.HasPrefix(e, EventPushPrefix) && len(e) > len(EventPushPrefix)
}

// Axis is one matrix dimension: an injected build variable and its value list.
// The KEY is the literal variable (no alias layer) — it lowers 1:1 to
// `${{ matrix.<KEY> }}` (GHA/Forgejo) or a make command-line var (m6e).
type Axis struct {
	Key    string
	Values []string
}

// ArchAxis is the matrix variable carrying the target CPU architecture. It is the
// one axis the resolver MINTS rather than reads from matrix.axes: the source of
// truth is org.projectfile.architecture, so nobody authors an arch axis by hand
// and no project can declare an arch set its matrix then contradicts.
//
// Values are BARE arch names (`amd64`, never `linux/amd64`) — see
// Reader.architectures for why the platform prefix is composed at use, not stored.
const ArchAxis = "M6E_ARCH"

// buildsContainer reports whether any tool is a container-build action — the
// producer of the per-cell OCI tar the arch axis exists to fan.
//
// It gates the minting so the DECLARATION alone is not enough: a project that
// declares architectures but builds no container (a Go CLI already fanning its
// own {GOOS,GOARCH} for binaries) keeps its pipeline unfanned, instead of
// doubling every job over a dimension none of its steps read.
func (st *Subtree) buildsContainer() bool {
	for _, man := range st.Tools {
		if man.Action == ActionContainerBuild {
			return true
		}
	}
	return false
}

// addAxis inserts a DERIVED axis, keeping Axes key-sorted — the order both the
// matrix lowering and artifactStem depend on for byte-stable output.
//
// An axis the author already declared under the same key WINS and the derived one
// is dropped: an explicit declaration is never silently overridden, and the axis
// cannot be minted twice into one matrix.
func (st *Subtree) addAxis(a Axis) {
	for _, ex := range st.Axes {
		if ex.Key == a.Key {
			genlog.Info("arch axis: axis already declared, keeping the authored one",
				"key", a.Key, "values", ex.Values)
			return
		}
	}
	st.Axes = append(st.Axes, a)
	sort.Slice(st.Axes, func(i, j int) bool { return st.Axes[i].Key < st.Axes[j].Key })
}

// KV is one key→value pair of a matrix.overrides row — used in both the MATCH
// (axis KEY → value) and the VARS (extra-var NAME → value) halves, key-sorted
// for byte-stable output.
type KV struct {
	Key   string
	Value string
}

// OverrideEntry is one matrix.overrides row: a MATCH (the subset of axis KEY→value
// pairs a cell must carry for the row to apply) plus VARS (the extra KEY→VALUE
// bindings added onto every matching cell). A row whose MATCH no cell satisfies
// is a parse error — an override only DECORATES cells the axes already enumerate; it
// never spawns new ones (the GHA "add cells" behaviour is deliberately refused,
// secure-by-default). VARS become matrix variables of the matching cells exactly
// like axes (lowered to ${{ matrix.<NAME> }} on a forge, a command-line var on
// m6e), so a build-arg or image placeholder referencing one varies per cell. A var
// left unset on some cell (a PARTIAL override) backstops to the build-arg default
// there — see render's matrixVarExpr — so an un-decorated cell keeps the default.
type OverrideEntry struct {
	Match []KV // axis KEY → value; empty => matches every cell (vars apply to all)
	Vars  []KV // extra NAME → value bound onto matching cells
}

// Exclusion is one matrix.exclude row: the axis KEY→value pairs a cell must ALL
// carry to be DROPPED from the product. It is the exact complement of an
// OverrideEntry — that one DECORATES a cell the axes enumerate, this one REMOVES
// one; neither ever mints a cell. The driver is a grid that is not fully
// buildable ({GOOS,GOARCH} mints darwin/riscv64, which no toolchain targets):
// subtracting the corner keeps ONE node owning the tool, where a second node for
// the survivor would multi-home it (node=job cannot place a diamond). Every key
// is a declared axis of the SAME matrix, every row matches ≥1 cell, and the rows
// together never empty the product — all three refused at parse (buildExcludes),
// because an exclusion that quietly excludes nothing is the costliest kind of typo.
type Exclusion []KV

// BuildArg is one decoded container-build argument: the build-arg NAME plus the
// SOURCE its value comes from and a Ref the source interprets. The render lowers
// (Source, Ref) to the concrete right-hand side per target (see render.Build) —
// the model itself never spells a vendor token, so the same arg lowers onto GHA,
// Forgejo, or a future engine. Decoded name-sorted for byte-stable output.
type BuildArg struct {
	Name   string // the build-arg name (Dockerfile ARG)
	Source string // BuildArgSources (var | file | ci) OR the internal SourceLiteral (a ${pf.path} string form)
	Ref    string // var name | file-path template | ci key | interpolated literal (SourceLiteral)
}

// BuildArgSources is the closed set of OUT-of-document value sources a build-arg
// OBJECT may declare — each a genuinely distinct resolution mechanism (a tagged
// union, not a named-entity enum standing in for a map): a runtime CI var, a
// per-cell file read, or a CI-context fact. An unknown source fails the parse (a
// typo must not silently drop a required build-arg). The retired `get:` in-
// document lookup is now the STRING form (`${pf.path}`, decoded to SourceLiteral),
// so it is deliberately NOT a member here.
var BuildArgSources = map[string]bool{
	SourceVar: true, SourceFile: true, SourceCI: true,
}

// BuildArgCIKeys is the closed set of abstract `ci:` context keys (today: the
// build version). The token stays abstract in the model; the per-target spelling
// (GHA `${{ github.ref_name }}`) is applied in the lowering.
var BuildArgCIKeys = map[string]bool{CIKeyVersion: true}

// Build-arg source kinds and CI-context keys — named so the parser, the render
// lowering, and the manifests all share one literal each.
const (
	SourceVar  = "var"  // runtime CI variable → ${{ vars.<ref> }}
	SourceFile = "file" // per-cell value read from a repo file at build time
	SourceCI   = "ci"   // abstract CI-context fact the target supplies
	// SourceLiteral is the INTERNAL source a STRING-form build-arg value decodes
	// to: an in-document `${pf.path}` reference the interpolation pass resolves to
	// a generation-time literal (the `${image.namespace}` identity args that used
	// to be `{get: image.namespace}`). Not a user-facing source token — it is
	// synthesised from the string wire form, never spelled in a document.
	SourceLiteral = "literal"

	CIKeyVersion = "version" // the build version (GHA: the git ref name)

	BoolTrue  = "true"
	BoolFalse = "false"
)

// Action tokens name the ci-actions library paths a tool's `action:` field
// dispatches to. The MODEL owns this vocabulary so the lowering (render) and the
// image-ref validator (ValidateImage) agree on ONE token — never a magic string
// duplicated across packages. render aliases these.
const (
	ActionContainerBuild   = "container-build"   // PRODUCES the cell-keyed OCI archive
	ActionOciPush          = "oci-push"          // CONSUMES it and pushes to a registry
	ActionContainerExec    = "container-exec"    // execs `run` INSIDE a running compose container (the fused live test)
	ActionSecretsProvision = "secrets-provision" // SYNTHETIC pre-dc-up-d step: materialise the .secrets/ tree (cloud half of org.projectfile.ci.secrets)
	ActionForgejoRelease   = "forgejo-release"   // CONSUMES the cell-keyed binary artifact and attaches it to a Forgejo release (create-then-attach across matrix cells)
)

// Manifest is one tool definition (org.projectfile.ci.tools.<tool>) — the
// EXECUTION metadata a render target needs but the lowering does not. Tool defs
// are projectfile, shipped by a d9t `*-tools` image as an `include` (the def
// travels with the image that satisfies it); the spec defines only the shape.
// Absent => the tool runs the bare target name with no container (the resolver
// never synthesises an auto-<name> prefix — that is D9T content, set explicitly).
type Manifest struct {
	Image string `json:"image"` // NAME of a org.projectfile.ci.images var holding the fully-formed image ref for this tool; empty => host runner. The generator resolves it to a nested CI expression (${{ vars.<NAME> || vars.<PARENT> || '<lit>' }}). MUST NOT be set with `action`.
	Run   string `json:"run"`   // entrypoint; defaults to the bare target name when empty
	// Network attaches the tool's container to a network so it can reach the LIVE
	// compose stack (dc-up-d) by compose SERVICE NAME — the ssh-audit case (black-box
	// audit of the running sshd). The only value today is the symbolic `live`, the
	// container companion of `fuse: live` (which co-locates the tool into the stack's
	// job): each render target maps it to the stack's own network (RunToolNetwork →
	// ${M6E_COMPOSE_PROJECT_NAME}-network, the name the pipeline compose file creates),
	// keeping the vendor spelling at render (Law 3). Host networking is deliberately not
	// modelled (port collisions + privilege). Empty => the default bridge (every non-live
	// tool). Only meaningful with `image:` (a host `run:` tool shares the runner's netns).
	Network string `json:"network"`
	// Fuse is a CO-LOCATION group name (e.g. `live`). Tools sharing a fuse group,
	// connected by `needs`, collapse into ONE job that runs each member's command
	// as an ordered step — because a stateful runtime (a compose stack) cannot span
	// jobs on an ephemeral runner. Pure co-location: no dispatch, no capability. On
	// a daemon-native target (m6e) fused members stay separate recipes. Empty => the
	// tool is its own job.
	Fuse string `json:"fuse"`
	// Action names a ci-actions library path (e.g. `container-build`) the tool lowers
	// to on a forge — the PRAGMATIC remainder of "not yet a plain container tool"
	// (checkout, build, publish). It is NOT a capability: equality is the goal, and
	// each will become an ordinary image tool when its image lands. Empty => the tool
	// runs its `run` entrypoint directly (the common case). On m6e an action tool keeps
	// its native recipe (the executor never lowers it).
	Action string `json:"action"`
	// Emit names the semantic EVENT this tool reports once it has succeeded (today
	// `ci.image.published`). It answers "WHAT did this produce" — the fact half of the
	// events model, distinct from the goal-level notify job, which answers "did the
	// goal pass". The resolver renders one extra webhook step directly after the tool's
	// own step, carrying the image, its tag cascade, the digest and the matrix cell.
	// Requires `action: oci-push`: the payload is read back from the files that action
	// drops, so a tool publishing no image has no fact to report (fail-closed at Load).
	// Inert without org.projectfile.events — that block supplies the webhook var the
	// step is gated on, so a project that has not opted in renders no step.
	Emit string `json:"emit"`
	// ReleaseAssetPath is the forgejo-release action's resolved binary path — the
	// unsuffixed org.projectfile.artifacts entry with kind=binary (e.g. dist/pf-cli),
	// looked up once during Load against the merged doc. The action suffixes it per
	// cell (→ dist/pf-cli-${GOOS}-${GOARCH}). NOT a JSON field: it is derived, never
	// authored. Empty for non-forgejo-release tools; a forgejo-release tool with zero
	// or >1 kind=binary artifact errors at Load (fail-closed — ambiguous attach target).
	ReleaseAssetPath string `json:"-"`
	// RawArgs is the on-the-wire build-args map (decoded in Parse → Args). The wire
	// shape is `args: {<NAME>: <value>}` — one build-arg per NAME, the VALUE either
	// an IN-document `${pf.path}` STRING or an OUT-of-document single-key object:
	//   "${image.namespace}" — an IN-document reference resolved at generation time
	//                  against the merged doc (decoded SourceLiteral; the identity args
	//                  that used to be `{get: image.namespace}`; any fieldpath resolves).
	//   {var: NAME}  — a runtime CI variable (→ ${{ vars.NAME }}); the registry, etc.
	//   {file: PATH} — a per-cell value READ from a repo file at build time; PATH may
	//                  embed {<axis>} placeholders (→ ${{ matrix.<axis> }}); the digest pin.
	//   {ci: KEY}    — an abstract CI-context fact the target supplies (version →
	//                  ${{ github.ref_name }}); kept abstract so a non-GHA engine maps it.
	// Invariant: in-document → `${…}` string, out-of-document → structured binding.
	// Vendor-NEUTRAL: the forge spelling is applied at render (like image/registry).
	// Generic args live in m6e/container; a namespace APPENDS its own (include-merge).
	RawArgs json.RawMessage `json:"args"`
	Args    []BuildArg      `json:"-"` // decoded RawArgs, name-sorted (see decodeBuildArgs)
	// Mounts is the unified mount LIST — ONE concept, no separate `caches`. Each entry
	// is a {from, to}: `to` is the CONTAINER mountpoint; `from` discriminates the lowering:
	//   * a bare NAME (no `/`, e.g. grype-db, npm, fetch) — a regenerable, managed volume.
	//     The canonical case is a scanner vuln DB or a package-manager cache: shared,
	//     daily-changing data that must NOT be baked into the immutable image (12-factor)
	//     nor downloaded per run. m6e binds $(XDG_CACHE_HOME)/<from>; a forge keys its own
	//     cache mechanism by the name (never spelled here — Law 3). On a plain run-tool it
	//     lowers to a single run-tool mount at the entry's `mode` (+ actions/cache restore
	//     on an ephemeral runner); on a container-build action it lowers to a build-context
	//     bind (satisfied EMPTY on an ephemeral runner).
	//   * a real PATH (`~/…`, `./…`, `/…`, e.g. the docker socket, a cert store) — a host
	//     bind only m6e can satisfy. The forge lowering IGNORES it (an ephemeral runner has
	//     no such host path). This replaces the old m6e-only `m6e.mounts`.
	// Empty => no mounts (the common case).
	Mounts []Mount `json:"mounts"`
	// Artifact is a filesystem path this tool PRODUCES (a built binary, a dir of
	// binaries). When set, the producer job uploads it as a per-CELL build artifact
	// (named from the tool + its cell axes), and any tool that NEEDS this one auto-
	// downloads it to the same path before running — the plain-`run:` analogue of the
	// container-build OCI-tar hand-off, for the binary-build → forge-release split. The
	// edge is DERIVED from the DAG `needs` (same cell ⇒ same name), never a second
	// manifest field on the consumer. The container-build action uploads its OCI tar
	// itself, so this field is for non-action tools. Empty => no shared artifact.
	Artifact string `json:"artifact"`
	// Reports is a filesystem path (or glob) of DIAGNOSTIC report files this tool emits
	// — a scanner's SARIF/JSON output. Unlike Artifact it is upload-ONLY: it never joins
	// the build→consumer download edge (nothing consumes a report), and its upload is
	// guarded `if: always()` with missing files tolerated, so a fail-CLOSED scan (a real
	// CVE exits non-zero) still leaves its report — the report is most useful exactly when
	// the scan goes red. The path is relative to the workspace the tool ran in (the
	// run-tool bind at /app/ws), so a report written inside the tool container lands in the
	// host checkout where upload-artifact finds it. Empty => no report upload.
	Reports string `json:"reports"`
	// Env are NAMES of runner env vars the tool needs in its execution context —
	// vendor-AGNOSTIC, names only, never values (no secrets in the manifest). The NAME
	// is what SCOPES a credential to the job that declares it: at render a name the
	// target's `credentials` overlay supplies is bound to that SECRET ref (`GH_TOKEN` →
	// ${{ secrets.GITHUB_TOKEN }}); a name with no credentials entry is left to the
	// runner's inherited environment (host `GOPROXY` — emitting `${{ env.NAME }}` would
	// resolve EMPTY off GHA's context and clobber it; honest container forwarding is a
	// later concern). The binding is target-specific (the secret VALUE), so Build
	// carries the NAMES neutral and Workflow resolves them. Absent => none (the common
	// case; auto-* tools need none).
	Env []string `json:"env"`
	// EnvSet is the VALUES-bearing sibling of Env: a map of env NAME -> LITERAL VALUE
	// the tool sets on its job's `env:` block. Where Env forwards credential NAMES
	// (bound from the target's secrets overlay) and `args` are build-args for the
	// container-build action, EnvSet carries authored, vendor-NEUTRAL literals a tool
	// needs at run with no secret content. The canonical case is the compose-runtime
	// vars a fused `live` job's compose file interpolates (COMPOSE_FILE + the
	// project/instance names) — the resolver injects only the one value it uniquely
	// derives (the loaded image ref, ImageFullnameEnv); everything else is authored
	// here so the m6e compose convention stays in the m6e include. Values may embed
	// {<axis>} placeholders, lowered per cell to ${{ matrix.<axis> }} like args/file
	// (substAxes). Empty => none.
	EnvSet map[string]string `json:"set-env"`
	// When marks WHEN this step runs relative to the OTHER steps of its job. The sole
	// value today is `always`: a cleanup/teardown member — the canonical case is a
	// `docker compose down` after a fused `live` job's `up`/`exec` — that must run even
	// when an EARLIER step failed, so a failing test never leaks the detached stack on a
	// host-mode (no per-job container) runner. Vendor-NEUTRAL: it lowers to the step
	// guard `if: ${{ always() }}` (Law 3 — the spelling lives in the template). Only
	// meaningful on a fused member (a lone step has nothing to clean up after). Empty =>
	// the step runs only when every earlier step succeeded (the default). This is the
	// per-STEP cousin of a node's `when:{events}` trigger predicate — same word, a
	// different axis (intra-job step ordering vs workflow-level event shaping).
	When string   `json:"when"`
	Tags []string `json:"tags"` // advisory open-vocabulary labels (no lowering semantics)
	// `guard` (a file-existence predicate) is DELIBERATELY not decoded here: this
	// lowering does not gate a tool on a file. The only fleet users (cffr-validate /
	// hadolint) name a file their bolt-on always ships, so the predicate is constant-
	// true — and the d9t runner self-guards a genuinely-absent file (a wasted-but-green
	// run, never a failure). The make lowering still honours `guard` via `test -f`
	// (clean, no inline shell), so the field stays valid spec; ci-resolver just trusts
	// the runner. Applicability belongs to MEMBERSHIP (include the fragment ⟺ have the
	// file), not a runtime predicate.
	// Lowering MEMBERSHIP, keyed by the TARGET NAME directly (a sibling of `m6e:`,
	// never under a `targets:` wrapper — one grammar: the target name is the key).
	// OPT-OUT: an absent key (nil) means the tool runs on this target; only an
	// explicit `false` removes it. The m6e lowering reads its own `m6e:` block;
	// ci-resolver renders exactly gha/forgejo (see render.Targets), so those are the
	// keys it consults — a new render target adds a field here next to its adapter row.
	GHA     *bool `json:"gha"`
	Forgejo *bool `json:"forgejo"`
}

// Mount is one entry of Manifest.Mounts: the source (`from`), the container
// mountpoint (`to`), and the access `mode`. `from` discriminates the lowering —
// see Manifest.Mounts.
type Mount struct {
	From string `json:"from"`
	To   string `json:"to"`
	// Mode is the container-side access this mount grants: `read-only` (the
	// default — secure by default; a scan READS a shared, refresh-owned DB) or
	// `read-write` (a WRITER node — the canonical case is a `*-db-update` that
	// REFRESHES the shared DB in place). Lowered to the docker `--volume` opt
	// (ro/rw) by VolOpt. Empty => read-only.
	Mode string `json:"mode"`
}

// VolOpt is the docker `--volume` access suffix for this mount: `rw` when Mode
// is `read-write`, else `ro` (the secure default — any other/absent value fails
// closed to read-only).
func (m Mount) VolOpt() string {
	if m.Mode == "read-write" {
		return "rw"
	}
	return "ro"
}

// IsPath reports whether this mount's `from` is a real host PATH (it contains a
// `/`) rather than a managed-volume NAME. A path mount is m6e-only — the forge
// lowering skips it (an ephemeral runner has no such host directory).
func (m Mount) IsPath() bool { return strings.Contains(m.From, "/") }

// EnabledFor reports whether this tool participates in the named lowering target.
// Not realistically 1:1 across vendors: a doc *-fix only makes sense under m6e (CI
// never commits the result), npm trusted-publishing is gha-only, image push is
// per-vendor — and `fetch` (a local cache warm-up) is pointless on an ephemeral
// cloud runner. An unrendered target (e.g. "m6e") is never asked about here.
func (m Manifest) EnabledFor(target string) bool {
	switch target {
	case "gha":
		return m.GHA == nil || *m.GHA
	case "forgejo":
		return m.Forgejo == nil || *m.Forgejo
	}
	return true
}

// Platform is the per-target DEPLOYMENT overlay (org.projectfile.ci.<target>):
// metadata that changes only WHERE/HOW a gate runs, never WHAT it means — so the
// lowering ignores it entirely and it is joined in only at render time, keyed by
// target. GHA and Forgejo share the shape (Forgejo Actions is GHA-compatible); a
// genuinely different engine (Tekton) will bring its own when it lands. The
// litmus (general-plan): changes what a gate means => core DAG; changes only
// where/how it runs => here.
type Platform struct {
	RunsOn         []string          // default runner label(s); overrides the adapter default
	TimeoutMinutes int               // per-job timeout applied to every job (0 => unset)
	Permissions    map[string]string // workflow-level GITHUB_TOKEN scopes (scope -> level)
	Concurrency    *Concurrency      // workflow-level concurrency group (nil => unset)
	Builder        string            // container-build backend override (buildx|buildah; "" => adapter default)
	// Credentials supplies the SECRET VALUE for a tool's declared env NAME on this
	// target: an {ENV_NAME: <secret-ref>} map (gha `GH_TOKEN: ${{ secrets.GITHUB_TOKEN
	// }}`). It is deployment, not DAG — a tool says "I need GH_TOKEN" via Manifest.Env;
	// THIS overlay binds that name to a target-specific secret at render. A name absent
	// here falls back to a plain runner passthrough, so build+test+scan jobs (no secret
	// names) stay green with no overlay at all (graceful degradation; scopable-away).
	Credentials map[string]string
	// Actions overrides the helper-action `uses:` refs the renderer otherwise takes
	// from the per-target adapter defaults (render.Target). It is a {slot: full-ref}
	// map — slot one of checkout|download-artifact|upload-artifact|ci-actions, value a
	// pinned `name@ref` exactly as you'd hand-write it (`actions/checkout@v7`). An
	// absent slot keeps the adapter default, so no overlay renders byte-identical to
	// before. Engine-concrete by design (a `uses:` token is GHA/Forgejo vocabulary, not
	// abstract DAG) — hence here, not in the model. Author it ONCE in a shared include
	// so the fleet stays single-source rather than drifting per projectfile.
	Actions map[string]string
	// CheckoutToken opts the checkout step into the ROBOT-ACCOUNT token, so a job
	// reaches a repository the run-scoped token cannot. The forge mints its per-run
	// token for ONE repository; a submodule — above all one with a RELATIVE url, which
	// resolves against the superproject remote and so lands on a SIBLING repository of
	// the same host — is outside that scope, and a private one fails to clone. A robot
	// account with read on those repositories closes the gap over HTTPS, which is why
	// no deploy key is involved: the checkout action already copies its own credential
	// into every submodule it populates.
	//
	// A BOOLEAN, not a name: the secret is ONE fleet-wide constant the renderer owns
	// (render.RobotTokenSecret), so an operator provisions it ONCE per organisation and
	// every project spells it the same way — convention over configuration, and nothing
	// secret enters the document (§8). False => the checkout keeps the run-scoped token,
	// i.e. no overlay renders byte-identical to before.
	CheckoutToken bool
}

// Concurrency is the workflow-level serialisation group: a second run in the same
// group cancels the first when CancelInProgress is set (PR-push churn control).
type Concurrency struct {
	Group            string
	CancelInProgress bool
}

// Subtree is the normalised, includes-merged org.projectfile.ci value.
type Subtree struct {
	Goals         []string        // goal nodes (flagged goal:true), sorted (empty => infer every sink)
	GoalsExplicit bool            // any node flagged goal:true (else fall back to sink inference)
	Image         string          // built-image BASENAME: registry-relative path (`b19/ubuntu`, composed <registry>/<image>:<tag> at render) OR a complete `:tag`-bearing ref (verbatim). Explicit `image:` wins; empty => Load DERIVES <last-label(identity.namespace)>/<identity.name>. Stamped into the OCI archive (container-build name=) so a consumer `docker load`s a TAGGED image (no anonymous archives).
	Axes          []Axis          // matrix axes, key-sorted (empty => no matrix)
	Overrides     []OverrideEntry // matrix.overrides rows (extra per-cell vars keyed by an axis-value match); empty => none
	Excludes      []Exclusion     // matrix.exclude rows (cells the axes mint but nothing builds); empty => the full grid
	Nodes         map[string]Node
	NodeOrder     []string            // node names, sorted — deterministic iteration
	Tools         map[string]Manifest // tool name -> execution manifest (may be empty)
	Platforms     map[string]Platform // target key ("gha"|"forgejo") -> deployment overlay
	// Env is the workflow-level env block: NAME -> make-plane VALUE (a string that may
	// carry ${VAR} make refs, lowered per target at render). Every job of every
	// generated workflow inherits it — the compose plane's `${D9T_DIND_IMAGE}` lands
	// here so dc-up-d AND dc-down read it with no per-tool duplication. Empty => none.
	Env map[string]string
}

// Dispatch is the manual-run trigger: the button plus its optional typed inputs (the
// parametrised-build form). Inputs are name-sorted for byte-stable output.
type Dispatch struct {
	Inputs []Input
	// BuildArgs opts the goal's run form into AUTO-exposing every declared
	// org.projectfile.build.args input as its own typed dispatch input, pre-filled with
	// that arg's default, so a manual run may override any build-arg without redeclaring
	// it here (DRY: build.args stays the single source of truth). The container-build
	// lowering then prefers the entered value — ${{ inputs.NAME || vars.NAME || 'default' }}
	// — falling through to today's var/default off a non-dispatch run. Only literal- and
	// empty-default STRING args are exposed; a `file:` (per-cell digest) arg and a composed
	// `${...}` default cannot round-trip as a plain scalar, so they are skipped. false =>
	// only the explicit `inputs:` render (back-compatible). An explicit input of the same
	// NAME wins (the author may have given it a choice/description).
	BuildArgs bool
}

// Input is one manual-dispatch parameter — the vendor-NEUTRAL subset GHA and Forgejo
// render identically. Type defaults to "string" when omitted; HasDefault distinguishes
// an authored "" from no default at all.
type Input struct {
	Name        string   // the input name (→ ${{ inputs.<Name> }})
	Type        string   // one of InputTypes: string | boolean | choice | number
	Description string   // free-prose label shown on the run form (optional)
	Required    bool     // the form refuses an empty value
	Default     string   // default value (see HasDefault)
	HasDefault  bool     // whether a default was authored (distinguishes "" from unset)
	Options     []string // choice values; REQUIRED for type=choice, forbidden otherwise
}

// InputTypes is the closed, vendor-NEUTRAL set of manual-dispatch input types — the
// intersection GHA and Forgejo render the same. An unknown type fails the parse (a
// typo must not silently degrade the form). A GHA-only `environment` type is excluded
// on purpose: it has no Forgejo equivalent, so it would break the litmus test.
var InputTypes = map[string]bool{
	InputString: true, InputBoolean: true, InputChoice: true, InputNumber: true,
}

const (
	InputString  = "string"
	InputBoolean = "boolean"
	InputChoice  = "choice"
	InputNumber  = "number"
)

// ForTarget returns a copy of the subtree with every tool DISABLED for the named
// target removed — both from `.Tools` and from every node's `needs`. It is the
// one target-aware pre-pass: the neutral resolver (conformance-pinned) must never
// see a target, so membership is applied HERE, before Resolve, by handing it a
// subtree that already lacks the dropped leaves. The resolver's abstract-node
// contraction then does the rest for free: a node whose only tool was pruned
// collapses to a pure join and contracts away, so nothing downstream dangles.
//
// No tool disabled for this target => the original is returned unchanged (the
// pipeline stays byte-identical — graceful degradation, the opt-out is additive).
func (st *Subtree) ForTarget(target string) *Subtree {
	if st == nil {
		return nil
	}
	disabled := make(map[string]bool)
	for name, man := range st.Tools {
		if !man.EnabledFor(target) {
			disabled[name] = true
		}
	}
	if len(disabled) == 0 {
		return st
	}
	out := *st // copy scalars/slices; Nodes+Tools are rebuilt below so the original is untouched
	out.Nodes = make(map[string]Node, len(st.Nodes))
	for name, n := range st.Nodes {
		kept := make([]Need, 0, len(n.Needs))
		for _, need := range n.Needs {
			if disabled[need.Target] {
				continue // drop the edge to a tool excluded from this target
			}
			kept = append(kept, need)
		}
		n.Needs = kept
		out.Nodes[name] = n
	}
	out.Tools = make(map[string]Manifest, len(st.Tools))
	for name, man := range st.Tools {
		if !disabled[name] {
			out.Tools[name] = man
		}
	}
	return &out
}

// ForGoal returns a copy of the subtree pinned to a SINGLE goal — the one-file-per-
// goal projection. The resolver's reachability (goals → needs-closure) then narrows
// the model to exactly that goal's subgraph for free, so each goal renders to its own
// workflow file (`<goal>.yaml` by default) with its own `on:` surface. The shared node/tool maps
// are reused as-is (read-only after Load); only the goal selection is rewritten.
//
// A goal genuinely shared by two goals' closures would render in both files (a
// re-run) — by design goals are disjoint terminal milestones, so this does not
// happen in practice; it is not silently deduplicated.
func (st *Subtree) ForGoal(name string) *Subtree {
	if st == nil {
		return nil
	}
	out := *st
	out.Goals = []string{name}
	out.GoalsExplicit = true
	return &out
}

// ---------------------------------------------------------------------------
// raw* mirror the on-the-wire JSON shape so we can decode the polymorphic
// `needs` (scalar OR sequence OR mapping) and need-values (bool | {args}).
// ---------------------------------------------------------------------------

type rawSubtree struct {
	Image string            `json:"image"` // built-image basename (explicit; empty => derived from identity in Load)
	Env   map[string]string `json:"env"`   // workflow-level env: NAME -> make-plane VALUE, inherited by every job
	// Matrix is the GLOBAL matrix object, kept raw so decodeMatrix can decode it
	// STRICTLY (an unsupported key must not vanish silently — see decodeMatrix).
	Matrix  json.RawMessage     `json:"matrix"`
	Nodes   map[string]rawNode  `json:"nodes"`
	Tools   map[string]Manifest `json:"tools"`
	Gha     *rawPlatform        `json:"gha"`     // GitHub Actions deployment overlay
	Forgejo *rawPlatform        `json:"forgejo"` // Forgejo Actions deployment overlay
}

type rawDispatch struct {
	Inputs    map[string]rawInput `json:"inputs"`
	BuildArgs bool                `json:"build-args"` // auto-expose org.projectfile.build.args as dispatch inputs
}

type rawSchedule struct {
	Cron string `json:"cron"`
}

type rawInput struct {
	Type        string           `json:"type"`
	Description string           `json:"description"`
	Required    bool             `json:"required"`
	Default     *json.RawMessage `json:"default"` // scalar; nil => no default
	Options     []string         `json:"options"`
}

// rawPlatform mirrors the org.projectfile.ci.<target> wire shape. The keys are
// the vendor's own spelling (kebab-case GHA YAML) so an author reads one mental
// model whether the field lands in the projectfile or the workflow.
type rawPlatform struct {
	RunsOn         json.RawMessage   `json:"runs-on"` // string OR []string (both legal in GHA)
	TimeoutMinutes int               `json:"timeout-minutes"`
	Permissions    map[string]string `json:"permissions"`
	Concurrency    *rawConcurrency   `json:"concurrency"`
	Builder        string            `json:"builder"`        // container-build backend override (buildx|buildah)
	Credentials    map[string]string `json:"credentials"`    // {ENV_NAME: <secret-ref>} bound to a tool's env name at render
	Actions        map[string]string `json:"actions"`        // {slot: name@ref} helper-action ref overrides (checkout|download-artifact|upload-artifact|ci-actions)
	CheckoutToken  bool              `json:"checkout-token"` // opt the checkout step into the fleet-wide robot-account secret (private submodules)
}

type rawConcurrency struct {
	Group            string `json:"group"`
	CancelInProgress bool   `json:"cancel-in-progress"`
}

type rawMatrix struct {
	Axes      map[string]json.RawMessage   `json:"axes"`
	Overrides []map[string]json.RawMessage `json:"overrides"`
	Exclude   []map[string]json.RawMessage `json:"exclude"`
	Without   []string                     `json:"without"`
}

type rawNode struct {
	Goal bool `json:"goal"`
	// Matrix is a bool (true => CELL over the GLOBAL axes) OR an object
	// `{axes: {...}}` (CELL over the node's OWN axes). Decoded by decodeNodeMatrix.
	Matrix json.RawMessage `json:"matrix"`
	// MaxParallel mirrors GHA's strategy.max-parallel — cap on concurrent matrix cells
	// for THIS node. 0/absent => the forge default (all cells at once).
	MaxParallel int             `json:"max-parallel"`
	Concurrency *rawConcurrency `json:"concurrency"` // optional per-node serialisation group (same shape as the platform one)
	Needs       json.RawMessage `json:"needs"`
	When        *rawWhen        `json:"when"`     // optional trigger predicate (decoded by decodeWhen)
	Schedule    []rawSchedule   `json:"schedule"` // cron timer for a goal node (mirrors GHA `- cron:`)
	Dispatch    *rawDispatch    `json:"dispatch"` // manual-run button for a goal node (+typed inputs)
}

// rawWhen mirrors the on-the-wire `when: {events: [...]}` shape. A nil pointer
// (the field absent) means "no predicate" — the node runs on every event.
type rawWhen struct {
	Events []string `json:"events"`
}

// Build holds the data the resolver needs at CI render time, drawn from TWO
// subtrees the agnostic revolution split apart: the RUN-images a tool executes
// in (org.projectfile.ci.images — the registry path a tool image resolves to) and
// the container-build ARGs (org.projectfile.build.args — auto-forwarded as
// --build-args). A nil Build is valid — the renderer falls back to a bare
// ${{ vars.<VAR> }} image and skips auto-forwarding.
type Build struct {
	// Images is the org.projectfile.ci.images map: var NAME → fully-formed run-image
	// value (e.g. "B19_GO_IMAGE" → "${B19_DOCKER_REGISTRY}/b19/go:${B19_GO_VERSION}").
	// Used to derive the parent image var and the path-only registry path a tool image
	// lowers to. A run-image is a `docker run` concern, so it is NEVER auto-forwarded
	// as a build-arg.
	Images map[string]string
	// Args is the org.projectfile.build.args list: the Dockerfile ARGs (FROM refs,
	// versions) container-build needs, auto-forwarded as --build-args (see
	// render.toolStep). Mirrors the make reader's --build-arg auto-emit.
	Args []BuildInput
	// BuildTarget is the org.projectfile.ci.build-target map: lowering key
	// ("m6e"|"gha"|"forgejo") → the Dockerfile stage that lowering builds
	// (buildx/buildah --target). The CI render reads the forge keys (gha/forgejo); the
	// make plane reads "m6e" itself. Absent => the action omits --target => the
	// Dockerfile's last stage (legacy single-stage behaviour).
	BuildTarget map[string]string
	// Events is the org.projectfile.events subtree (the events model, phase 1). Non-nil
	// => the project OPTED IN to lifecycle emission, so the resolver appends a webhook
	// `notify` job to each goal workflow (gated at RUNTIME on the webhook var being set —
	// secure default: an opted-in repo with no var configured emits nothing). nil => the
	// project declared no events, so no notify job is rendered. The m6e side lowers the
	// SAME events to a desktop notification; both emit the identical neutral envelope.
	Events *Events
	// Secrets is the RAW org.projectfile.ci.secrets JSON — the dev/CI secret-provisioning
	// manifest a compose `secrets:` block mounts. Captured VERBATIM (not decoded into a Go
	// map): secret names carry dots, which would defeat a flat dump (the Phase-4 decision
	// in m6e), and provision.sh parses the EXACT pf-cli `--format json` output, so
	// decoding+re-encoding risks byte drift (key order, shorthand normalisation). nil when
	// the subtree is absent — the render-side invariant: a nil Secrets injects NO
	// secrets-provision step (the empty-subtree no-op). Non-nil ⇒ render injects a
	// SYNTHETIC secrets-provision step before dc-up-d in the live (fused) job.
	Secrets json.RawMessage
	// PublishRefs is the composed destination list per LOWERING key ("gha"|"forgejo"):
	// where a pipeline of that lowering publishes to. Composed at LOAD time from
	// org.projectfile.publish.<forge>.push (the sink NAMES) and each
	// org.projectfile.sinks.<name>.ref (the whole grammar), so render threads finished
	// references and knows no path shape. Empty => the project declares no route, and
	// oci-push keeps its single OUTPUT_REGISTRY destination.
	PublishRefs map[string][]SinkRef
	// PullRefs is the READ destination per LOWERING key: the one place a consumer of
	// this project is sent to, composed from org.projectfile.publish.<forge>.pull and
	// that sink's own `ref`. Its consumer is the `audited` re-scan target, so an audit
	// names the destination the document declares instead of gluing a registry prefix
	// onto a basename. Empty => no route declares a pull, and render falls back to the
	// single OUTPUT_REGISTRY destination.
	PullRefs map[string]SinkRef
	// ReleaseTargets is the binaries-plane twin: where a pipeline of that lowering
	// ATTACHES its release, composed at LOAD time from org.projectfile.publish.<forge>.
	// release (the forge names) and the source-code links those names match. Empty =>
	// the project releases only on the forge it runs on, which is the ambient Forgejo
	// Actions context and needs no input at all.
	ReleaseTargets map[string][]ReleaseTarget
}

// SinkRef is one composed publish destination: the sink NAME the credentials key on,
// and the reference that sink's own template states. A `{AXIS}` placeholder survives
// verbatim — it carries no `$`, so composition never touches it and render substitutes
// it per matrix cell, exactly as it does for the image basename.
type SinkRef struct {
	Sink string
	Ref  string
}

// Events is the decoded org.projectfile.events subtree. Phase 1 is intentionally
// minimal: a single built-in webhook sink whose target VAR name is all the resolver
// needs. The URL VALUE lives in the forge (a repo/org variable or secret), NEVER in
// the projectfile — so it is never committed and never rendered into the workflow.
type Events struct {
	// WebhookVar is the forge var/secret NAME the emit step reads (the `${VAR}` in
	// `org.projectfile.events.webhook.url`, or the DefaultWebhookVar when the block is
	// present without an explicit ref). The workflow gates the notify job on this var
	// being non-empty, so nothing is sent unless the operator sets it.
	WebhookVar string
}

// DefaultWebhookVar is the phase-1 built-in webhook sink var: the name a project's
// notify job reads when `org.projectfile.events` is declared without a custom
// `webhook.url` ref. Mirrors the EVENTS_WEBHOOK_URL the m6e emit script reads, so the
// local and forge sinks are configured through the ONE name.
const DefaultWebhookVar = "EVENTS_WEBHOOK_URL"

// rawEvents mirrors the on-the-wire org.projectfile.events shape for the one field
// the resolver reads in phase 1: the webhook sink's URL BINDING — an explicit
// `{ env: NAME }` runtime-env reference (D6), NEVER a bare `${VAR}` string (that
// spelling is retired so a runtime-env reference is never confused with a
// generation-time `${pf.path}`). The future sinks{}/on{} subscription surface is
// deliberately not decoded yet (YAGNI).
type rawEvents struct {
	Webhook struct {
		URL struct {
			Env string `json:"env"`
		} `json:"url"`
	} `json:"webhook"`
}

// webhookVar returns the forge var/secret NAME the emit step reads: the explicit
// `{ env: NAME }` binding, or DefaultWebhookVar when the events block is present
// without one (so the mere presence of the block still enables the built-in sink).
func webhookVar(re rawEvents) string {
	if re.Webhook.URL.Env != "" {
		return re.Webhook.URL.Env
	}
	return DefaultWebhookVar
}

// BuildInput is one org.projectfile.build.args entry: a Dockerfile ARG that
// container-build auto-forwards. Unlike a ci.tools.<t>.args BuildArg (a tagged
// {source:ref} union), a build input carries a literal default OR a per-cell file
// read — the wire shapes the make reader also lowers. File and Default are
// mutually exclusive; an empty Default means "forward only when vars.<NAME> is
// set" (no `|| 'literal'` fallback, so an undefined var cannot clobber a
// Dockerfile ARG default with an empty value).
type BuildInput struct {
	Name    string
	Default string // literal default; "" => forward only when vars.<NAME> is set
	File    string // file-path template; non-empty => per-cell file read
}

// rawBuild mirrors the on-the-wire org.projectfile.build JSON shape for the one
// field the resolver needs. Remaining fields (registries, env) are ignored.
type rawBuild struct {
	Args json.RawMessage `json:"args"`
}

// LoadBuild reads the optional subtrees the resolver needs: the run-image map
// (org.projectfile.ci.images) and the container-build args (org.projectfile.build
// .args). If neither is present, returns (nil, nil) and the renderer falls back to
// single-tier expressions with no auto-forwarding. Reads through the library seam
// (core's includes-merged document), not a subprocess.
func LoadBuild(pfPath string) (*Build, error) {
	r, err := newReader(pfPath)
	if err != nil {
		return nil, err
	}
	imgRaw, err := r.subtree("org.projectfile.ci.images")
	if err != nil {
		return nil, err
	}
	var images map[string]string
	if len(imgRaw) > 0 {
		if err := json.Unmarshal(imgRaw, &images); err != nil {
			return nil, fmt.Errorf("org.projectfile.ci.images: parse: %w", err)
		}
	}
	// build-target: per-lowering Dockerfile stage selection (the forge keys feed the
	// container-build action's `target:` input; the make plane reads its own key).
	btRaw, err := r.subtree("org.projectfile.ci.build-target")
	if err != nil {
		return nil, err
	}
	var buildTarget map[string]string
	if len(btRaw) > 0 {
		if err := json.Unmarshal(btRaw, &buildTarget); err != nil {
			return nil, fmt.Errorf("org.projectfile.ci.build-target: parse: %w", err)
		}
	}
	buildRaw, err := r.subtree("org.projectfile.build")
	if err != nil {
		return nil, err
	}
	var rb rawBuild
	if len(buildRaw) > 0 {
		if err := json.Unmarshal(buildRaw, &rb); err != nil {
			return nil, fmt.Errorf("org.projectfile.build: parse: %w", err)
		}
	}
	inputs, err := decodeBuildInputs(rb.Args)
	if err != nil {
		return nil, fmt.Errorf("org.projectfile.build: %w", err)
	}
	// The events model is opt-in and orthogonal to images/build: a project may declare
	// org.projectfile.events with no container-build at all. Presence of the subtree
	// enables the webhook notify job (gated at runtime on the var); its resolved var
	// name defaults to DefaultWebhookVar or comes from an explicit `{ env: NAME }` binding.
	evRaw, err := r.subtree("org.projectfile.events")
	if err != nil {
		return nil, err
	}
	var events *Events
	if len(evRaw) > 0 {
		var re rawEvents
		if err := json.Unmarshal(evRaw, &re); err != nil {
			return nil, fmt.Errorf("org.projectfile.events: parse: %w", err)
		}
		events = &Events{WebhookVar: webhookVar(re)}
	}
	// Secrets is captured RAW (verbatim JSON) — provision.sh parses it as-is, so no
	// Go decoding (see Build.Secrets). Same subtree probe images/events use; an
	// absent subtree yields nil (the no-op invariant).
	secRaw, err := r.subtree("org.projectfile.ci.secrets")
	if err != nil {
		return nil, err
	}
	publishRefs, err := r.publishRefs()
	if err != nil {
		return nil, err
	}
	pullRefs, err := r.pullRefs()
	if err != nil {
		return nil, err
	}
	releaseTargets, err := r.releaseTargets()
	if err != nil {
		return nil, err
	}
	if len(images) == 0 && len(inputs) == 0 && events == nil && len(secRaw) == 0 &&
		len(buildTarget) == 0 && len(publishRefs) == 0 && len(pullRefs) == 0 && len(releaseTargets) == 0 {
		return nil, nil
	}
	return &Build{
		Images: images, Args: inputs, Events: events, Secrets: secRaw,
		BuildTarget: buildTarget, PublishRefs: publishRefs, PullRefs: pullRefs,
		ReleaseTargets: releaseTargets,
	}, nil
}

// rawPublish is one org.projectfile.publish.<forge> route. `pull` names the ONE
// destination a consumer of this project READS from — the ref the readme prints and
// the ref the `audited` re-scan pulls. It is declared rather than derived from
// `priority`, because a pipeline reads from where it is cheapest to read (kiota builds
// from kiota) and that is not the destination a reader is sent to. It does NOT answer
// where a build sources its BASE images: those are foreign coordinates, resolved
// through org.projectfile.images and the registry vars.
//
// `push` and `release` are the two PLANES, kept apart because they are: an image is a
// ref composed from a sink template, a release is a tarball attached to a git tag by a
// token. A release destination therefore names a FORGE, never a sink.
type rawPublish struct {
	Push    []string `json:"push"`
	Pull    string   `json:"pull"`
	Release []string `json:"release"`
}

// ReleaseTarget is one composed release destination: the NAME the token derives from,
// the forge base URL to attach on, and the repository path ON that forge. Repo is
// carried rather than derived because it differs per forge — kiota holds
// projectfile/bridge, GitHub holds damian-buho/projectfile-bridge.
type ReleaseTarget struct {
	Sink string
	URL  string
	Repo string
}

// releaseTargets resolves every declared release destination, keyed by the LOWERING
// that attaches it — the binaries-plane twin of publishRefs.
//
// A route names forge SLUGS; the coordinates come from the source-code links the
// project already declares, matched by the same first-domain-label rule
// originForgeSlug uses. Nothing is restated: the URL lives once, in links.
//
// No `release` key => nil, and the release action keeps the ambient Forgejo context —
// the single-forge behaviour every project has today.
func (r *Reader) releaseTargets() (map[string][]ReleaseTarget, error) {
	routes, err := r.publishRoutes()
	if err != nil || len(routes) == 0 {
		return nil, err
	}
	forgeLinks := map[string]ReleaseTarget{}
	for _, l := range r.doc.Links {
		if l.Type != linkSourceCode {
			continue
		}
		slug, base, repo := splitForgeURL(l.URL)
		if slug == "" || repo == "" {
			continue
		}
		// First in document order wins, the rule slug collisions already use.
		if _, seen := forgeLinks[slug]; !seen {
			forgeLinks[slug] = ReleaseTarget{Sink: slug, URL: base, Repo: repo}
		}
	}
	out := map[string][]ReleaseTarget{}
	for lowering, forge := range r.publishForges(routes) {
		for _, name := range routes[forge].Release {
			target, ok := forgeLinks[name]
			if !ok {
				// A destination with no link has no repository path, and guessing one
				// would attach a release to a repository nobody named.
				genlog.Warn("release destination dropped — no source-code link declares it",
					"forge", forge, "destination", name,
					"remedy", "declare a links[type=source-code] entry on "+name)
				continue
			}
			out[lowering] = append(out[lowering], target)
		}
	}
	return out, nil
}

// linkSourceCode is the link type a forge repository is declared under (spec §4.7).
const linkSourceCode = "source-code"

// splitForgeURL decomposes a forge repository URL into the slug a route names it by,
// the base URL a release attaches on, and the `<owner>/<repo>` path on that forge.
// Any transport, because a link may be written in any of them.
func splitForgeURL(raw string) (slug, base, repo string) {
	rest := raw
	// The base is what a release client talks HTTP to, so a git transport (ssh://,
	// git://) resolves to the forge's web origin rather than being carried through.
	scheme := "https"
	if i := strings.Index(rest, "://"); i >= 0 {
		if s := rest[:i]; s == "http" || s == "https" {
			scheme = s
		}
		rest = rest[i+3:]
	}
	if i := strings.Index(rest, "@"); i >= 0 {
		rest = rest[i+1:]
	}
	// Host and path are split on the first slash — except in the scp-style
	// `git@host:owner/repo.git`, where the colon is the separator. A colon may also
	// introduce a PORT, and the two are told apart by what follows it: a port is
	// digits only. The port stays in the BASE (it addresses the forge) and never
	// reaches the slug (which is the first domain label).
	authority, path, _ := strings.Cut(rest, "/")
	host := authority
	if h, after, ok := strings.Cut(authority, ":"); ok {
		if _, err := strconv.Atoi(after); err == nil {
			host = h
		} else {
			host, authority, path = h, h, after+"/"+path
		}
	}
	label, _, _ := strings.Cut(host, ".")
	return label, scheme + "://" + authority, strings.TrimSuffix(strings.Trim(path, "/"), ".git")
}

// publishRoutes reads the org.projectfile.publish table once for both planes. A route
// is one document fact; reading it twice would let the two planes disagree about which
// forges the project publishes from at all.
func (r *Reader) publishRoutes() (map[string]rawPublish, error) {
	pubRaw, err := r.subtree("org.projectfile.publish")
	if err != nil || len(pubRaw) == 0 {
		return nil, err
	}
	var routes map[string]rawPublish
	if err := json.Unmarshal(pubRaw, &routes); err != nil {
		return nil, fmt.Errorf("org.projectfile.publish: parse: %w", err)
	}
	return routes, nil
}

// publishRefs composes every declared destination, keyed by the LOWERING that
// publishes it. The document keys routes by FORGE slug, because a route is a fact
// about a forge; render works in lowerings, because that is what a workflow file is.
// Mapping the two here keeps render free of forge knowledge and leaves exactly one
// place to correct when a project gains a third home.
func (r *Reader) publishRefs() (map[string][]SinkRef, error) {
	routes, err := r.publishRoutes()
	if err != nil || len(routes) == 0 {
		return nil, err
	}
	sinks, err := r.sinkTemplates()
	if err != nil {
		return nil, err
	}
	out := map[string][]SinkRef{}
	for lowering, forge := range r.publishForges(routes) {
		for _, sink := range routes[forge].Push {
			if ref, ok := r.composeSink(sinks, forge, sink, "publish"); ok {
				out[lowering] = append(out[lowering], ref)
			}
		}
	}
	return out, nil
}

// pullRefs composes the destination each lowering READS from — `publish.<forge>.pull`,
// keyed by lowering exactly as publishRefs keys its push list. One ref per lowering,
// because `pull` is a scalar: a reader is sent to one place.
//
// Its consumer is the `audited` image re-scan, which has no build in scope and must
// name what it pulls. Composing it here rather than gluing a prefix in render is what
// makes the audit read the SAME grammar the sink declared — a destination holding a
// flat path (Docker Hub) is audited at its flat path, with no code aware of either
// shape. No route, or a route with no `pull` => empty, and render keeps the single
// OUTPUT_REGISTRY destination every project has today.
func (r *Reader) pullRefs() (map[string]SinkRef, error) {
	routes, err := r.publishRoutes()
	if err != nil || len(routes) == 0 {
		return nil, err
	}
	sinks, err := r.sinkTemplates()
	if err != nil {
		return nil, err
	}
	out := map[string]SinkRef{}
	for lowering, forge := range r.publishForges(routes) {
		if ref, ok := r.composeSink(sinks, forge, routes[forge].Pull, "pull"); ok {
			out[lowering] = ref
		}
	}
	return out, nil
}

// sinkTemplates reads the org.projectfile.sinks table as VALUES. Expanding
// `${org.projectfile.sinks.X.ref}` instead would spend one re-expansion level on the
// lookup itself, and a template whose parts nest (`path` → `${org}/${name}` →
// `${identity.name}`) then exhausts core's depth bound and reports unresolved on a
// reference that composed fine. The readme bridge expands the template for the same
// reason.
func (r *Reader) sinkTemplates() (map[string]string, error) {
	sinkRaw, err := r.subtree("org.projectfile.sinks")
	if err != nil || len(sinkRaw) == 0 {
		return nil, err
	}
	var sinks map[string]struct {
		Ref string `json:"ref"`
	}
	if err := json.Unmarshal(sinkRaw, &sinks); err != nil {
		return nil, fmt.Errorf("org.projectfile.sinks: parse: %w", err)
	}
	out := make(map[string]string, len(sinks))
	for name, s := range sinks {
		out[name] = s.Ref
	}
	return out, nil
}

// composeSink resolves ONE destination name to its composed reference. Shared by both
// route planes so a sink cannot mean one path when pushed to and another when pulled
// from; `plane` names the caller in the drop warnings and nothing else.
func (r *Reader) composeSink(sinks map[string]string, forge, sink, plane string) (SinkRef, bool) {
	if sink == "" {
		return SinkRef{}, false
	}
	tmpl := sinks[sink]
	if tmpl == "" {
		genlog.Warn(plane+" sink dropped — declares no ref template",
			"forge", forge, "sink", sink,
			"remedy", "declare ref on org.projectfile.sinks."+sink)
		return SinkRef{}, false
	}
	// Composed by core's engine under the image scope — the SAME call the readme
	// bridge and the make plane make, so the three planes cannot disagree about where
	// this project publishes.
	ref, resolved := interp.ExpandIn(r.doc, tmpl, imageScope)
	if !resolved {
		// A half-composed reference is a well-formed name for the WRONG repository, so
		// it is dropped rather than published to or audited.
		genlog.Warn(plane+" sink dropped — ref left unresolved",
			"forge", forge, "sink", sink, "template", tmpl, "composed", ref,
			"remedy", "declare the missing part under "+imageScope)
		return SinkRef{}, false
	}
	return SinkRef{Sink: sink, Ref: ref}, true
}

// The lowering keys a route can apply to, and the one forge slug that is a property
// of a lowering rather than of a project: `gha` IS GitHub Actions. A Forgejo lowering
// names no fixed forge — kiota and Codeberg both speak it — so its slug is read from
// the document.
const (
	LoweringGHA     = "gha"
	LoweringForgejo = "forgejo"
	forgeGitHub     = "github"
)

// publishForges answers which forge slug each lowering runs on. A Forgejo lowering
// runs on whichever instance hosts the ORIGIN, which only the document knows — so it
// is read, never assumed, and a route keyed by that slug is the one that applies.
func (r *Reader) publishForges(routes map[string]rawPublish) map[string]string {
	forges := map[string]string{}
	if _, ok := routes[forgeGitHub]; ok {
		forges[LoweringGHA] = forgeGitHub
	}
	if slug := r.originForgeSlug(); slug != "" {
		if _, ok := routes[slug]; ok {
			forges[LoweringForgejo] = slug
		}
	}
	return forges
}

// originForgeSlug is the first domain label of the origin repository's host — the
// same slug rule the readme bridge derives its forge remotes with, so a route keyed
// `kiota` matches the kiota.ch origin without anyone restating the mapping.
func (r *Reader) originForgeSlug() string {
	// `repositories` is a LIST, so the selector is the bracket form. The brace form
	// selects within a MAP, and using it here silently answers nothing.
	raw, ok := interp.ExpandIn(r.doc, "${repositories[role=origin].url}", imageScope)
	if !ok || raw == "" {
		return ""
	}
	// Any transport: ssh://git@host/o/r.git, https://host/o/r, git@host:o/r.git.
	host := raw
	if i := strings.Index(host, "://"); i >= 0 {
		host = host[i+3:]
	}
	if i := strings.Index(host, "@"); i >= 0 {
		host = host[i+1:]
	}
	host = strings.FieldsFunc(host, func(c rune) bool { return c == '/' || c == ':' })[0]
	label, _, _ := strings.Cut(host, ".")
	return label
}

// Load runs pf-cli against the projectfile at dir (empty => auto-discover the
// newest sibling, pf-cli's production behaviour) and returns the merged subtree.
// A project that declares no org.projectfile.ci subtree yields (nil, nil): no CI
// is not an error (graceful degradation), the caller emits nothing.
func Load(pfPath string) (*Subtree, error) {
	r, err := newReader(pfPath)
	if err != nil {
		return nil, err
	}
	raw, err := r.subtree("org.projectfile.ci")
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		// No org.projectfile.ci subtree is "no CI", not a failure — the caller
		// emits nothing (graceful degradation).
		return nil, nil
	}
	st, err := Parse(raw)
	if err != nil || st == nil {
		return st, err
	}
	// Convention over configuration: an explicit `image:` wins (read verbatim by
	// Parse); otherwise the built-image basename comes from core's SINGLE home of
	// the rule (`image.basename` = org.projectfile.ci.image else
	// <last-label(identity.namespace)>/<identity.name>). m6e's M6E_IMAGE_BASENAME
	// reads the same rule via `projectfile get image.basename`, so a cloud-built ref
	// and an m6e-built ref of one project can never disagree. Best-effort: a project
	// with CI but no container-build never reads the basename, so a miss just leaves
	// Image empty and the image-naming steps emit nothing.
	if st.Image == "" {
		st.Image = r.imageBasename()
	}
	// Resolve every `${<pf-path>}` generation-time reference in the tool invocations
	// (run / positional args / set-env / string build-args) against the merged doc. A
	// miss resolves to EMPTY (D4, reversed) — a preset ref to an artifact this project
	// omits leaves the arg unset; only a malformed ref errors.
	if err := r.interpolateRefs(st); err != nil {
		return nil, fmt.Errorf("org.projectfile.ci: %w", err)
	}
	// Resolve the forgejo-release action's binary asset path from
	// org.projectfile.artifacts (the single kind=binary entry). The action cannot
	// take a `run:` (it supplies its own steps), so the path is resolved here —
	// Load-time, against the same merged doc interpolateRefs walks — and stashed on
	// the manifest for toolStep to thread as a `with:` input. Fail-closed: zero or
	// >1 kind=binary artifact is an ambiguous attach target, not a silent miss.
	if err := r.resolveReleaseAssetPaths(st); err != nil {
		return nil, fmt.Errorf("org.projectfile.ci: %w", err)
	}
	// Mint the arch axis from org.projectfile.architecture. It is DERIVED, never
	// authored, so the declared arch set and what CI actually builds cannot drift
	// apart. Done before ValidateImage so the minted key counts as a declared axis
	// for the image ref's {placeholder} check.
	arches, err := r.architectures()
	if err != nil {
		return nil, err
	}
	if len(arches) > 0 && st.buildsContainer() {
		genlog.Info("arch axis: minting from the declared architecture set",
			"axis", ArchAxis, "arches", arches, "image", st.Image)
		st.addAxis(Axis{Key: ArchAxis, Values: arches})
	}
	// Fail fast on an image ref a matrix could not satisfy (unknown axis / push
	// collision) — once the basename is finalized (explicit OR identity-derived),
	// since a derived `b19/ubuntu` collides under a matrix just as an explicit one would.
	if err := st.ValidateImage(); err != nil {
		return nil, err
	}
	return st, nil
}

// imagePlaceholder matches one {AXIS} interpolation token in an image basename
// (e.g. b19/ubuntu-{B19_UBUNTU_SERIES}). The capture is the bare axis KEY.
var imagePlaceholder = regexp.MustCompile(`\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// ValidateImage rejects an image basename a matrix could not publish without a
// collision. Both failures key off DATA already in the model — the declared
// matrix axes and the `oci-push` ACTION token — NEVER a node name or the word
// "publish" (Law 1: the framework names no workflow, it only refuses an
// unsatisfiable one). Two failures:
//
//   - unknown-axis: a {KEY} placeholder names an axis the project never declares,
//     so the ref would render a literal `{KEY}` into an image name (a typo'd template).
//   - collision: the oci-push JOB itself fans more than one cell (see imagePushFans),
//     yet the ref carries NO {AXIS} placeholder — every cell would push the SAME ref.
//     The consumer must template the ref (e.g. `b19/ubuntu-{B19_UBUNTU_SERIES}`) so
//     each cell publishes to its own image.
//
// A non-matrix project, a matrix with no push, OR a SINGLE image push that merely
// coexists with an unrelated matrix (e.g. a Go CLI whose binaries fan {GOOS,GOARCH}
// but whose container image is single) is unaffected — the bare basename stays the
// back-compatible default (a non-fanning push is one ref, no collision). An empty
// image (no container build) is vacuously valid.
func (st *Subtree) ValidateImage() error {
	if st == nil || st.Image == "" {
		return nil
	}
	// The legal {placeholder} names are the matrix variables a cell may carry —
	// axis keys AND matrix.overrides extra-var keys (an image may template on a
	// derived per-cell var, e.g. a basename embedding an LLVM series).
	axes := st.MatrixKeys()
	placeholders := imagePlaceholder.FindAllStringSubmatch(st.Image, -1)
	for _, m := range placeholders {
		if !axes[m[1]] {
			return fmt.Errorf("org.projectfile.ci.image %q references {%s}, not a declared matrix axis %v",
				st.Image, m[1], sortedKeys(axes))
		}
	}
	if len(placeholders) == 0 && st.imagePushFans() {
		return fmt.Errorf("org.projectfile.ci.image %q is a matrix build consumed by an %s but carries no {AXIS} placeholder — "+
			"every cell would push the same ref; template it per axis (e.g. %q)",
			st.Image, ActionOciPush, st.Image+"-{"+st.firstAxis()+"}")
	}
	return nil
}

// axisKeys is the SET of every declared matrix-axis KEY — the GLOBAL axes plus any
// per-node axes — i.e. the legal {placeholder} names for the image ref.
func (st *Subtree) axisKeys() map[string]bool {
	keys := make(map[string]bool)
	for _, a := range st.Axes {
		keys[a.Key] = true
	}
	for _, n := range st.Nodes {
		for _, a := range n.Axes {
			keys[a.Key] = true
		}
	}
	return keys
}

// imagePushFans reports whether the JOB that runs an oci-push fans MORE THAN ONE
// cell — the precise condition under which "every cell would push the same ref"
// is a real collision (a single push to one ref is not). A tool takes the timing
// of the node that NEEDS it, so we find each node needing an oci-push tool and ask
// whether THAT node fans: a per-node `matrix:{axes}` (its own Axes), or a bare
// `matrix:true` over a NON-EMPTY global axis set. The distinction matters for a
// single-image project that ALSO carries an unrelated per-node matrix (e.g. cli:
// build-binaries fans {GOOS,GOARCH} for release assets, but its container image is
// single and its publish node is `matrix:true` over EMPTY global axes) — that push
// is one ref, not a collision, so the coarse "any matrix anywhere" would wrongly
// reject it. Keyed only off DATA (the oci-push ACTION token + each node's matrix
// shape + its needs edges), never a node name (Law 1).
func (st *Subtree) imagePushFans() bool {
	pushTools := make(map[string]bool)
	for name, man := range st.Tools {
		if man.Action == ActionOciPush {
			pushTools[name] = true
		}
	}
	if len(pushTools) == 0 {
		return false
	}
	for _, n := range st.Nodes {
		needsPush := false
		for _, dep := range n.Needs {
			if pushTools[dep.Target] {
				needsPush = true
				break
			}
		}
		if !needsPush {
			continue
		}
		if len(n.Axes) > 0 { // per-node matrix: fans its own axes
			return true
		}
		if n.Matrix && len(st.Axes) > 0 { // bare matrix:true over the global axes
			return true
		}
	}
	return false
}

// firstAxis returns one declared axis key for the templating hint — the
// sorted-first global axis, else any per-node axis. Only reached from the collision
// branch (which requires hasMatrix), so axisKeys is never empty here.
func (st *Subtree) firstAxis() string {
	if len(st.Axes) > 0 {
		return st.Axes[0].Key // Axes are key-sorted at Parse
	}
	return sortedKeys(st.axisKeys())[0]
}

// Parse normalises one merged-subtree JSON document into a Subtree. Exposed so
// the conformance suite can drive the exact same normalisation pf-cli feeds in
// production.
func Parse(data []byte) (*Subtree, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, nil
	}
	var raw rawSubtree
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("decode org.projectfile.ci: %w", err)
	}
	if len(raw.Nodes) == 0 {
		return nil, nil // no nodes => nothing to lower
	}

	st := &Subtree{Nodes: make(map[string]Node, len(raw.Nodes)), Image: raw.Image, Env: raw.Env}

	// GLOBAL matrix axes — key-sorted for a deterministic cell order and CELL render.
	if len(bytes.TrimSpace(raw.Matrix)) > 0 {
		m, err := decodeMatrix(raw.Matrix)
		if err != nil {
			return nil, err
		}
		if len(m.Without) > 0 {
			return nil, fmt.Errorf("matrix.without is a per-node feature — " +
				"the global matrix IS the axis set, so subtracting from it here means deleting the axis")
		}
		axes, err := buildAxes(m.Axes)
		if err != nil {
			return nil, err
		}
		st.Axes = axes
		// matrix.exclude: the cells the axes mint but nothing builds. Decoded BEFORE
		// overrides because it changes WHICH cells exist — an override is then
		// validated against the cells that SURVIVE, so a row decorating only excluded
		// cells is the same loud no-match error as a typo'd axis value.
		ex, err := buildExcludes(m.Exclude, axes)
		if err != nil {
			return nil, err
		}
		st.Excludes = ex
		// matrix.overrides: extra per-cell vars keyed by an axis-value match. Decoded
		// AFTER axes (the match/var split keys off the axis set) and validated
		// against the cells the axes enumerate (a row matching no cell is refused —
		// an override decorates existing cells, never spawns new ones).
		ov, err := buildOverrides(m.Overrides, axes, ex)
		if err != nil {
			return nil, err
		}
		st.Overrides = ov
	}

	// Nodes.
	for name, rn := range raw.Nodes {
		needs, err := decodeNeeds(rn.Needs)
		if err != nil {
			return nil, fmt.Errorf("decode needs of node %q: %w", name, err)
		}
		isCell, axes, excludes, without, err := decodeNodeMatrix(rn.Matrix)
		if err != nil {
			return nil, fmt.Errorf("node %q: %w", name, err)
		}
		// max-parallel throttles matrix cells, so it is only meaningful on a CELL node
		// and must be a positive cap — reject a misplaced or nonsensical value at parse
		// rather than emit a strategy block the forge would choke on.
		if rn.MaxParallel != 0 {
			if !isCell {
				return nil, fmt.Errorf("node %q: max-parallel set on a non-matrix node (add matrix: true or drop it)", name)
			}
			if rn.MaxParallel < 0 {
				return nil, fmt.Errorf("node %q: max-parallel must be >= 1, got %d", name, rn.MaxParallel)
			}
		}
		when, err := decodeWhen(rn.When)
		if err != nil {
			return nil, fmt.Errorf("node %q: %w", name, err)
		}
		schedule, dispatch, err := decodeNodeTriggers(name, rn)
		if err != nil {
			return nil, err
		}
		// A concurrency block is inert without a group key, so reject the empty spelling at
		// parse rather than emit a group-less block the forge rejects.
		var concurrency *Concurrency
		if rn.Concurrency != nil {
			if rn.Concurrency.Group == "" {
				return nil, fmt.Errorf("node %q: concurrency.group must be non-empty", name)
			}
			concurrency = &Concurrency{Group: rn.Concurrency.Group, CancelInProgress: rn.Concurrency.CancelInProgress}
		}
		st.Nodes[name] = Node{Name: name, Goal: rn.Goal, Matrix: isCell, Axes: axes, Excludes: excludes, Without: without, MaxParallel: rn.MaxParallel, Concurrency: concurrency, Needs: needs, When: when, Schedule: schedule, Dispatch: dispatch}
		st.NodeOrder = append(st.NodeOrder, name)
	}
	sort.Strings(st.NodeOrder)

	// Goals — opt-in via the per-node `goal: true` flag. When ANY node is flagged
	// the goal set is EXACTLY those nodes; when NONE is, the resolver falls back to
	// sink inference. A goal is therefore always a declared node by construction —
	// the old top-level `goals` list (and its unknown-goal defect) is gone.
	for _, name := range st.NodeOrder {
		if st.Nodes[name].Goal {
			st.GoalsExplicit = true
			st.Goals = append(st.Goals, name)
		}
	}

	// Tool manifests (optional) — execution metadata joined in at render time.
	// Each tool's polymorphic `args` map is decoded here (like `needs`): the typed
	// BuildArg slice is re-stored on the struct (map values are copies, so write back).
	if len(raw.Tools) > 0 {
		st.Tools = raw.Tools
		for name, man := range st.Tools {
			args, err := decodeBuildArgs(man.RawArgs)
			if err != nil {
				return nil, fmt.Errorf("decode args of tool %q: %w", name, err)
			}
			// `when` is step-ordering, not an event predicate: only `always` (or empty)
			// is meaningful — a typo must fail the parse, never silently drop the guard.
			if man.When != "" && man.When != StepWhenAlways {
				return nil, fmt.Errorf("tool %q: when %q invalid (only %q)", name, man.When, StepWhenAlways)
			}
			// The emitted payload is read back from what the publish action wrote (the
			// digest and tag files). On any other tool the step would POST an image fact
			// with nothing in it, so refuse the pairing rather than emit a hollow event.
			if man.Emit != "" && man.Action != ActionOciPush {
				return nil, fmt.Errorf("tool %q: emit %q needs action %q (the payload comes from what that action published)",
					name, man.Emit, ActionOciPush)
			}
			man.Args = args
			st.Tools[name] = man
		}
	}

	// A node may only GATE on dispatch/schedule if some goal actually declares that
	// trigger, else the `on:` surface never carries it and the gated step is a dead
	// branch. Validated against the node-level trigger data lifted above.
	if err := st.validateEventGates(); err != nil {
		return nil, err
	}

	// Per-target deployment overlays (optional) — ignored by the lowering, joined
	// in only when rendering THAT target. Keyed so a Forgejo run never reads gha.
	for key, rp := range map[string]*rawPlatform{"gha": raw.Gha, "forgejo": raw.Forgejo} {
		if rp == nil {
			continue
		}
		p, err := rp.normalise()
		if err != nil {
			return nil, fmt.Errorf("decode org.projectfile.ci.%s: %w", key, err)
		}
		if st.Platforms == nil {
			st.Platforms = make(map[string]Platform, 2)
		}
		st.Platforms[key] = p
	}
	return st, nil
}

// normalise lowers a raw platform overlay to the typed model, resolving the
// scalar-or-list `runs-on` shape GHA permits.
func (rp *rawPlatform) normalise() (Platform, error) {
	runsOn, err := decodeStringOrList(rp.RunsOn)
	if err != nil {
		return Platform{}, fmt.Errorf("runs-on: %w", err)
	}
	p := Platform{
		RunsOn:         runsOn,
		TimeoutMinutes: rp.TimeoutMinutes,
		Permissions:    rp.Permissions,
		Builder:        rp.Builder,
		Credentials:    rp.Credentials,
		Actions:        rp.Actions,
		CheckoutToken:  rp.CheckoutToken,
	}
	if c := rp.Concurrency; c != nil {
		p.Concurrency = &Concurrency{Group: c.Group, CancelInProgress: c.CancelInProgress}
	}
	return p, nil
}

// decodeStringOrList accepts GHA's two `runs-on` spellings — a bare label
// ("ubuntu-latest") or a label list (["self-hosted", "linux"]) — and returns a
// slice either way, so the renderer has one shape to format.
func decodeStringOrList(raw json.RawMessage) ([]string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	if trimmed[0] == '[' {
		var list []string
		if err := json.Unmarshal(trimmed, &list); err != nil {
			return nil, err
		}
		return list, nil
	}
	var s string
	if err := json.Unmarshal(trimmed, &s); err != nil {
		return nil, err
	}
	return []string{s}, nil
}

// decodeNeeds handles both shapes:
//
//	[]string                 sequence form — each entry enabled, no args
//	map[string]value         mapping form  — value is true | false | {args:"…"}
//
// false drops the target; {} is tolerated as true (spec §4.9a override grammar).
func decodeNeeds(raw json.RawMessage) ([]Need, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	needs, err := decodeTargetList(raw)
	if err != nil {
		return nil, err
	}
	// Stable order independent of JSON map iteration: ordering is derived from
	// the graph downstream, so a deterministic sort here only aids reproducibility.
	sort.Slice(needs, func(i, j int) bool { return needs[i].Target < needs[j].Target })
	return needs, nil
}

// decodeTargetList decodes the scalar/sequence/mapping shape a `needs` value may
// take (scalar shorthand, a bare list, or the override-grammar mapping). It is the
// one place all three `needs` forms collapse to a Need slice.
func decodeTargetList(raw json.RawMessage) ([]Need, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	if trimmed[0] == '"' {
		// Scalar shorthand: `needs: <target>` is exactly the one-entry sequence
		// [<target>], no args. Spec-supported third shape; silently dropping it (as
		// both lowerings once did) severs the subtree from the goal closure.
		var s string
		if err := json.Unmarshal(trimmed, &s); err != nil {
			return nil, err
		}
		return []Need{{Target: s}}, nil
	}
	if trimmed[0] == '[' {
		var seq []string
		if err := json.Unmarshal(trimmed, &seq); err != nil {
			return nil, err
		}
		out := make([]Need, 0, len(seq))
		for _, t := range seq {
			out = append(out, Need{Target: t})
		}
		return out, nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &m); err != nil {
		return nil, err
	}
	out := make([]Need, 0, len(m))
	for _, target := range sortedKeys(m) {
		n, keep, err := decodeNeedValue(target, m[target])
		if err != nil {
			return nil, err
		}
		if keep {
			out = append(out, n)
		}
	}
	return out, nil
}

// decodeNeedValue interprets one mapping value: true (enable), false (drop), or
// {args:"…"} (enable + bind the arg string; {} == true).
func decodeNeedValue(target string, raw json.RawMessage) (Need, bool, error) {
	trimmed := bytes.TrimSpace(raw)
	switch {
	case bytes.Equal(trimmed, []byte(BoolTrue)):
		return Need{Target: target}, true, nil
	case bytes.Equal(trimmed, []byte(BoolFalse)):
		return Need{}, false, nil
	}
	var obj struct {
		Args *string `json:"args"`
	}
	if err := json.Unmarshal(trimmed, &obj); err != nil {
		return Need{}, false, fmt.Errorf("target %q: unsupported needs value %s", target, trimmed)
	}
	n := Need{Target: target}
	if obj.Args != nil {
		n.Args, n.HasArgs = *obj.Args, true
	}
	return n, true, nil
}

// decodeBuildArgs normalises the `args` map (`{<NAME>: {<source>: <ref>}}`) into a
// name-sorted BuildArg slice. Each value is a single-key object; the key is the
// source (var|get|file|ci) and its scalar the ref. The source is validated against
// the closed BuildArgSources set, and get/ci refs against their known sets, so a
// typo fails the parse rather than emitting a build-arg that clobbers a Dockerfile
// default with an empty value. An absent/empty `args` yields nil (no build-args).
func decodeBuildArgs(raw json.RawMessage) ([]BuildArg, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("args must be a map of {name: <${pf.path} string> | {source: ref}}: %w", err)
	}
	out := make([]BuildArg, 0, len(m))
	for _, name := range sortedKeys(m) {
		v := m[name]
		// STRING form: an IN-document `${pf.path}` reference. Kept raw here as
		// SourceLiteral.Ref; Load interpolates it once the merged doc is available
		// (an unresolvable ref resolves to EMPTY THERE — D4 reversed, arg left unset).
		var s string
		if err := json.Unmarshal(v, &s); err == nil {
			out = append(out, BuildArg{Name: name, Source: SourceLiteral, Ref: s})
			continue
		}
		// OBJECT form: an OUT-of-document {var|file|ci} binding.
		var src map[string]string
		if err := json.Unmarshal(v, &src); err != nil {
			return nil, fmt.Errorf("build-arg %q: value must be a ${pf.path} string or a {var|file|ci} object: %w", name, err)
		}
		if len(src) != 1 {
			return nil, fmt.Errorf("build-arg %q: want exactly one source {var|file|ci}, got %d", name, len(src))
		}
		var ba BuildArg
		for source, ref := range src { // single iteration (len == 1)
			ba = BuildArg{Name: name, Source: source, Ref: ref}
		}
		if !BuildArgSources[ba.Source] {
			return nil, fmt.Errorf("build-arg %q: unknown source %q (want one of var|file|ci, or a ${pf.path} string)", name, ba.Source)
		}
		if ba.Source == SourceCI && !BuildArgCIKeys[ba.Ref] {
			return nil, fmt.Errorf("build-arg %q: unknown ci key %q (want one of %v)", name, ba.Ref, sortedKeys(BuildArgCIKeys))
		}
		out = append(out, ba)
	}
	return out, nil
}

// decodeBuildInputs normalises org.projectfile.build.args into a name-sorted
// BuildInput slice. Each value is a bare SCALAR (string|number|bool, == {default})
// OR a single-key object {default: <scalar>} | {file: <path>} — the same wire shapes
// the make reader lowers. Declaring both default and file fails the parse (an
// ambiguous input must not silently drop one half). An absent/empty `args`
// yields nil (no auto-forwarded build-args). A non-string scalar is coerced to its
// literal string form (`16`->"16", `3.14`->"3.14", `true`->"true") so an author
// writing `B19_GCC_SERIES: 16` is not bamboozled into quoting — mirrors
// decodeAxisValues, the b19/{gcc,node,...} fix applied to build inputs.
func decodeBuildInputs(raw json.RawMessage) ([]BuildInput, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("args must be a map of {name: value|{default|file}}: %w", err)
	}
	out := make([]BuildInput, 0, len(m))
	for _, name := range sortedKeys(m) {
		bi := BuildInput{Name: name}
		// bare-scalar shorthand: `args.<NAME>: <scalar>` == {default: "<scalar>"}.
		if s, ok := decodeScalarToString(m[name]); ok {
			bi.Default = s
			out = append(out, bi)
			continue
		}
		// object form: {default: <scalar>} | {file: <path>}. Default is decoded
		// raw so a non-string scalar (16) survives the same coercion as the bare form.
		var obj struct {
			Default *json.RawMessage `json:"default"`
			File    *string          `json:"file"`
		}
		if err := json.Unmarshal(m[name], &obj); err != nil {
			return nil, fmt.Errorf("build-arg %q: want a scalar or {default|file}: %w", name, err)
		}
		if obj.Default != nil && obj.File != nil {
			return nil, fmt.Errorf("build-arg %q: default and file are mutually exclusive", name)
		}
		switch {
		case obj.File != nil:
			bi.File = *obj.File
		case obj.Default != nil:
			s, ok := decodeScalarToString(*obj.Default)
			if !ok {
				return nil, fmt.Errorf("build-arg %q: default must be a scalar (string, number, or boolean)", name)
			}
			bi.Default = s
		}
		out = append(out, bi)
	}
	return out, nil
}

// decodeScalarToString turns a bare JSON scalar into its literal string form:
// a string verbatim, a number as written (UseNumber keeps `16`->"16" and
// `3.14`->"3.14" with no float reformatting that could lie), a bool as
// "true"/"false". Returns ok=false for objects, arrays, and null — the caller
// routes those to the {default|file} branch. Shared by decodeBuildInputs (the
// bare form and the {default} field) and aligned with decodeAxisValues so a
// build-arg and a matrix axis spell scalar coercion identically.
func decodeScalarToString(raw json.RawMessage) (string, bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return "", false
	}
	switch t := v.(type) {
	case string:
		return t, true
	case json.Number:
		return t.String(), true
	case bool:
		return strconv.FormatBool(t), true
	}
	return "", false
}

// decodeWhen normalises a node's optional `when: {events: [...]}` predicate into a
// sorted, deduped token slice. A nil predicate (field absent) yields nil — the node
// runs on every event. An empty `events` list, or any token outside the closed
// WhenEvents vocabulary, fails the parse (a typo must not silently drop a gate).
func decodeWhen(rw *rawWhen) ([]string, error) {
	if rw == nil {
		return nil, nil
	}
	if len(rw.Events) == 0 {
		return nil, fmt.Errorf("when.events must list at least one event")
	}
	out := make([]string, 0, len(rw.Events))
	seen := make(map[string]bool, len(rw.Events))
	for _, e := range rw.Events {
		if !validEvent(e) {
			return nil, fmt.Errorf("when: unknown event %q (want %q or %q<branch>)", e, EventTag, EventPushPrefix)
		}
		if !seen[e] {
			seen[e] = true
			out = append(out, e)
		}
	}
	sort.Strings(out)
	return out, nil
}

// cronFields is the POSIX cron field count (minute hour dom month dow). GHA and
// Forgejo take the same 5-field form (no seconds), so a different count is a typo we
// reject rather than emit an `on: schedule` the forge would silently ignore.
const cronFields = 5

// decodeNodeTriggers normalises a node's optional `schedule:` (cron timer) and
// `dispatch:` (manual-run button) fields — the workflow-level trigger DATA lifted
// from the old global block onto the node that owns the file. Every malformed field
// (unknown input type, a choice with no options, a non-5-field cron) fails the parse
// so a typo cannot silently drop a trigger. Trigger data is only meaningful on a GOAL
// node (each goal lowers to its own file); on a non-goal it would be dead config, so
// it is rejected rather than ignored (secure-by-default).
func decodeNodeTriggers(name string, rn rawNode) ([]string, *Dispatch, error) {
	if len(rn.Schedule) == 0 && rn.Dispatch == nil {
		return nil, nil, nil
	}
	if !rn.Goal {
		return nil, nil, fmt.Errorf("node %q: schedule/dispatch are only valid on a goal node (goal: true)", name)
	}
	var schedule []string
	for i, rs := range rn.Schedule {
		cron := strings.TrimSpace(rs.Cron)
		if n := len(strings.Fields(cron)); n != cronFields {
			return nil, nil, fmt.Errorf("node %q schedule[%d]: cron %q must have %d fields, got %d", name, i, rs.Cron, cronFields, n)
		}
		schedule = append(schedule, cron)
	}
	var dispatch *Dispatch
	if rn.Dispatch != nil {
		dispatch = &Dispatch{BuildArgs: rn.Dispatch.BuildArgs}
		for _, in := range sortedKeys(rn.Dispatch.Inputs) {
			input, err := decodeInput(in, rn.Dispatch.Inputs[in])
			if err != nil {
				return nil, nil, fmt.Errorf("node %q: %w", name, err)
			}
			dispatch.Inputs = append(dispatch.Inputs, input)
		}
	}
	return schedule, dispatch, nil
}

// decodeInput normalises one manual-dispatch input, defaulting an omitted type to
// string and enforcing the choice/options pairing and per-type default validity.
func decodeInput(name string, ri rawInput) (Input, error) {
	in := Input{Name: name, Type: ri.Type, Description: ri.Description, Required: ri.Required, Options: ri.Options}
	if in.Type == "" {
		in.Type = InputString
	}
	if !InputTypes[in.Type] {
		return Input{}, fmt.Errorf("dispatch.inputs.%s: unknown type %q (want one of %v)", name, in.Type, sortedKeys(InputTypes))
	}
	if (in.Type == InputChoice) != (len(in.Options) > 0) {
		return Input{}, fmt.Errorf("dispatch.inputs.%s: options are required for type=choice and forbidden otherwise", name)
	}
	if ri.Default != nil {
		def, ok := decodeScalarToString(*ri.Default)
		if !ok {
			return Input{}, fmt.Errorf("dispatch.inputs.%s: default must be a scalar", name)
		}
		if err := validateDefault(name, in.Type, def, in.Options); err != nil {
			return Input{}, err
		}
		in.Default, in.HasDefault = def, true
	}
	return in, nil
}

// validateDefault rejects a default that cannot satisfy its input's type — a boolean
// that is not true/false, a number that is not numeric, or a choice value absent from
// its options — so the run form cannot ship an unselectable default.
func validateDefault(name, typ, def string, options []string) error {
	switch typ {
	case InputBoolean:
		if def != BoolTrue && def != BoolFalse {
			return fmt.Errorf("dispatch.inputs.%s: boolean default must be true or false, got %q", name, def)
		}
	case InputNumber:
		if _, err := strconv.ParseFloat(def, 64); err != nil {
			return fmt.Errorf("dispatch.inputs.%s: number default %q is not numeric", name, def)
		}
	case InputChoice:
		for _, o := range options {
			if o == def {
				return nil
			}
		}
		return fmt.Errorf("dispatch.inputs.%s: choice default %q is not one of %v", name, def, options)
	}
	return nil
}

// validateEventGates refuses a node `when` that gates on dispatch/schedule no goal
// node declares: without the trigger the workflow never fires that event, so the
// gated step could never run (a silent dead branch). The trigger data now lives on
// goal nodes (each owns a file), so the existence check is across goals. Secure-by-
// default — fail fast at parse rather than emit a workflow with an unreachable job.
//
// This is a coarse existence check (some goal declares it), not a per-goal-closure
// one; a goal lowering re-derives the precise surface from its OWN node's triggers.
func (st *Subtree) validateEventGates() error {
	var hasDispatch, hasSchedule bool
	for _, name := range st.NodeOrder {
		n := st.Nodes[name]
		hasDispatch = hasDispatch || n.Dispatch != nil
		hasSchedule = hasSchedule || len(n.Schedule) > 0
	}
	for _, name := range st.NodeOrder {
		for _, e := range st.Nodes[name].When {
			switch {
			case e == EventDispatch && !hasDispatch:
				return fmt.Errorf("node %q gates on %q but no goal declares dispatch", name, EventDispatch)
			case e == EventSchedule && !hasSchedule:
				return fmt.Errorf("node %q gates on %q but no goal declares schedule", name, EventSchedule)
			}
		}
	}
	return nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// decodeAxisValues turns a matrix axis's JSON array into string values, accepting
// bare scalars (`24`, `true`) as well as quoted strings. UseNumber keeps a numeric
// token's exact literal (`24`→"24", `3.14`→"3.14") so no float formatting can lie.
func decodeAxisValues(raw json.RawMessage) ([]string, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var items []any
	if err := dec.Decode(&items); err != nil {
		return nil, err
	}
	vals := make([]string, len(items))
	for i, v := range items {
		switch t := v.(type) {
		case string:
			vals[i] = t
		case json.Number:
			vals[i] = t.String()
		case bool:
			vals[i] = strconv.FormatBool(t)
		default:
			return nil, fmt.Errorf("axis value %d has unsupported type %T", i, v)
		}
	}
	return vals, nil
}

// buildAxes turns an axes map (`{KEY: [values]}`) into key-sorted Axis values.
// Shared by the GLOBAL subtree matrix and a PER-NODE matrix so both decode an
// axis identically (sorted keys => a deterministic cell order and stable render).
func buildAxes(axesRaw map[string]json.RawMessage) ([]Axis, error) {
	var axes []Axis
	for _, k := range sortedKeys(axesRaw) {
		vals, err := decodeAxisValues(axesRaw[k])
		if err != nil {
			return nil, fmt.Errorf("decode matrix axis %q: %w", k, err)
		}
		axes = append(axes, Axis{Key: k, Values: vals})
	}
	return axes, nil
}

// buildOverrides decodes matrix.overrides into key-sorted OverrideEntry rows. Each
// row's fields split into MATCH (the key is a declared GLOBAL axis) and VARS (the
// rest); a row with no MATCH fields matches every cell (its vars apply cell-wide).
// Every row MUST match at least one cell of the axes product — an override only
// DECORATES cells the axes enumerate, it never spawns new ones (the GHA "add cells"
// behaviour is refused, secure-by-default). A typo'd axis value (no cell carries it)
// is caught here as a no-match row rather than silently dropping the vars. Field
// values are coerced from bare scalars (`21`, `true`) exactly as an axis value is,
// so an author writes `B19_LLVM_SERIES: 21` unquoted.
func buildOverrides(raws []map[string]json.RawMessage, axes []Axis, excludes []Exclusion) ([]OverrideEntry, error) {
	if len(raws) == 0 {
		return nil, nil
	}
	axisSet := make(map[string]bool, len(axes))
	for _, a := range axes {
		axisSet[a.Key] = true
	}
	entries := make([]OverrideEntry, 0, len(raws))
	for i, row := range raws {
		var match, vars []KV
		for _, k := range sortedKeys(row) {
			val, ok := decodeScalarToString(row[k])
			if !ok {
				return nil, fmt.Errorf("matrix.overrides[%d]: field %q must be a scalar (string, number, or boolean)", i, k)
			}
			if axisSet[k] {
				match = append(match, KV{Key: k, Value: val})
			} else {
				vars = append(vars, KV{Key: k, Value: val})
			}
		}
		entries = append(entries, OverrideEntry{Match: match, Vars: vars})
	}
	// Validate every row matches ≥1 SURVIVING cell (the product minus matrix.exclude
	// — decorating a cell nothing builds is as dead as decorating one the axes never
	// minted). No axes => the one empty cell; a row then matches iff its MATCH is
	// empty (a non-empty match names an axis that does not exist, which buildOverrides
	// above routed to VARS — so such a row matches all).
	cells := Cells(axes, excludes)
	if len(cells) == 0 {
		cells = []map[string]string{{}}
	}
	for i, e := range entries {
		matched := false
		for _, c := range cells {
			if rowMatches(e.Match, c) {
				matched = true
				break
			}
		}
		if !matched {
			return nil, fmt.Errorf("matrix.overrides[%d]: match %s matches no cell of the axes product — "+
				"an override only decorates cells the axes enumerate, never adds new ones", i, kvsString(e.Match))
		}
	}
	return entries, nil
}

// buildExcludes decodes matrix.exclude into Exclusion rows (key-sorted, like every
// other decoded list, for byte-stable output). Each field binds ONE axis value; a
// cell carrying every pair of a row is dropped from the product. Three defects are
// refused here, all of them a typo that would otherwise exclude nothing (or
// everything) in silence:
//   - a field naming something that is not a declared axis of THIS matrix — unlike
//     an override, where a non-axis field is an extra var, an exclusion has no such
//     half, so the key can only be a mistake;
//   - a row matching no cell (a value no axis carries);
//   - rows that together empty the product — a matrix with no cell realises no work,
//     which is never what an author meant to write.
//
// Values are coerced from bare scalars exactly as an axis value is, so `GOARCH: 386`
// and `GOARCH: "386"` mean the same cell.
func buildExcludes(raws []map[string]json.RawMessage, axes []Axis) ([]Exclusion, error) {
	if len(raws) == 0 {
		return nil, nil
	}
	axisSet := make(map[string]bool, len(axes))
	for _, a := range axes {
		axisSet[a.Key] = true
	}
	rows := make([]Exclusion, 0, len(raws))
	for i, row := range raws {
		var match Exclusion
		for _, k := range sortedKeys(row) {
			if !axisSet[k] {
				return nil, fmt.Errorf("matrix.exclude[%d]: field %q is not a declared matrix axis %v — "+
					"an exclusion names axis keys only (it removes cells; it never binds a variable)", i, k, axisKeyNames(axes))
			}
			val, ok := decodeScalarToString(row[k])
			if !ok {
				return nil, fmt.Errorf("matrix.exclude[%d]: field %q must be a scalar (string, number, or boolean) — "+
					"one value per axis; excluding several values takes several entries", i, k)
			}
			match = append(match, KV{Key: k, Value: val})
		}
		rows = append(rows, match)
	}
	// Every row must hit the grid, and the grid must survive.
	full := productCells(axes)
	for i, e := range rows {
		matched := false
		for _, c := range full {
			if rowMatches(e, c) {
				matched = true
				break
			}
		}
		if !matched {
			return nil, fmt.Errorf("matrix.exclude[%d]: %s matches no cell of the axes product — "+
				"an exclusion removes cells the axes enumerate, so a value no axis carries excludes nothing", i, kvsString(e))
		}
	}
	if len(Cells(axes, rows)) == 0 {
		return nil, fmt.Errorf("matrix.exclude removes every cell of the axes product (%d cells) — "+
			"a matrix with no cell runs nothing", len(full))
	}
	return rows, nil
}

// axisKeyNames lists the declared axis KEYS, for an error message that names what
// the author could have meant.
func axisKeyNames(axes []Axis) []string {
	keys := make([]string, len(axes))
	for i, a := range axes {
		keys[i] = a.Key
	}
	return keys
}

// Cells is the realised cell set: the axes product MINUS every cell an exclusion
// row matches. It is the ONE definition of "which cells exist" — override
// validation, the partial-var scan, and the resolver's per-cell multiplicity all
// read it, so the grid cannot mean one thing at parse and another at lowering.
// Empty axes => nil (no cells), exactly as productCells.
func Cells(axes []Axis, excludes []Exclusion) []map[string]string {
	cells := productCells(axes)
	if len(excludes) == 0 {
		return cells
	}
	kept := make([]map[string]string, 0, len(cells))
	for _, c := range cells {
		if !excluded(c, excludes) {
			kept = append(kept, c)
		}
	}
	return kept
}

// excluded reports whether ANY exclusion row matches the cell (rows are unordered
// and purely subtractive — one hit is enough).
func excluded(cell map[string]string, excludes []Exclusion) bool {
	for _, e := range excludes {
		if rowMatches(e, cell) {
			return true
		}
	}
	return false
}

// productCells returns the cartesian product of axes as a list of axis-key→value
// maps (each map is one cell). Empty axes => nil (no cells); the caller treats
// that as the single empty cell for include matching.
func productCells(axes []Axis) []map[string]string {
	if len(axes) == 0 {
		return nil
	}
	cells := []map[string]string{{}}
	for _, a := range axes {
		next := make([]map[string]string, 0, len(cells)*len(a.Values))
		for _, c := range cells {
			for _, v := range a.Values {
				cc := make(map[string]string, len(c)+1)
				for k, val := range c {
					cc[k] = val
				}
				cc[a.Key] = v
				next = append(next, cc)
			}
		}
		cells = next
	}
	return cells
}

// rowMatches reports whether every MATCH key→value is present with the same value
// in the cell (the cell's axis bindings are a superset of the match). An empty
// match matches every cell.
func rowMatches(match []KV, cell map[string]string) bool {
	for _, kv := range match {
		if cell[kv.Key] != kv.Value {
			return false
		}
	}
	return true
}

func kvsString(kvs []KV) string {
	parts := make([]string, len(kvs))
	for i, kv := range kvs {
		parts[i] = kv.Key + "=" + kv.Value
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

// ExtraVarKeys is the set of all extra-var NAMES declared across matrix.overrides
// rows — the keys that become matrix variables of matching cells (siblings of the
// axis keys for {placeholder} substitution and build-arg forwarding). Exposed so
// the render treats an extra var exactly like an axis where it appears.
func (st *Subtree) ExtraVarKeys() map[string]bool {
	keys := make(map[string]bool)
	for _, e := range st.Overrides {
		for _, kv := range e.Vars {
			keys[kv.Key] = true
		}
	}
	return keys
}

// PartialExtraVars is the subset of ExtraVarKeys that at least one cell of the axes
// product leaves UNSET — a partial override. Such a var's ${{ matrix.<NAME> }} ref
// resolves empty on an un-decorated cell, so the render backstops it with the
// build-arg default there (matrixVarExpr). A var bound on EVERY cell (fully
// enumerated, e.g. zig's per-cell LLVM) is absent: its ref always resolves, so it
// keeps the bare matrix spelling and existing renders stay byte-identical.
func (st *Subtree) PartialExtraVars() map[string]bool {
	extras := st.ExtraVarKeys()
	if len(extras) == 0 {
		return nil
	}
	cells := Cells(st.Axes, st.Excludes)
	if len(cells) == 0 {
		cells = []map[string]string{{}}
	}
	partial := make(map[string]bool)
	for k := range extras {
		for _, c := range cells {
			if !st.cellBindsVar(k, c) {
				partial[k] = true
				break
			}
		}
	}
	return partial
}

// cellBindsVar reports whether some override row matching cell c sets var k.
func (st *Subtree) cellBindsVar(k string, c map[string]string) bool {
	for _, e := range st.Overrides {
		if !rowMatches(e.Match, c) {
			continue
		}
		for _, v := range e.Vars {
			if v.Key == k {
				return true
			}
		}
	}
	return false
}

// MatrixKeys is the set of every matrix-variable name a cell may carry — the
// global + per-node axis keys PLUS the matrix.overrides extra-var names. It is the
// legal set of {placeholder} names for an image basename, and the set of names a
// build-arg lowering treats as matrix-passed (cell-defined) rather than a vars-store
// lookup. Kept as one method so ValidateImage and the render lowering agree.
func (st *Subtree) MatrixKeys() map[string]bool {
	keys := st.axisKeys()
	for k := range st.ExtraVarKeys() {
		keys[k] = true
	}
	return keys
}

// decodeMatrix decodes a matrix OBJECT — the shape shared by the GLOBAL matrix and
// a per-node one — STRICTLY: an unknown key is a parse error, never a block that
// vanishes. Silence here is expensive out of all proportion to the typo, because
// the symptom is a projectfile that looks changed and workflows that regenerate
// byte for byte identical, with no line of output to suspect.
func decodeMatrix(raw json.RawMessage) (*rawMatrix, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var m rawMatrix
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("matrix: want {axes: {...}} with optional overrides/exclude/without: %w", err)
	}
	return &m, nil
}

// decodeNodeMatrix interprets a node's `matrix` field, which is polymorphic:
//
//   - a BOOL: `true` makes the node a CELL over the GLOBAL subtree axes (the common
//     case — every matrix node shares one fan-out, e.g. b19's series); `false`/absent
//     => not a cell;
//
//   - an OBJECT `{axes: {...}}`: the node is a CELL over its OWN axes, isolated from
//     the global set, so two artifact classes can fan over DIFFERENT dimensions in
//     one pipeline (binaries over {GOOS,GOARCH}, the image over arch). An explicit
//     axes object always implies the node is a cell. It takes the same `exclude`
//     list as the global matrix, subtracting cells its own axes cannot build.
//
//   - an OBJECT `{without: [KEY]}`: the node is a CELL over the GLOBAL axes MINUS the
//     named ones. The subtractive form is what a DERIVED axis needs — the manifest
//     assembly node indexes the per-arch pushes, so it must fan over every other axis
//     and NOT over arch, without ever naming the arch values it is avoiding.
//
// `overrides` is refused here: extra per-cell VARS are a global-matrix feature (the
// render emits them as strategy.matrix.include only for a job whose axes ARE the
// global ones), so a node-level block would decorate nothing. `axes` + `without`
// together is refused as contradictory.
//
// Empty input => (false, nil, nil, nil): not a matrix node.
func decodeNodeMatrix(raw json.RawMessage) (isCell bool, axes []Axis, excludes []Exclusion, without []string, err error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return false, nil, nil, nil, nil
	}
	var b bool
	if json.Unmarshal(raw, &b) == nil {
		return b, nil, nil, nil, nil // bare bool => global-axes cell (true) or not a cell (false)
	}
	m, err := decodeMatrix(raw)
	if err != nil {
		return false, nil, nil, nil, fmt.Errorf("matrix: want a bool or {axes: {...}} / {without: [...]}: %w", err)
	}
	if len(m.Overrides) > 0 {
		return false, nil, nil, nil, fmt.Errorf("matrix.overrides is a global-matrix feature — " +
			"move the rows to the top-level matrix (a per-node matrix has no cell of the global product to decorate)")
	}
	if len(m.Axes) > 0 && len(m.Without) > 0 {
		return false, nil, nil, nil, fmt.Errorf("matrix.axes and matrix.without are exclusive — " +
			"own axes are already the complete set this node fans over, so there is nothing to subtract")
	}
	if axes, err = buildAxes(m.Axes); err != nil {
		return false, nil, nil, nil, err
	}
	if excludes, err = buildExcludes(m.Exclude, axes); err != nil {
		return false, nil, nil, nil, err
	}
	return true, axes, excludes, m.Without, nil
}
