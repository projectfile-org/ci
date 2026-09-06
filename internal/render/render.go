// SPDX-FileCopyrightText: 2026 Damián Búho <damian.buho@proton.me>
//
// SPDX-License-Identifier: MIT

// Package render turns a resolved job model into a vendor CI workflow.
//
// The graph lowering (internal/resolve) is target-neutral — that is the whole
// point of the neutral name "pf-ci". A target is then just a TEMPLATE plus
// a thin ADAPTER: GitHub Actions and Forgejo Actions share one workflow template
// (Forgejo Actions is GHA-compatible) and differ only in a handful of tokens
// (runner label, checkout ref, workflow path). Tekton, a genuinely different
// engine, will bring its own template over the SAME model.
//
// Rendering joins two inputs:
//   - the resolved Model (names, needs, matrix class) — the fixture-pinned logic;
//   - the tool manifests from the subtree (image / run / guard) — execution
//     metadata that the lowering deliberately ignores.
package render

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"text/template"

	"kiota.ch/projectfile/core/v2/pkg/genlog"
	"projectfile.org/projectfile/ci/internal/ci"
	"projectfile.org/projectfile/ci/internal/resolve"
)

//go:embed templates/*.tmpl
var templatesFS embed.FS

// Target is the thin per-vendor adapter — every token GHA and Forgejo differ on.
// Adding a same-family vendor is one row; a different engine (Tekton) gets its
// own template, selected by Template.
type Target struct {
	Key      string // CLI selector ("gha" | "forgejo")
	RunsOn   string // default runner label
	Checkout string // checkout action reference
	Download string // download-artifact action reference (build→scan tar hand-off)
	Upload   string // upload-artifact action reference (build-artifact producer → consumer)
	// UploadOverwrite emits `overwrite: true` on the upload. A re-run shares the run_id,
	// so the run_id-scoped name (artifactScopeSuffix) re-uploads UNDER THE SAME name; the
	// v4+ backend (gha) hard-refuses a duplicate without this. Forgejo's v1 store is
	// last-write-wins and has no such input, so it stays false.
	UploadOverwrite bool
	// OutDir is the vendor-fixed workflows directory (`.github/workflows`, `.forgejo/
	// workflows`); each goal renders to `<OutDir>/<goal><Ext>`. The forge only reads its
	// own dir, so the per-goal split is N files there, not a free-form path. OutPath is
	// the single-file fallback for a non-workflow target (lefthook, repo-root file).
	OutDir   string // workflows directory for the per-goal split (empty => single-file target)
	Ext      string // per-goal file extension (workflowExt(), ".yaml" by default); paired with OutDir
	OutPath  string // single committed file path (lefthook); the per-goal targets use OutDir/Ext
	Template string // template file under templates/
	// ActionLib + ActionVer are the external action-library ref: an `action:`
	// tool lowers to `uses: <ActionLib>/<name>@<ActionVer>`, NOT an inline recipe
	// compiled into this resolver. The token is per-target because `uses:` is NOT
	// forge-portable — a GHA runner resolves it against github.com, a Forgejo runner
	// against its OWN instance — and a mirror rarely lands under the SAME OWNER on
	// both (a forge owner is free; a github.com org name may already be taken). The
	// adapter default names the canonical Forgejo coordinate; a deployment whose
	// mirror sits elsewhere overrides it via org.projectfile.ci.<target>.library.
	ActionLib string
	ActionVer string
	// Builder is the default container-build BACKEND for this target. Two backends
	// exist as drop-in analogues that emit the SAME OCI-tar hand-off: `buildx`
	// (daemon, GitHub-runner native) and `buildah` (daemonless, the Tekton path).
	// The backend is a HOW-it-runs detail, so it lives here (per-target default) and
	// is overridable per project via org.projectfile.ci.<target>.builder — never in
	// the neutral DAG. The container-build action lowers to the backend-specific
	// library entry `container-build/<Builder>`.
	Builder string
	// CacheRestore + CacheSave + CacheDir are the halves of the `caches` lowering (Phase 8 /
	// Law 3): a tool's named cache (a scanner DB) mounts from a host dir, but HOW that
	// dir is provisioned forks by runner kind. On an EPHEMERAL runner (GitHub) CacheRestore
	// names the action that fills it (actions/cache/restore — restore-only, the scan READS)
	// and CacheSave names the action that persists a WRITER's refresh back (actions/cache/save,
	// emitted only for `rw` mounts — the `*-db-update` writers own the roll); on a SELF-HOSTED
	// runner (Forgejo) both are EMPTY and the dir is a persistent path the refresh pipeline
	// populates and the run-tool step binds at the mount's `mode` (a scan RO, a `*-db-update`
	// writer RW). CacheDir is that host base, joined with the cache NAME. The model never
	// names actions/cache — this adapter does.
	CacheRestore string
	CacheSave    string
	CacheDir     string
	// CheckoutToken is the ready-to-emit `token:` EXPRESSION for the checkout step, or
	// empty for the run-scoped default. Composed in Go (checkoutTokenExpr), not in the
	// template, because every `${{ }}` collides with `{{ }}` — the same rule EmitView.Run
	// follows. There is deliberately NO adapter default: a robot account is a deployment
	// fact of ONE forge instance, never a vendor constant, so the value arrives only via
	// org.projectfile.ci.<target>.checkout-token.
	CheckoutToken string
}

// Container-build backend identifiers (the org.projectfile.ci.<target>.builder
// values). Named so the per-target default rows and the recognised-set map share
// one literal each.
const (
	BackendBuildx  = "buildx"
	BackendBuildah = "buildah"
)

// Target key constants — the CLI selector values that index the Targets map.
// Single-homed in `ci`, which reads the same keys off the document (build-target,
// platforms, publish routes): two spellings of one lowering would let the loader and
// the renderer disagree about which target a project declared.
const (
	TargetGHA     = ci.LoweringGHA
	TargetForgejo = ci.LoweringForgejo
)

// Action slot names — the keys in org.projectfile.ci.<target>.actions.
const (
	SlotCheckout         = "checkout"
	SlotDownloadArtifact = "download-artifact"
	SlotUploadArtifact   = "upload-artifact"
)

// Cache lowering literals (Phase 8). The three share the `ci-cache` segment on
// purpose — the host dirs and the cache-key stem name the SAME logical store so the
// scan side (restore) and the scheduled refresh side (save) agree.
//
// The segment is deliberately concept-NEUTRAL: a named cache is any tool-owned
// store keyed by name (a scanner DB, a language package cache), and the first
// consumers happening to be scanner DBs is not a reason to bake that into the
// path every other consumer inherits.
const (
	// CacheActionRestore is GitHub's restore-ONLY cache sub-action (the ephemeral-runner
	// half of the lowering). Restore-only — a scan READS the DB; the refresh pipeline
	// owns the save (the cache-key roll). Pinned major, Node-24-native.
	CacheActionRestore = "actions/cache/restore@v4"
	// CacheActionSave is GitHub's save-ONLY cache sub-action — the other ephemeral-runner
	// half. Emitted after a `*-db-update` writer step (rw mount) so the refreshed DB is
	// persisted under a UNIQUE rolled key the scan's restore-keys prefix later picks up;
	// without it an ephemeral refresh is discarded at job end. Pinned major, Node-24-native.
	CacheActionSave = "actions/cache/save@v4"
	// CacheDirForge is the persistent runner dir a self-hosted runner binds a named cache
	// from. Populated out-of-band by the refresh pipeline (a scanner DB) or accumulated
	// in place across runs (a package cache), and provisioned in the runner compose
	// (Phase 8.6). An absolute host path (the run-tool `--volume` host side).
	CacheDirForge = "/app/data/ci-cache"
	// CacheDirGHA is the restore target on an ephemeral GitHub runner: actions/cache/restore
	// fills it, then run-tool mounts it. Under runner.temp (outside the checkout, per-job).
	CacheDirGHA = "${{ runner.temp }}/ci-cache"
	// CacheKeyPrefix is the shared stem of a named cache's actions/cache key. The scan side
	// RESTORES `<prefix>-<name>-` (prefix-match the newest entry); the scheduled refresh
	// SAVES `<prefix>-<name>-<roll>`. One literal so both sides agree.
	CacheKeyPrefix = "ci-cache"
)

// ActionContainerBuild is the action path whose leaf emits the cell-keyed OCI
// archive (`<Stem>.tar`). A PORTABLE tool that `needs` such a leaf is an image-scan
// consumer: the build→scan hand-off is DERIVED from that edge (no manifest field),
// so the render pulls the build's artifact and points the entrypoint at it.
const ActionContainerBuild = ci.ActionContainerBuild

// ActionOciPush is the action path whose leaf CONSUMES a cell-keyed OCI archive
// and pushes it to a registry. Like container-build it lowers to an action-library ref
// (never `make` in the cloud), but it is a tar CONSUMER, not a producer: the
// build→push hand-off rides the SAME derived `needs` edge to container-build the
// image scans use (download-artifact + the cell-keyed `<Stem>.tar`), so there is no
// manifest field for it. Registry login REUSES the credentials overlay — the leaf
// declares `env: [REGISTRY_USERNAME, REGISTRY_PASSWORD]` and the per-target
// credentials map binds those NAMES to secret refs (Task-1 surface, no new design).
const ActionOciPush = ci.ActionOciPush

// ActionContainerExec is the action path that execs a tool's `run` command INSIDE
// an already-running compose container — the fused live job's test step. It does
// NOT start a container (run-tool's `docker run`) nor consume a tar: it `docker
// exec`s into the live stack dc-up-d brought up, named by the ContainerInstanceEnv
// contract var the build→live edge derives onto the job env. The action owns the
// imperative exec + the on-failure post-mortem (the live container is NOT --rm, so
// it is still inspectable when the exec fails), keeping the rendered step a clean
// `uses:` with no inline `docker exec` shell (Law 2).
const ActionContainerExec = ci.ActionContainerExec

// ActionSecretsProvision is the SYNTHETIC pre-dc-up-d step path — the cloud half of
// org.projectfile.ci.secrets. Unlike container-build/oci-push/container-exec it is NOT
// a tool the DAG carries: `secrets-provision` is declared gha:false/forgejo:false
// (m6e-only), so ForTarget strips it and the resolver never sees it as a tool leaf.
// Build injects a SYNTHETIC StepView with this Action before dc-up-d when the subtree
// is non-empty (the empty-subtree no-op invariant); the stepfrag dispatcher then routes
// it to providers/secrets-provision like any action step. The action serializes the
// declarations to JSON into `declarations:` and provision.sh dispatches each entry.
const ActionSecretsProvision = ci.ActionSecretsProvision

// ActionForgejoRelease is the action path that CONSUMES the cell-keyed binary
// artifact (restored to the workspace by the build→consumer download edge, the
// same hand-off the image scans use) and attaches it to a Forgejo release. Like
// oci-push it is a tar/binary CONSUMER, never a producer. The release is tagged
// with the git tag (ci:version → ${{ github.ref_name }}); the binary path is
// resolved from org.projectfile.artifacts (the single kind=binary entry). The
// action is create-then-attach: the first matrix cell creates the release, cells
// 2..N attach via `tea release attachment create` (HTTP 409 fallback), so a
// matrix fan-out (GOOS×GOARCH) converges on one release. Auth REUSES the
// credentials overlay — the leaf declares `env: [FORGEJO_TOKEN]`.
const ActionForgejoRelease = ci.ActionForgejoRelease

// ImageTagVar is the forge-plane VARIABLE name supplying the tag for every
// workspace-relative tool/project image — `${{ vars.BASE_IMAGE_DEFAULT_VERSION ||
// 'latest' }}`, ONE var a dev flips to `dev` locally (act / the vars store) while prod
// stays zero-config on `latest`. The make plane's tag var is M6E_BASE_IMAGE_DEFAULT_VERSION
// (m6e executor.mk); the make→forge boundary MAPS the two (imageTagMakeVar), so a
// projectfile value `${M6E_BASE_IMAGE_DEFAULT_VERSION}` lowers to the forge var here.
// The M6E_ prefix is dropped on the forge surface (an internal m6e mark that confuses
// forge users/agents); the make plane keeps it. A `:tag`-bearing external ref is exempt
// (composed verbatim).
const ImageTagVar = "BASE_IMAGE_DEFAULT_VERSION"

// imageTagMakeVar is the make-plane spelling of the tag var — the name that appears in
// projectfile build-arg VALUES (`${M6E_BASE_IMAGE_DEFAULT_VERSION}`), which lowerMakeExpr
// recognises and maps to the forge ImageTagVar. Kept M6E_-prefixed because the make
// plane (m6e) owns that name; only the forge surface is unprefixed.
const imageTagMakeVar = "M6E_BASE_IMAGE_DEFAULT_VERSION"

// SourceRegistryVar is the ONE pull-side redirect: set it and every sink-composed
// image reference pulls from it instead of the sink's declared head — an instance
// mirroring the whole fleet to one registry with a single variable. Same-LAYOUT
// only (nested↔nested, flat↔flat): the path grammar is sink data the render bakes,
// so a registry needing the other layout is a metadata edit + regenerate, not a
// variable. Optional everywhere: unset falls to the head the sink declares, which
// is correct by construction (the route names where the fleet publishes). The
// per-image vars.<NAME> override (see sinkImageExpr) outranks it for exceptions.
const SourceRegistryVar = "SOURCE_DOCKER_REGISTRY"

// TagEnvVar (M6E_TAG) is the workflow-level env name the tag expression is hoisted to:
// the full `${{ vars.BASE_IMAGE_DEFAULT_VERSION || 'latest' }}` form is defined ONCE in
// the workflow `env:` block and referenced as `${{ env.M6E_TAG }}` at every image/version
// site (run-tool `version:`, build-args, pf-cli-image, …). The workflow engine evaluates
// `${{ env.* }}` at expression-eval time for `with:` inputs, so this indirection is safe
// even where the composite-action env-dropping problem forbids process env — but NOT
// inside an `env:` block itself, where the context does not yet exist (inlineHoists
// expands it back there). The hoist is RENDER-LAYER only: imageTagExpr emits the env
// indirection, and the workflow template (ResolverEnv) emits the one definition; the
// neutral JSON Model stays clean (a future Tekton lowering brings its own spelling
// over the same names).
const TagEnvVar = "M6E_TAG"

// imageTagExpr renders the tag expression as the workflow env indirection (`${{ env.M6E_TAG }}`).
// The definition lives ONCE in the workflow `env:` block (ResolverEnv), so every call
// site — BASE/TOOL image refs (b19/gcc, d9t/misc-tools, …), the self-image tag, the
// build-arg tag value — reads the same single binding instead of repeating the full
// `${{ vars.X || 'literal' }}` form. Target-neutral — GHA and Forgejo share the spelling.
func imageTagExpr() string { return "${{ env." + TagEnvVar + " }}" }

// selfImageTagExpr renders the tag for the project's OWN built image (the artifact
// container-build stamps and docker-load recovers). It SUFFIXES imageTagExpr with the
// run scope so each pipeline run gets a UNIQUE tag. Why: a freshly docker-loaded
// artifact under the shared mutable tag (`latest`/`dev`) can be SHADOWED by a stale
// pre-existing image of the same name in the runner's docker store (e.g.
// `docker.io/<ns>/<name>:latest` wins resolution over the just-loaded
// `<ns>/<name>:latest`), so the test runs against the OLD image and the new one never
// publishes. A run-unique tag cannot collide with any prior image by construction. The
// dev/latest flip is preserved (local `dev` → `dev-<run>`; prod `latest` →
// `latest-<run>`), and stamp + load + compose + run all flow through composeImage so
// they stay mutually consistent. The tag is single-use: a trailing `docker rmi` reaps
// it at the end of the run (else the store would accumulate one image per run forever
// — the mutable tag self-recycled, the unique one does not).
//
// The scope is artifactScopeSuffix (run_id), NOT runScopeSuffix (run_id + run_attempt),
// and the two MUST stay identical: this tag is stamped INTO the run_id-scoped artifact
// by container-build and recomputed by every consumer that docker-loads it. An attempt
// number in the tag desynchronises them the moment one attempt consumes an earlier
// attempt's tar — a partial re-run never re-executes a succeeded producer, so the tar
// keeps the OLD attempt's tag while the re-running consumer asks for the NEW one. The
// consumer then looks up a tag no tar ever carried and compose falls through to a
// registry pull of an image that exists in no registry. run_id alone already delivers
// the run-uniqueness the suffix exists for; attempts of one run are strictly sequential
// (a run must finish before it can be re-run), so they cannot collide in the store.
// It takes the cell's ARCH for the same reason it takes the run: the tag has to be
// unique among everything sharing a docker store, and a host-mode runner shares one
// across concurrent jobs. Every other cell dimension is already in the image NAME (the
// basename carries its {AXIS} placeholders), but the arch axis is derived and reaches
// no basename — so without this, one series' arch cells all load different images under
// one ref and the last `docker load` wins. Empty arch (no axis, or a node that pinned
// it away) => today's tag exactly.
func selfImageTagExpr(arch string) string {
	return imageTagExpr() + artifactScopeSuffix + archSuffix(arch)
}

// archSuffix is the one spelling of "…and this cell's architecture", shared by the
// self-image tag and the compose stack identity so the two can never disagree about
// what makes a cell distinct.
func archSuffix(arch string) string {
	if arch == "" {
		return ""
	}
	return "-" + arch
}

// PfCliImageVar is the ci.images var NAME carrying the projectfile/cli ref (registry +
// path). container-build lowers it to the action's `pf-cli-image` input so oci-labels.sh
// reads the projectfile through that image when the runner has no host pf-cli — the same
// var m6e's make plane wires into PF_CLI_IMAGE.
const PfCliImageVar = "PF_CLI_IMAGE"

// pfCliImageRef renders the container-build `pf-cli-image` input: the projectfile/cli
// registry path (imageExpr, from the ci.images PF_CLI_IMAGE var) plus the shared tag
// expr, composed exactly as run-tool composes image:version. Empty when the build
// declares no PF_CLI_IMAGE var, so the input is omitted and the action degrades to host
// pf-cli or a logged label skip — never a bare, unqualified ${{ vars.PF_CLI_IMAGE }}.
func pfCliImageRef(b *ci.Build) string {
	ref, _ := declaredImageRef(PfCliImageVar, b)
	return ref
}

// declaredImageRef renders a declared image as a full path:tag ref; ok=false when name matches nothing declared.
func declaredImageRef(name string, b *ci.Build) (ref string, ok bool) {
	if b == nil || b.Images[name] == "" { // unset var: caller keeps its own fallback
		return "", false
	}
	if head, sank := b.ImageHeads[name]; sank { // sink-composed: full ref, SOURCE_DOCKER_REGISTRY fallback tier included
		return sinkImageExpr(name, head, b.Images[name], nil, nil, nil) + tagFallback(b.Images[name]), true
	}
	return imageExpr(name, b) + ":" + imageTagExpr(), true // plain lowered path + shared tag expr
}

// tagFallback appends the flip-var tag only when the composed value carries none,
// so a sink template that builds the tag itself is not doubled.
func tagFallback(val string) string {
	if imageTag(val) == "" {
		return ":" + imageTagExpr()
	}
	return ""
}

// MiscToolsImageVar is the ci.images var NAME carrying the misc-tools ref — where the
// `docker:` shorthand recipes and a no-`image:` `docker:` declaration run. The image
// hosts openssl, htpasswd, and the m6e-secret-* recipes (Phase 2). Threaded to the
// secrets-provision action's `default-image:` input the SAME way pfCliImageRef threads
// the projectfile/cli image to container-build: registry path + tag, fully-formed as
// provision.sh expects. The make plane exports D9T_MISC_TOOLS_IMAGE already expanded;
// the forge plane reads the same var NAME here.
const MiscToolsImageVar = "D9T_MISC_TOOLS_IMAGE"

// miscToolsImageRef renders the secrets-provision `default-image:` input: the misc-tools
// image ref (registry path + tag) where `docker:` shorthand recipes run. Mirrors
// pfCliImageRef exactly — the same imageExpr registry path + imageTagExpr tag
// composition — because D9T_MISC_TOOLS_IMAGE is a ci.images var of the SAME shape as
// PF_CLI_IMAGE. Empty when the build declares no such var (provision.sh then fails
// closed on a `docker:` declaration with no default-image, matching the m6e recipe).
func miscToolsImageRef(b *ci.Build) string {
	ref, _ := declaredImageRef(MiscToolsImageVar, b)
	return ref
}

// OutputRegistryVar is the SINGLE forge variable supplying the PUSH (output) registry
// for oci-push — DISTINCT from SourceRegistryVar, the pull-side redirect. OUTPUT_REGISTRY
// answers "where does THIS project publish TO", and is forge-DEPENDENT: each forge's vars
// store sets it (a kiota.ch pipeline pushes to kiota.ch; a GitHub mirror to ghcr.io; a
// Codeberg pipeline to Docker Hub). The value is the full push prefix (host + optional
// owner/path), appended with the project basename; empty => Docker Hub. Org-level var =
// default, repo var = project-wise override (forge var precedence), so one name covers
// both. The resolver stays forge-agnostic — the per-forge value lives in the forge vars
// store, never in the manifest or the resolver.
const OutputRegistryVar = "OUTPUT_REGISTRY"

// outputRegistryExpr renders the oci-push push-target expression: the single
// OUTPUT_REGISTRY var, empty => Docker Hub (push.sh treats an empty REGISTRY as Docker
// Hub). See OutputRegistryVar for the input/output registry distinction.
func outputRegistryExpr() string { return "${{ vars." + OutputRegistryVar + " }}" }

// VarRef wraps a build-arg NAME as the forge expression that reads the CI
// variable of the same name. A manifest declares only the NEUTRAL name; the
// value reference (`${{ vars.NAME }}`, the gha/forgejo spelling — Forgejo Actions
// is GHA-compatible) is composed HERE, exactly as composeImage applies the
// registry token, so the org.projectfile.ci.tools source stays vendor-neutral. A
// genuinely different engine (Tekton) supplies its own form over the same names.
func VarRef(name string) string { return "${{ vars." + name + " }}" }

// EnvRef is VarRef's sibling for a value the resolver itself put on the JOB env rather
// than one an operator configures on the instance — the build→live contract vars
// (M6E_IMAGE_FULLNAME and friends). Reading such a name off `vars.` yields empty.
func EnvRef(name string) string { return "${{ env." + name + " }}" }

// RobotTokenSecret is the ONE fleet-wide secret holding the robot-account token the
// checkout step uses for a repository the run-scoped token cannot read (a private
// submodule). One constant, not a per-project name: an operator provisions it ONCE as
// an organisation secret and every project that opts in spells it identically, so
// enabling a project is a boolean and never a naming decision.
//
// Deliberately outside the GITHUB_/GITEA_/FORGEJO_ namespaces the forge reserves for
// the context it injects itself — it REFUSES to store a secret under any of them, and
// those are exactly the prefixes one reaches for first when naming a robot token.
// TestRobotTokenSecretIsStorable holds that line against a future rename.
//
// #nosec G101 — this is the NAME a workflow reads the secret by, never a credential.
// The value it names lives in the forge's secret store and is resolved by the runner.
const RobotTokenSecret = "CI_ROBOT_TOKEN"

// checkoutTokenExpr renders the checkout `token:` expression. The `||` fallback is the
// NON-DESTRUCTIVE half of the knob: a project may opt in before the organisation holds
// the secret (or on a fork that never will), and the step still authenticates with the
// run-scoped token exactly as it does today instead of degrading to an anonymous clone.
// An unset secret reads as the empty string on both gha and forgejo, so the fallback
// fires on its own.
func checkoutTokenExpr() string {
	return "${{ secrets." + RobotTokenSecret + " || github.token }}"
}

// secretNameRE is the forge's own secret-name grammar (Forgejo and Gitea share it): an
// identifier — letters, digits and `_`, never a dash or a dot.
var secretNameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// reservedSecretPrefixes are the namespaces the forge keeps for the context it injects
// itself. Forgejo REFUSES to store a secret under any of them, so a name under one
// describes a secret that can never exist — not a preference an operator can satisfy
// later.
var reservedSecretPrefixes = []string{"GITHUB_", "GITEA_", "FORGEJO_"}

// validateSecretName reports whether the forge would accept a name as a secret. It
// guards RobotTokenSecret rather than any author input (the name is a constant now):
// the `||` fallback that makes the knob safe would MASK an unstorable name — the
// workflow renders clean, the secret can never be created, and the first symptom is a
// private submodule failing to clone in CI. Forgejo upcases a secret name on creation,
// so the prefix test upcases too.
func validateSecretName(name string) error {
	if !secretNameRE.MatchString(name) {
		return fmt.Errorf("%q is not a valid secret name: letters, digits and `_` only, never leading with a digit", name)
	}
	for _, prefix := range reservedSecretPrefixes {
		if strings.HasPrefix(strings.ToUpper(name), prefix) {
			return fmt.Errorf("%q starts with %s, a prefix the forge reserves and refuses to store "+
				"— name the robot secret something outside it", name, prefix)
		}
	}
	return nil
}

// ciContextExpr maps an abstract `ci:` context KEY (kept vendor-neutral in the
// manifest) to the GHA/Forgejo expression that supplies it. The build VERSION is
// the git ref name — m6e derives the same value from the git tag — so a cloud
// build stamps a meaningful provenance string into the image (write-lineage). A
// genuinely different engine (Tekton) would bring its own table over the SAME keys.
var ciContextExpr = map[string]string{
	ci.CIKeyVersion: "${{ github.ref_name }}",
	// The branch this run is on, empty on a tag. A FACT, not a policy: what it means
	// for the published tags (one `latest-<branch>`, no cascade, primary sink only) is
	// the versioned oci-push action's rule. The resolver hands over the ref and stops,
	// exactly as it does for the semver cascade it likewise never computes.
	ci.CIKeyPreview: "${{ github.ref_type == 'branch' && github.ref_name || '' }}",
}

// eventExpr maps one abstract trigger token (ci.WhenEvents) to the GHA/Forgejo
// job-`if:` expression that recognises it (Forgejo Actions is GHA-compatible, so
// both targets share the spelling). A genuinely different engine (Tekton) would
// bring its own table over the SAME tokens — the neutral model never names one.
//   - "tag"           → a tag ref pushed;
//   - "preview"       → a push to a branch that is not one of `primary`;
//   - "push:<branch>" → a push whose ref is that branch;
//   - "dispatch"      → the run was started by the manual button;
//   - "schedule"      → the run was started by a cron timer.
//
// `primary` is the project's primary-branch set (ci.Build.PrimaryBranches, defaulted by
// primaryBranches) and is read by the preview token alone.
func eventExpr(e string, primary []string) string {
	switch e {
	case ci.EventTag:
		return "startsWith(github.ref, 'refs/tags/')"
	case ci.EventPreview:
		// Spelled as "a branch ref, minus the primary names" rather than as a branch
		// allow-list, because the whole point of the token is that the branch cannot be
		// named ahead of time. Reads the `github` context only, so it stays legal at JOB
		// level on both forges — an `if:` naming `matrix` makes the WHOLE file unusable.
		parts := make([]string, 0, len(primary)+1)
		parts = append(parts, "startsWith(github.ref, 'refs/heads/')")
		for _, b := range primary {
			parts = append(parts, "github.ref != 'refs/heads/"+b+"'")
		}
		return strings.Join(parts, " && ")
	case ci.EventDispatch:
		return "github.event_name == 'workflow_dispatch'"
	case ci.EventSchedule:
		return "github.event_name == 'schedule'"
	}
	branch := strings.TrimPrefix(e, ci.EventPushPrefix)
	return "github.ref == 'refs/heads/" + branch + "'"
}

// primaryBranches is the project's primary-branch set with the fleet default applied:
// the branch names a push must NOT be on for `preview` to fire. A nil Build (a project
// declaring nothing the resolver reads) and a Build that simply records no default
// branch answer identically, because neither one states anything to the contrary.
func primaryBranches(b *ci.Build) []string {
	if b == nil || len(b.PrimaryBranches) == 0 {
		return ci.DefaultPrimaryBranches
	}
	return b.PrimaryBranches
}

// jobIf composes a job's `if:` condition from its neutral event set: one event is
// the bare expression, several are OR-ed (each parenthesised) so the job runs when
// ANY of its triggers fires. An empty set yields "" — no `if:` line, the job runs
// whenever the workflow triggers (the back-compatible default).
func jobIf(events []string, primary []string) string {
	switch len(events) {
	case 0:
		return ""
	case 1:
		return eventExpr(events[0], primary)
	}
	parts := make([]string, len(events))
	for i, e := range events {
		parts[i] = "(" + eventExpr(e, primary) + ")"
	}
	return strings.Join(parts, " || ")
}

// OnView is the resolved workflow `on:` trigger block — the union of every job's
// trigger surface, narrowed to exactly what the reachable jobs need. The default
// (any un-gated job present) is the broad `push:{} / pull_request:{}` the workflow
// has always emitted, so a project with no `when` anywhere renders byte-identically.
type OnView struct {
	PushAny      bool          // push: {} — all branches AND tags (the broad default)
	PushBranches []string      // push: {branches: [...]} — sorted, when narrowed
	PushTags     bool          // push: {tags: ["**"]} — a tag-only trigger
	PullRequest  bool          // pull_request: {}
	Dispatch     *DispatchView // workflow_dispatch — the manual-run button (+inputs); nil => none
	Schedule     []string      // cron expressions for `on: schedule`; empty => none
}

// buildOn derives the workflow `on:` block from the jobs' neutral event sets.
//
// goalScope is the OWNING goal's own `when` events when the file is pinned to a single
// goal (empty for the combined "ci" model, or a goal with no `when`). A goal's `when`
// scopes the ENTIRE file: a per-goal file fires EXACTLY on the goal's events, so its
// interior un-gated jobs must NOT widen the surface to push/PR (the bug this fixes — an
// `audited` goal gated `when:[schedule]` was still emitting push:{}/pull_request:{}
// because its interior scan jobs carry no `when`). When goalScope is set the surface is
// the union of the goal's events AND any GATED interior job's events (so a tag-scoped
// goal that also hosts a push:main-gated node still triggers on both); un-gated interior
// jobs contribute nothing and never widen it to push/PR (jobIf drives their `if:`).
//
// Without a goalScope the historic rule holds: if ANY reachable job is UN-gated (empty
// Events) the surface stays broad (`push:{}` covers branch AND tag pushes; `pull_request:{}`
// covers PRs) and gated jobs are filtered by their own `if:`; only when EVERY job is gated
// does it narrow to the union. A degenerate empty set falls back to broad (a valid `on:`
// is always emitted).
//
// dispatch/schedule are ORTHOGONAL to the push/PR axis: they are the GOAL's own additive
// triggers (tr — the timer/button DATA carried on the goal node), merged in regardless of
// the push narrowing. The dispatch/schedule TOKENS only drive a job's `if:` (eventExpr),
// never the push surface — hence they are skipped in the fold.
func buildOn(jobs []JobView, tr *TriggersView, goalScope []string) OnView {
	ov := OnView{}
	if tr != nil {
		ov.Dispatch = tr.Dispatch
		ov.Schedule = tr.Schedule
	}
	branches := map[string]bool{}
	tags := false
	// A `preview` gate cannot contribute a branch NAME — the branch is unknown until the
	// push happens — so it widens the surface to every branch and lets the job `if:` do
	// the narrowing. That is the same division of labour the broad default already uses.
	branchesAll := false
	// fold accumulates a token set's push/tag surface; dispatch/schedule are gating-only
	// (their `on:` entry comes from the goal's triggers, not the push axis).
	fold := func(events []string) {
		for _, e := range events {
			switch e {
			case ci.EventTag:
				tags = true
			case ci.EventPreview:
				branchesAll = true
			case ci.EventDispatch, ci.EventSchedule:
			default:
				branches[strings.TrimPrefix(e, ci.EventPushPrefix)] = true
			}
		}
	}
	if len(goalScope) > 0 {
		// Per-goal file with an explicit `when`: the goal SCOPES the file, so an un-gated
		// interior job (empty Events — often un-gated only because ANOTHER goal claims the
		// shared node unconditionally, e.g. source-is-secure under `published`) does NOT
		// widen this file to push/PR; it just runs whenever the goal's file fires. Gated
		// interior jobs still contribute their own events so they can fire (a tag-scoped
		// goal that also hosts a push:main-gated node must trigger on both). fold(nil) is
		// a no-op, so un-gated jobs add nothing.
		fold(goalScope)
		for _, j := range jobs {
			fold(j.Events)
		}
	} else {
		for _, j := range jobs {
			if len(j.Events) == 0 {
				// An un-gated job runs on every event ⇒ keep the broad push/PR surface,
				// additive to any dispatch/schedule trigger already set above.
				ov.PushAny, ov.PullRequest = true, true
				return ov
			}
			fold(j.Events)
		}
	}
	if len(branches) == 0 && !tags && !branchesAll {
		// No push/tag events: fall back to the broad surface ONLY when there is also no
		// dispatch/schedule trigger — a pure manual/cron file must not silently gain push/PR.
		if ov.Dispatch == nil && len(ov.Schedule) == 0 {
			ov.PushAny, ov.PullRequest = true, true
		}
		return ov
	}
	ov.PushTags = tags
	if branchesAll {
		// The `**` glob matches every branch and no tag, so a file gated [tag, preview]
		// still narrows its tag surface rather than falling back to a bare `push: {}`.
		ov.PushBranches = []string{"**"}
		return ov
	}
	if len(branches) > 0 {
		ov.PushBranches = make([]string, 0, len(branches))
		for b := range branches {
			ov.PushBranches = append(ov.PushBranches, b)
		}
		sort.Strings(ov.PushBranches)
	}
	return ov
}

// TriggersView is the neutral trigger surface carried on the Model (so `pf-ci resolve`
// emits it and a Tekton lowering reads the same data). The vendor `on:` spelling is
// composed per target at Workflow time (buildOn → OnView), never here.
type TriggersView struct {
	Dispatch *DispatchView `json:"dispatch,omitempty"`
	Schedule []string      `json:"schedule,omitempty"`
}

// DispatchView is the rendered workflow_dispatch trigger: the manual-run button and
// its typed inputs (the parametrised-build form). Empty Inputs => a bare button.
type DispatchView struct {
	Inputs []InputView `json:"inputs,omitempty"`
}

// InputView is one rendered manual-dispatch input. Default is already YAML-formatted
// for its type (quoted for string/choice, bare for boolean/number) so the template
// stays dumb; HasDefault distinguishes an authored "" from no default at all.
type InputView struct {
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	Description string   `json:"description,omitempty"`
	Required    bool     `json:"required,omitempty"`
	HasDefault  bool     `json:"-"`
	Default     string   `json:"default,omitempty"`
	Options     []string `json:"options,omitempty"`
}

// buildTriggers lowers a GOAL node's schedule/dispatch onto the Model view, formatting
// each input default for YAML by its type (the one type-aware step; the template
// renders the result verbatim). Empty schedule + nil dispatch => no triggers (a
// push/PR-only workflow).
// buildInputs is the org.projectfile.build.args list; when the dispatch declares
// `build-args: true` every EXPOSABLE arg (isExposableBuildArg) is appended as its own
// string input pre-filled with its default (a composed/file arg, and a declared
// foreign image, are skipped, matching the override wiring). An explicit `inputs:`
// entry of the same NAME wins (the author's typed form). The merged list is
// name-sorted for a byte-stable, alphabetised run form.
func buildTriggers(schedule []string, dispatch *ci.Dispatch, buildInputs []ci.BuildInput, b *ci.Build) *TriggersView {
	if len(schedule) == 0 && dispatch == nil {
		return nil
	}
	tv := &TriggersView{Schedule: schedule}
	if dispatch != nil {
		d := &DispatchView{}
		declared := make(map[string]bool, len(dispatch.Inputs))
		for _, in := range dispatch.Inputs {
			declared[in.Name] = true
			iv := InputView{
				Name: in.Name, Type: in.Type, Description: in.Description,
				Required: in.Required, Options: in.Options, HasDefault: in.HasDefault,
			}
			if in.HasDefault {
				iv.Default = formatDefault(in.Type, in.Default)
			}
			d.Inputs = append(d.Inputs, iv)
		}
		if dispatch.BuildArgs {
			for _, bi := range buildInputs {
				if declared[bi.Name] || !isExposableBuildArg(bi, b) {
					continue
				}
				iv := InputView{Name: bi.Name, Type: ci.InputString}
				if bi.Default != "" {
					iv.HasDefault, iv.Default = true, formatDefault(ci.InputString, bi.Default)
				}
				d.Inputs = append(d.Inputs, iv)
			}
			sort.Slice(d.Inputs, func(i, j int) bool { return d.Inputs[i].Name < d.Inputs[j].Name })
		}
		tv.Dispatch = d
	}
	return tv
}

// formatDefault renders an input default as a YAML scalar token: a boolean or number
// stays bare (so the forge parses a real bool/number), a string or choice is double-
// quoted (so a value like "8.5" or "" is unambiguous).
func formatDefault(typ, def string) string {
	switch typ {
	case ci.InputBoolean, ci.InputNumber:
		return def
	default:
		return fmt.Sprintf("%q", def)
	}
}

// lowerBuildArg resolves one declared build-arg to its concrete right-hand side
// (and, for a `file:` arg, the read-step the provider partial emits). The gha/
// forgejo spelling is applied HERE — the same compromise VarRef/composeImage make
// — so the manifest source stays vendor-neutral:
//   - literal → the interpolated `${pf.path}` value    (already resolved in ci.Load)
//   - var     → ${{ vars.<ref> }}                       a runtime CI variable
//   - ci      → the target's context expr               (version → ${{ github.ref_name }})
//   - file    → a FileRead{Name, Path}                  the action reads at build time (the
//     returned string is unused for file args)
//
// A `file:` ref or a literal that still carries {<axis>} placeholders lowers each
// to ${{ matrix.<axis> }} (substAxes) against THIS job's axes so every matrix cell
// reads its OWN file / names its OWN image (the per-cell pin).
func lowerBuildArg(ba ci.BuildArg, axes []ci.Axis) (string, *FileRead) {
	switch ba.Source {
	case ci.SourceLiteral:
		// An IN-document `${pf.path}` value, resolved to a literal by ci.Load's
		// interpolation pass (the identity args `${image.namespace}` / `${image.name}`
		// that used to be `{get: …}`). substAxes handles any residual {<axis>}.
		return substAxes(ba.Ref, axes), nil
	case ci.SourceVar:
		return VarRef(ba.Ref), nil
	case ci.SourceCI:
		return ciContextExpr[ba.Ref], nil
	case ci.SourceFile:
		return "${{ env." + ba.Name + " }}", &FileRead{Name: ba.Name, Path: substAxes(ba.Ref, axes)}
	}
	return "", nil // unreachable: ci.Parse rejects any other source
}

// buildInputValue lowers a non-file org.projectfile.build.args input to its CI
// variable expression. A literal default becomes ${{ vars.NAME || 'default' }}
// (always forwarded; the default wins when the var is unset); an empty default
// becomes bare ${{ vars.NAME }} (forward only when set). Mirrors the make reader's
// `export NAME ?= <default>` (+ always --build-arg) vs `export NAME ?=` (+
// conditional --build-arg) distinction.
//
// A default carrying make-plane ${VAR} refs (e.g. an image FROM ref
// ${B19_DOCKER_REGISTRY}/b19/dasel:${M6E_BASE_IMAGE_DEFAULT_VERSION}) takes a THIRD
// path: it cannot ride the ${{ vars.NAME || 'default' }} form — the forge plane
// never expands ${...}, so the literal default would reach the build backend as an
// INVALID reference (buildah: "parsing reference …: invalid reference format"), and
// ${...} cannot nest inside the quoted fallback anyway. Such a default is lowered by
// lowerMakeExpr to a juxtaposed forge expression — the SAME lowering an image path
// takes — so the build backend receives a valid ref. Override via vars.NAME does not
// apply to a composed default: override the registry/tag PRIMITIVE vars, not the
// composite (and the composite override never worked in the forge plane).
//
// exposed is the set of build-arg NAMES this file exposes as workflow_dispatch inputs
// (dispatchBuildArgs) — non-empty only when the goal declares `dispatch:{build-args:true}`.
// An exposed arg prepends `inputs.NAME ||` so a manual run's entered value wins; off a
// dispatch inputs.NAME is empty and the expression falls through unchanged (a non-exposed
// arg, or any file, renders byte-identically to before). A composed `${...}` default is
// never exposed (isExposableBuildArg), so its lowerMakeExpr form needs no prefix.
func buildInputValue(bi ci.BuildInput, b *ci.Build, defaults map[string]string, matrix, partial, exposed map[string]bool) string {
	if b != nil {
		if head, ok := b.ImageHeads[bi.Name]; ok {
			// A declared foreign image: the sink-composed ref with the
			// full-ref vars.<NAME> override in front — one expression, no
			// per-image variable an operator must set. OVERRIDES the arg's own
			// default shapes below (the declaration is the truth).
			return sinkImageExpr(bi.Name, head, b.Images[bi.Name], defaults, matrix, partial)
		}
	}
	if imageVarRefRe.MatchString(bi.Default) {
		return lowerMakeExpr(bi.Default, defaults, matrix, partial)
	}
	prefix := ""
	if exposed[bi.Name] {
		prefix = "inputs." + bi.Name + " || "
	}
	if bi.Default == "" {
		return fmt.Sprintf("${{ %svars.%s }}", prefix, bi.Name)
	}
	return fmt.Sprintf("${{ %svars.%s || '%s' }}", prefix, bi.Name, bi.Default)
}

// isExposableBuildArg reports whether a build.args input can round-trip as a plain
// workflow_dispatch string input: a literal- or empty-default STRING arg. A `file:`
// arg (a per-cell digest READ at build time), a composed `${...}` default (make-plane
// refs the forge cannot expand, so it cannot be pre-filled nor entered as a scalar),
// and a DECLARED foreign image (sink-composed at render; its vars.<NAME> override is
// a full ref, not an input) are excluded — the SAME predicate gates both the input
// exposure (buildTriggers) and the override wiring (buildInputValue via
// dispatchBuildArgs), so the form and the build agree.
func isExposableBuildArg(bi ci.BuildInput, b *ci.Build) bool {
	return bi.File == "" && !imageVarRefRe.MatchString(bi.Default) &&
		(b == nil || b.ImageHeads[bi.Name] == "")
}

// dispatchBuildArgs returns the set of org.projectfile.build.args NAMES this file exposes
// as workflow_dispatch inputs — non-empty only when the subtree is pinned to a SINGLE goal
// (ForGoal, the one-file-per-goal path, or a lone-goal project) whose `dispatch` declares
// `build-args: true`. Goal-scoped by construction: another goal's file (no flag) yields nil
// and renders byte-identically. nil for the combined model or a goal without the flag.
func dispatchBuildArgs(st *ci.Subtree, b *ci.Build) map[string]bool {
	if b == nil || !st.GoalsExplicit || len(st.Goals) != 1 {
		return nil
	}
	g := st.Nodes[st.Goals[0]]
	if g.Dispatch == nil || !g.Dispatch.BuildArgs {
		return nil
	}
	exposed := make(map[string]bool, len(b.Args))
	for _, bi := range b.Args {
		if isExposableBuildArg(bi, b) {
			exposed[bi.Name] = true
		}
	}
	return exposed
}

// substAxes lowers {<axis>} placeholders in s to the gha/forgejo cell expression
// ${{ matrix.<axis> }} — the SINGLE home of the cloud-spelling interpolation an
// image basename, a file-arg path, and a get:image.name share. A string with no
// placeholder, or an empty axis set (a non-cell job), is returned unchanged, so a
// non-matrix project renders byte-identically to before. The m6e lowering does the
// same {<axis>} -> $(<axis>) substitution against the cell sub-make's vars (Law 3:
// one neutral template, two engine spellings).
func substAxes(s string, axes []ci.Axis) string {
	for _, a := range axes {
		s = strings.ReplaceAll(s, "{"+a.Key+"}", "${{ matrix."+a.Key+" }}")
	}
	return s
}

// imageVarName matches a tool manifest `image:` value that is a ci.images VAR NAME
// (uppercase-only identifier, no slashes or colons) — the discriminator between the
// NEW agnostic form (var name → nested ${{ vars }} expression) and the legacy form
// (registry-relative path, kept for backward compat with deferred d9t tools).
var imageVarName = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

// parentImageVar finds the longest key K in buildImages such that K (stripped of its
// "_IMAGE" suffix) is a PROPER PREFIX of varName (also stripped of "_IMAGE"), returning
// K. This implements the auto-alias "longest-_IMAGE-prefix parent" rule: for
// "B19_GO_VET_IMAGE" with buildImages having "B19_GO_IMAGE", the parent is
// "B19_GO_IMAGE" (prefix "B19_GO" + "_" + "VET"). Returns "" when no parent is found
// (e.g., the var is itself a ci.images key with no shorter ancestor).
func parentImageVar(varName string, buildImages map[string]string) string {
	const suffix = "_IMAGE"
	if !strings.HasSuffix(varName, suffix) {
		return ""
	}
	prefix := strings.TrimSuffix(varName, suffix) // e.g., "B19_GO_VET"
	best := ""
	for k := range buildImages {
		if !strings.HasSuffix(k, suffix) {
			continue
		}
		kPrefix := strings.TrimSuffix(k, suffix) // e.g., "B19_GO"
		if strings.HasPrefix(prefix, kPrefix+"_") && len(kPrefix) > len(best) {
			best = kPrefix
		}
	}
	if best == "" {
		return ""
	}
	return best + suffix
}

// imagePath returns the PATH part of a full image value (strips any `:tag` suffix).
// Used to derive the literal fallback for the nested CI expression — the path is
// registry-relative or carries the registry prefix exactly as the build value stores it.
func imagePath(imageVal string) string {
	if i := strings.LastIndex(imageVal, ":"); i >= 0 {
		return imageVal[:i]
	}
	return imageVal
}

// imageTag is imagePath's twin: the TAG half of a ci.images value (everything after the
// last `:`), or "" when the value carries no tag. A make-var tag (`${M6E_BASE_IMAGE_..}`)
// and a literal pin (`v2.14.0`) are BOTH returned verbatim here; the caller (toolImageParts)
// decides which is mutable. Safe on a `${VAR}`-bearing path because the registry ref has no
// bare `:` — the only colon is the tag separator (`${D9T_DOCKER_REGISTRY}/d9t/x:${...}`).
func imageTag(imageVal string) string {
	if i := strings.LastIndex(imageVal, ":"); i >= 0 {
		return imageVal[i+1:]
	}
	return ""
}

// imageVarRefRe matches a `${NAME}` make-variable reference embedded in a ci.images
// path (the registry prefix, e.g. `${D9T_DOCKER_REGISTRY}/d9t/js-tools`). Each match
// is lowered by imagePathExpr to a juxtaposed ${{ vars.NAME }} fragment.
var imageVarRefRe = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// imageExpr lowers a tool whose manifest `image:` is a ci.images var NAME to its
// forge-plane registry path. The per-tool/per-family `vars.<NAME>_IMAGE` override
// tiers were REMOVED: an override knob let a stray repo variable shadow the real
// registry path (a leftover `D9T_JS_TOOLS_IMAGE` Forgejo var fed run-tool a ref that
// resolved to the mis-qualified `docker.io/<registry>/d9t/js-tools`). The image is now
// ALWAYS the ci.images registry path:
//
//	B19_GO_IMAGE       "reg.example/b19/go:tag"            -> reg.example/b19/go
//	D9T_JS_TOOLS_IMAGE "${D9T_DOCKER_REGISTRY}/d9t/js:tag" -> ${{ vars.D9T_DOCKER_REGISTRY }}/d9t/js
//
// A var with no own ci.images entry resolves through its longest-_IMAGE-prefix parent
// (B19_GO_VET_IMAGE -> B19_GO_IMAGE). A var with neither entry nor parent (or a nil
// Build) has no registry path to emit and degrades to a bare ${{ vars.<VAR> }} — a
// misconfiguration, not a supported override. The caller passes the path as
// `step.Image`; `runToolVersion()` still supplies the `:tag` part via imageTagExpr(),
// so the two pieces compose at render time: `image:version` = `path:tag`.
func imageExpr(varName string, b *ci.Build) string {
	if b == nil {
		return VarRef(varName)
	}
	resolved := varName
	val, ok := b.Images[varName]
	if !ok {
		if parent := parentImageVar(varName, b.Images); parent != "" {
			val, ok = b.Images[parent]
			resolved = parent
		}
	}
	lit := imagePath(val)
	if head, sank := b.ImageHeads[resolved]; sank && strings.HasPrefix(lit, head+"/") {
		// Sink-composed: the redirect + per-image override wrap, repository-scoped
		// (run-tool appends the tag). Defaults/matrix are nil — a tool PATH has no
		// build-arg refs to default (the series cases are build-args, not tools).
		return sinkImageExpr(resolved, head, lit, nil, nil, nil)
	}
	if ok && lit != "" {
		return imagePathExpr(lit)
	}
	return VarRef(varName)
}

// toolImageParts resolves a tool's ci.images VAR NAME to the (imagePath, pinnedTag) pair a
// run-tool step needs. It extends imageExpr with the two things a self-pinned EXTERNAL tool
// (`HADOLINT_IMAGE: hadolint/hadolint:v2.14.0`) requires and imageExpr alone could not give:
//
//   - TAG PRESERVATION (P1). imageExpr strips the tag and lets runToolVersion re-supply the
//     mutable dev↔latest flip var — correct for a WORKSPACE image (every one tags with
//     `:${M6E_BASE_IMAGE_DEFAULT_VERSION}`, the survey's uniform shape), WRONG for a vendor
//     image whose tag is a hard release pin. So the tag is classified: the flip var (or an
//     absent tag) yields "" (ride the flip, unchanged behaviour); ANY OTHER literal is
//     returned as pinnedTag so it round-trips instead of collapsing to `latest`.
//   - INSTANCE OVERRIDE (P2). A workspace image is already instance-redirectable via its
//     registry var (`${D9T_DOCKER_REGISTRY}`), so it keeps the bare lowered path. An external
//     image has NO registry var to point at a mirror, so — and ONLY here — the path is wrapped
//     `${{ vars.NAME || 'literal' }}`: the instance sets vars.NAME to its mirror, unset falls
//     back to the vendor default. The wrap is gated on "the lowered path == the literal path"
//     (no `${VAR}` was lowered ⇒ external), which is exactly the guard the removed per-tool
//     override tier lacked — a workspace path (its registry lowered to `${{ vars.* }}`) never
//     gets a shadowing whole-image var.
func toolImageParts(varName string, b *ci.Build) (path, pinnedTag string) {
	if b == nil {
		return VarRef(varName), ""
	}
	resolved := varName
	val, ok := b.Images[varName]
	if !ok {
		if parent := parentImageVar(varName, b.Images); parent != "" {
			val, ok = b.Images[parent]
			resolved = parent
		}
	}
	lit := imagePath(val)
	if head, sank := b.ImageHeads[resolved]; sank && strings.HasPrefix(lit, head+"/") {
		// Sink-composed: the redirect + per-image override wrap replaces the
		// external-literal wrap below (a sink-composed path is never a bare
		// literal, so that guard could not fire for it anyway).
		path = sinkImageExpr(resolved, head, lit, nil, nil, nil)
	} else if ok && lit != "" {
		path = imagePathExpr(lit)
		if path == lit { // no ${VAR} lowered ⇒ external literal ⇒ instance override knob
			path = "${{ vars." + varName + " || '" + lit + "' }}"
		}
	}
	if !ok || lit == "" {
		return VarRef(varName), "" // orphan var: bare ref, flip tag (imageExpr parity)
	}
	if tag := imageTag(val); tag != "" && tag != "${"+imageTagMakeVar+"}" {
		pinnedTag = lowerMakeExpr(tag, nil, nil, nil) // a plain literal stays verbatim
	}
	return path, pinnedTag
}

// lowerMakeExpr lowers a make-plane expression (a string with ${VAR} refs) to a
// juxtaposed forge expression: each ${VAR} becomes ${{ vars.VAR }} and the literal
// text between refs is left verbatim, since GHA/Forgejo interpolate ${{ }} and
// concatenate it with the surrounding text — the forge plane never expands ${...}
// itself, so a verbatim ${...} would reach a backend as an invalid reference. The
// ImageTagVar ref keeps its 'latest' fallback (a mirror of imageTagExpr), so a
// build-arg FROM tag does not collapse to an empty (invalid) tag when the var is
// unset. Any OTHER ref whose name has a known build-arg default (the `defaults`
// map, keyed by ARG name) keeps that default as its `|| 'literal'` fallback — the
// make plane defaults the var (`export NAME ?= resolute`), so the forge plane must
// too, or an embedded ref like ${B19_UBUNTU_SERIES} collapses to an empty segment
// (`reg/b19/ubuntu/:latest` — buildah: invalid reference format). Refs with no
// default stay bare ${{ vars.NAME }} (forward only when set). Shared by
// imagePathExpr (image paths, no defaults) and buildInputValue (build-arg defaults
// that are image FROM refs) — the single home of the make→forge lowering.
// A ref whose NAME is a matrix variable (an axis or a matrix.overrides extra var)
// takes the matrix spelling INSTEAD: the cell defines it, so it must read
// ${{ matrix.<NAME> }} (per-cell), never a vars-store lookup. This is what lets a
// composed FROM ref like ${B19_DOCKER_REGISTRY}/b19/llvm-${B19_LLVM_SERIES} lower
// per-cell when B19_LLVM_SERIES is a matrix extra var — the registry stays a vars
// lookup, the series follows the cell.
func lowerMakeExpr(s string, defaults map[string]string, matrix, partial map[string]bool) string {
	return imageVarRefRe.ReplaceAllStringFunc(s, func(m string) string {
		name := imageVarRefRe.FindStringSubmatch(m)[1]
		return "${{ " + refExprInner(name, defaults, matrix, partial) + " }}"
	})
}

// refExprInner is the INNER (context-free) lowering of one ${NAME} make ref — the
// same rule set lowerMakeExpr wraps in ${{ }}: the tag var maps to the workflow
// env hoist, a matrix-bound name reads the cell, a name with a literal build-arg
// default keeps it as its fallback, anything else is a bare vars lookup. Shared
// with sinkImageExpr, whose format() args are expressions WITHOUT the ${{ }}
// wrapper — one rule set, two wrappings.
func refExprInner(name string, defaults map[string]string, matrix, partial map[string]bool) string {
	if name == imageTagMakeVar {
		return "env." + TagEnvVar
	}
	if matrix[name] {
		return matrixExprInner(name, partial, defaults)
	}
	if def := defaults[name]; def != "" {
		return "vars." + name + " || '" + def + "'"
	}
	return "vars." + name
}

// matrixExprInner is the matrix branch of the inner lowering: a var bound on every
// cell needs only the bare matrix.NAME. A PARTIAL override (a var some cell leaves
// unset) backstops with the vars store then the build-arg default, so an
// un-decorated cell keeps the default instead of an empty value — `||` is
// first-truthy, so the cell's matrix value still wins wherever it is set.
func matrixExprInner(name string, partial map[string]bool, defaults map[string]string) string {
	if partial[name] {
		if def := defaults[name]; def != "" {
			return "matrix." + name + " || vars." + name + " || '" + def + "'"
		}
	}
	return "matrix." + name
}

// matrixVarExpr lowers a per-cell matrix variable NAME to its wrapped forge
// expression — matrixExprInner in ${{ }}, for the call sites that embed one
// matrix ref in surrounding text.
func matrixVarExpr(name string, partial map[string]bool, defaults map[string]string) string {
	return "${{ " + matrixExprInner(name, partial, defaults) + " }}"
}

// buildArgDefaults maps each build-arg NAME to its LITERAL default. Composed defaults
// (those carrying their own ${...}) are excluded: they are never referenced by name,
// and one would nest ${{ }} inside a quoted fallback.
func buildArgDefaults(b *ci.Build) map[string]string {
	m := map[string]string{}
	if b == nil {
		return m
	}
	for _, bi := range b.Args {
		if bi.Default != "" && !imageVarRefRe.MatchString(bi.Default) {
			m[bi.Name] = bi.Default
		}
	}
	return m
}

// imagePathExpr renders a lowered image PATH as a YAML `image:` value. A plain
// literal passes through unchanged (`reg.example/b19/go`); a path carrying an
// embedded ${REGISTRY} make var lowers each ref to a juxtaposed ${{ vars.* }}
// fragment. The forge plane (GHA/Forgejo) interpolates ${{ }} and concatenates it
// with the surrounding literal text, so the registry var and the path compose
// WITHOUT a format() wrapper — and since the forge plane never expands ${...}
// itself, leaving a make ref verbatim would hand run-tool an invalid ref:
//
//	reg.example/b19/go             -> reg.example/b19/go
//	${D9T_DOCKER_REGISTRY}/d9t/js  -> ${{ vars.D9T_DOCKER_REGISTRY }}/d9t/js
func imagePathExpr(path string) string {
	if !imageVarRefRe.MatchString(path) {
		return path
	}
	return lowerMakeExpr(path, nil, nil, nil)
}

// sinkImageExpr lowers a pull-sink-composed image reference (Build.Images value
// with a Build.ImageHeads entry) to ONE forge expression:
//
//	${{ vars.<overrideVar> || format('{0}<body…>:{N}', vars.SOURCE_DOCKER_REGISTRY || '<head>', <part refs…>) }}
//
// head is the literal repository head the sink contributed (kiota.ch,
// docker.io/damianbuho) and body is the rest of val — entirely the image's own
// identity, so the head is the only part SourceRegistryVar replaces. Every ${NAME}
// in the body lowers by refExprInner's rules and becomes a format() arg, which is
// why the WHOLE reference must live inside one ${{ }}: a piecewise spelling would
// juxtapose the override against the body and a full-ref vars.<NAME> value would
// append to it instead of replacing it. overrideVar names the per-image exception
// hatch — its scope is the CALLER's value (the full ref for a build-arg/pf-cli
// input, the repository for a tool `image:` whose tag run-tool appends), matching
// the external-literal override toolImageParts already wraps. format() braces in
// literal body text are doubled, per the expression grammar. A val the head does
// not prefix (a stale head) falls to piecewise lowering — the old spelling —
// rather than guessing a split.
func sinkImageExpr(overrideVar, head, val string, defaults map[string]string, matrix, partial map[string]bool) string {
	if !strings.HasPrefix(val, head+"/") {
		return lowerMakeExpr(val, defaults, matrix, partial)
	}
	body := val[len(head):]
	var fmtStr, args []string
	fmtStr = append(fmtStr, "{0}")
	args = append(args, "vars."+SourceRegistryVar+" || '"+head+"'")
	rest := body
	for {
		loc := imageVarRefRe.FindStringSubmatchIndex(rest)
		if loc == nil {
			fmtStr = append(fmtStr, escapeFormatLiteral(rest))
			break
		}
		fmtStr = append(fmtStr, escapeFormatLiteral(rest[:loc[0]]))
		name := rest[loc[2]:loc[3]]
		fmtStr = append(fmtStr, "{"+strconv.Itoa(len(args))+"}")
		args = append(args, refExprInner(name, defaults, matrix, partial))
		rest = rest[loc[1]:]
	}
	return "${{ vars." + overrideVar + " || format('" + strings.Join(fmtStr, "") +
		"', " + strings.Join(args, ", ") + ") }}"
}

// escapeFormatLiteral doubles the braces a format() template would otherwise read
// as placeholders — registry references carry none, but the escape keeps the
// builder honest for any body text.
func escapeFormatLiteral(s string) string {
	return strings.NewReplacer("{", "{{", "}", "}}").Replace(s)
}

// ImageArchiveEnv is the build→scan contract variable. A consuming portable job
// receives `M6E_IMAGE_ARCHIVE=<Stem>.tar` (a path to the downloaded OCI archive,
// relative to the workdir); the d9t `auto-grype`/`auto-trivy`/`auto-dockle`
// entrypoint reads it and scans `oci-archive:$M6E_IMAGE_ARCHIVE` DAEMONLESSLY —
// the same variable m6e sets, so both lowerings scan the identical artifact. The
// value is the bare path; each scanner applies its own access scheme (grype
// `docker-archive:`, trivy/dockle `--input`).
const ImageArchiveEnv = "M6E_IMAGE_ARCHIVE"

// ImageFullnameEnv is the build→live contract variable, the live-stack analogue of
// ImageArchiveEnv. A FUSED member that loads the build tar into the daemon (the `live`
// compose job) gets `M6E_IMAGE_FULLNAME=<the loaded ref>` so its compose file finds
// the image by tag — the SAME ref container-build stamped into the archive (composeImage
// over the per-cell basename, registry-less, matching the loaded tag) and the SAME name
// m6e's executor exports, so both lowerings name one image. The resolver injects only
// this one value (the rest of the compose-runtime env is authored m6e convention via
// Manifest.EnvSet); a non-compose fuse simply never reads it.
const ImageFullnameEnv = "M6E_IMAGE_FULLNAME"

// ImagePublishedEnv is the PUBLISHED-image contract variable, the audit-re-scan analogue of
// ImageArchiveEnv: a scheduled `audited` run has NO build in scope, so the image scanners
// cannot read a freshly built tar (M6E_IMAGE_ARCHIVE) or a live daemon ref — they must PULL
// the already-published image by ref. A scanner tool that requests this var (via `env:`)
// gets `M6E_IMAGE_PUBLISHED=<OUTPUT_REGISTRY>/<per-cell basename>:latest` — the SAME registry
// oci-push prefixes the push ref with and the mutable `latest` tag oci-push always publishes,
// so the audit scans EXACTLY what was pushed. Unlike a forwarded credential name this value is
// resolver-COMPUTED (publishedImageRef), the forge-plane sibling of the make plane's
// M6E_IMAGE_PUBLISHED (container.mk).
const ImagePublishedEnv = "M6E_IMAGE_PUBLISHED"

// ComposeProjectEnv / ContainerInstanceEnv are the live-stack IDENTITY contract
// variables — DERIVED siblings of ImageFullnameEnv, not authored literals. The
// fused compose job names its project, container, and network `ci-<basename>`
// (slashes → dashes), the SAME value m6e's make plane computes locally
// (M6E_RESOURCE_PREFIX=ci + M6E_CONTAINER_NAME from NAMESPACE/PROJECT), so a forge
// run and a local `make` name ONE stack. Deriving it kills the generic `app`
// placeholder AND a per-include hardcode: the include declares `fuse: live` only,
// the resolver stamps the real identity it already holds from the per-cell basename.
const (
	ComposeProjectEnv    = "M6E_COMPOSE_PROJECT_NAME"
	ContainerInstanceEnv = "M6E_CONTAINER_INSTANCE"
	composeProjectPrefix = "ci-" // mirrors m6e's M6E_RESOURCE_PREFIX in CI mode
)

// RunScopeEnvVar (M6E_RUN_SCOPE) is the workflow-level env name the run-scope pair
// (github.run_id-run_attempt) is hoisted to: defined ONCE in the workflow `env:` block
// (ResolverEnv), referenced as `${{ env.M6E_RUN_SCOPE }}` everywhere the pair repeats —
// M6E_COMPOSE_PROJECT_NAME, M6E_CONTAINER_INSTANCE, the run-scoped image tag, and the
// docker-cleanup reaps. Like TagEnvVar this is RENDER-LAYER only (the neutral Model
// never names it); a future Tekton lowering brings its own run-unique spelling.
const RunScopeEnvVar = "M6E_RUN_SCOPE"

// runScopeSuffix makes the live-stack IDENTITY unique per workflow RUN, not just per
// cell. ci-<basename>-<series> is unique among one run's matrix cells, but two runs of
// the same pipeline (concurrent pushes, OR a manual re-run) recompute the SAME stem;
// on a host-mode runner (CAPACITY=NUMPROCS — many jobs share one podman) the second
// run's `up`/`down` then reaps the first run's live container mid-test (rc=137 SIGKILL,
// the resolute symptom). github.run_id is unique per run (distinct pushes never
// collide); github.run_attempt distinguishes a re-run of the SAME run_id. Applied to
// the project/container/network ONLY — NEVER ImageFullnameEnv, which must stay the
// exact ref container-build stamped into the tar or the live `load` misses it.
//
// The pair rides the workflow env var (RunScopeEnvVar) instead of being inlined: the
// `${{ github.run_id }}-${{ github.run_attempt }}` form is defined ONCE (ResolverEnv)
// and referenced everywhere via `${{ env.M6E_RUN_SCOPE }}`.
const runScopeSuffix = "-${{ env." + RunScopeEnvVar + " }}"

// artifactScopeSuffix scopes an artifact NAME to the run, NOT the attempt. run_id
// already disambiguates distinct runs (the only case the stale-artifact hazard needs);
// run_attempt must NOT be here. A partial re-run (failed jobs only) does not re-execute
// a succeeded producer, so its artifact keeps the OLD attempt number while the
// re-running consumer computes the NEW one — the "Unable to find an artifact" break.
// run_attempt stays on the live-stack identity (ephemeral, wanted unique per attempt).
//
// It scopes the self-image TAG too (selfImageTagExpr), because that tag is content of
// the artifact this suffix names: container-build stamps it into the tar, a consumer
// docker-loads it back. Name and payload must carry the SAME scope or a re-run loads a
// tar tagged for the attempt that built it and then looks up its own attempt's tag.
//
// NOTE: artifactScopeSuffix is NOT hoisted to a workflow env var. Unlike runScopeSuffix
// (which is read at expression-eval time by the docker-cleanup action `with:` and the
// compose identity), the artifact NAME is materialised by GHA/Forgejo as a literal
// string identifier (download/upload `name:`) — `${{ env.* }}` is resolved fine, but
// the artifact protocol treats the name as opaque and the bare github.run_id form is
// the established contract. Keep it inline.
const artifactScopeSuffix = "-${{ github.run_id }}"

// resolverEnv returns the workflow-level `env:` entries the RENDERER itself defines
// (NOT the projectfile-authored env, which is Model.Env). These are the two hoists
// that DRY the generated YAML: the tag expression (TagEnvVar) and the run-scope pair
// (RunScopeEnvVar), each defined ONCE here and referenced as `${{ env.M6E_* }}` at
// every site instead of repeating the full `${{ vars.* }}` / `${{ github.* }}` form.
// RENDER-LAYER only: the neutral JSON Model never carries these (a future Tekton
// lowering brings its own spelling), so they are composed here at Workflow time, not
// in Build. Order is fixed (tag, then run-scope) for byte-stable output.
func resolverEnv() []KV {
	return []KV{
		{Key: TagEnvVar, Value: "${{ vars." + ImageTagVar + " || 'latest' }}"},
		{Key: RunScopeEnvVar, Value: "${{ github.run_id }}-${{ github.run_attempt }}"},
	}
}

// inlineHoists expands the resolverEnv indirections (`${{ env.M6E_TAG }}`,
// `${{ env.M6E_RUN_SCOPE }}`) back to their DEFINITIONS. The `env` CONTEXT is not
// available while an `env:` block is itself being evaluated — the workflow- and
// job-level blocks are resolved before any env exists, so a value reading `env.*`
// there is a SCHEMA error, not an empty string: Forgejo rejects the whole file with
// "Unknown Variable Access env" (GHA: "Unrecognized named-value: 'env'"). Only the
// LATER-evaluated sites (step `with:`/`run:`/`env:`) may keep the compact indirection,
// so this expansion is applied at exactly the two block sites and nowhere else.
// Driven by resolverEnv() itself, so a third hoist is covered by construction; the
// definitions carry no env refs, so one pass is enough.
//
// The bare-token pass (`env.M6E_TAG` WITHOUT the wrapper) covers hoist refs embedded
// INSIDE a larger expression — a format() arg in a sink-composed image ref, where the
// exact-string form never occurs. It applies only to single-expression definitions
// (innerExpr): the tag var's definition has an inner form, but a juxtaposed
// definition like the run scope's (`${{ github.run_id }}-${{ github.run_attempt }}`)
// has none — its halves would splice mid-expression — so such a hoist must never be
// referenced bare inside an env block, and isn't.
func inlineHoists(s string) string {
	for _, kv := range resolverEnv() {
		s = strings.ReplaceAll(s, "${{ env."+kv.Key+" }}", kv.Value)
		if inner, ok := innerExpr(kv.Value); ok {
			s = strings.ReplaceAll(s, "env."+kv.Key, inner)
		}
	}
	return s
}

// innerExpr strips the single ${{ … }} wrapper off a hoist definition, yielding the
// expression as it can appear as a format() argument or inside another expression.
func innerExpr(v string) (string, bool) {
	const open, shut = "${{ ", " }}"
	if !strings.HasPrefix(v, open) || !strings.HasSuffix(v, shut) {
		return "", false
	}
	return v[len(open) : len(v)-len(shut)], true
}

// inlineHoistsEnv applies inlineHoists across a job `env:` block. Fresh slice: bindEnv
// hands back the SHARED model slice for a job that forwards no credential, and the
// per-target render must never mutate it.
func inlineHoistsEnv(in []EnvVar) []EnvVar {
	if len(in) == 0 {
		return in
	}
	out := make([]EnvVar, len(in))
	for i, e := range in {
		out[i] = EnvVar{Key: e.Key, Value: inlineHoists(e.Value)}
	}
	return out
}

// composeProject lowers a per-cell image basename (b19/ubuntu) to the compose
// project/container/network stem (ci-b19-ubuntu) — slashes → dashes, exactly the
// transform m6e applies deriving M6E_CONTAINER_NAME from NAMESPACE/PROJECT.
func composeProject(basename string) string {
	return composeProjectPrefix + strings.ReplaceAll(basename, "/", "-")
}

// stepAlwaysExpr is the step `if:` guard a `when: always` teardown tool lowers to. The
// always() expression is identical on GHA and Forgejo, so the one spelling serves both.
const stepAlwaysExpr = "${{ always() }}"

// Builders is the set of recognised container-build backends. An overlay that
// sets an unknown builder fails the render (a config typo must not produce a
// dangling `uses:` ref) — same fail-fast stance as the unprovided-provider guard.
var Builders = map[string]bool{BackendBuildx: true, BackendBuildah: true}

// sortedBuilders lists the recognised backends (for a deterministic error).
func sortedBuilders() []string {
	keys := make([]string, 0, len(Builders))
	for k := range Builders {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// WorkflowExtEnv is the single knob that picks the committed workflow file
// extension. The workspace standard is ".yaml" (see m6e/AGENTS.md: the data
// plane is ".yaml"); a developer who prefers ".yml" exports
// PF_CI_WORKFLOW_EXT=.yml and BOTH planes honour it — pf-ci renders to
// ".yml" here, and m6e's act.mk derives its M6E_ACT_WORKFLOW_EXT default from
// the same var, so `make act-*` finds the ".yml" file. One knob, no lockstep
// burden on the operator.
//
// A process-env override (NOT a forge var like PF_CLI_IMAGE): it is a
// dev-machine generation preference, not a value the cloud runtime reads. The
// default is the workspace standard so a checkout renders ".yaml" with no
// config; the override is the documented escape hatch.
const WorkflowExtEnv = "PF_CI_WORKFLOW_EXT"

// workflowExtVar is the resolved value of WorkflowExtEnv, read ONCE at package
// init (Targets is a package var, so the targets read this, not the env, on
// every render — a mid-process flip would be silently ignored). A test that
// needs the other extension constructs a Target literal directly.
var workflowExtVar = resolveWorkflowExt()

// The two recognised committed-workflow extensions. Named (not inline literals)
// so the switch + the validation error share one spelling each, and goconst
// stays quiet — the same convention as the Backend* identifiers.
const (
	extYAML = ".yaml" // the workspace standard (m6e/AGENTS.md: the data plane is .yaml)
	extYML  = ".yml"  // the documented override for developers who prefer the legacy form
)

// resolveWorkflowExt reads WorkflowExtEnv (default extYAML) and validates it.
// An empty or unset value yields the default; an UNRECOGNISED value is a hard
// error — same fail-fast stance as the unrecognised-builder guard (a config
// typo like "yaml" or ".YAML" must not produce a silently-wrong filename).
func resolveWorkflowExt() string {
	switch v := os.Getenv(WorkflowExtEnv); v {
	case "", extYAML:
		return extYAML
	case extYML:
		return extYML
	default:
		panic(fmt.Sprintf("%s=%q is not a recognised workflow extension (want %q or %q)", WorkflowExtEnv, v, extYAML, extYML))
	}
}

// workflowExt returns the resolved committed-workflow file extension. Each
// per-goal target lowers to `<OutDir>/<goal><ext>`; the lefthook target lowers
// to `lefthook<ext>`. Kept as a func (not a const) so the call sites in Targets
// read symmetrically with the other field initialisers.
func workflowExt() string { return workflowExtVar }

// Targets is the registry of supported render targets. GHA and Forgejo point at
// the same template by design — implementing both "at once" is one shared
// workflow plus two adapter rows.
var Targets = map[string]Target{
	// Action majors DIVERGE by target because the ARTIFACT protocol does. checkout is
	// Node-24-native on both (checkout@v7 — the runner ships b19/node-24, so the old
	// Node-20 floor is gone). But upload/download-artifact@v4+ ride `@actions/artifact`
	// v2, which treats any non-github.com server as GHES and HARD-REFUSES
	// ("@actions/artifact v2.0.0+ … not currently supported on GHES") — and Forgejo IS
	// such a server. Forgejo's backend implements the v1 artifact protocol, i.e. the
	// `@v3` actions. So gha gets the current majors (upload@v7 / download@v8, v2
	// protocol) and forgejo is pinned to @v3 (v1 protocol). This is a Forgejo PLATFORM
	// constraint, not project policy, hence a lowering default (Law 3). NOTE: the upload
	// major MUST match the action-library container-build composite that uploads the build
	// tar — gha→buildx (upload@v7), forgejo→buildah (upload@v3); the backend split is
	// also the forge split, so each composite carries its own paired version.
	TargetGHA: {
		Key: TargetGHA, RunsOn: "ubuntu-latest", Checkout: "actions/checkout@v7",
		Download: "actions/download-artifact@v8", Upload: "actions/upload-artifact@v7",
		UploadOverwrite: true,
		OutDir:          ".github/workflows", Ext: workflowExt(), Template: "workflow.yaml.tmpl",
		ActionLib: "projectfile/actions", ActionVer: "v1", Builder: BackendBuildx,
		CacheRestore: CacheActionRestore, CacheSave: CacheActionSave, CacheDir: CacheDirGHA,
	},
	TargetForgejo: {
		Key: TargetForgejo, RunsOn: "docker", Checkout: "actions/checkout@v7",
		Download: "actions/download-artifact@v3", Upload: "actions/upload-artifact@v3",
		OutDir: ".forgejo/workflows", Ext: workflowExt(), Template: "workflow.yaml.tmpl",
		// buildah, NOT buildx: our forgejo runner (r8e/forgejo-runner) is host-mode
		// podman, where `podman-docker` shims the top-level `docker` command but has
		// no `buildx` subcommand. buildah is the daemonless, podman-native analogue
		// and emits the IDENTICAL OCI-tar hand-off. gha keeps buildx (real docker on
		// GitHub-hosted runners). Overridable per project via .forgejo.builder.
		ActionLib: "projectfile/actions", ActionVer: "v1", Builder: BackendBuildah,
		// No CacheRestore: a self-hosted runner binds a PERSISTENT cache dir (RO) the
		// refresh pipeline fills — no per-job restore action. The emptiness IS the fork.
		CacheDir: CacheDirForge,
	},
	// lefthook is the THIRD lowering of the same DAG — git hooks, not a cloud
	// workflow. It carries only Key/OutPath/Template: the job adapter fields
	// (RunsOn, Checkout, artifact actions, Builder) are cloud concepts a git hook
	// has no use for, so they stay zero. Rendered by render.Lefthook (a node-level
	// projection), NOT the job pipeline — see TargetLefthook.
	TargetLefthook: {
		Key: TargetLefthook, OutPath: "lefthook" + workflowExt(), Template: "lefthook.yaml.tmpl",
	},
}

// TargetLefthook is the git-hook lowering's key. main.cmdGenerate routes it to
// render.Lefthook instead of the job pipeline (resolve→Build→Workflow), because
// the hook nodes are non-goals that never become cloud jobs.
const TargetLefthook = "lefthook"

// TargetKeys lists the registered targets, sorted (for CLI help / errors).
func TargetKeys() []string {
	keys := make([]string, 0, len(Targets))
	for k := range Targets {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// HookStages is the ordered allow-list of git-hook stages the lefthook target
// recognises — the same closed-vocabulary pattern as WhenEvents. A node whose
// NAME is one of these lowers to the matching lefthook hook; every other node
// name is an ordinary DAG join label and is ignored here, so a hook is OPT-IN by
// naming convention (name a node `pre-commit` and it becomes the pre-commit
// hook). The slice is the extension point — a new client hook is one entry (it
// must be a name lefthook accepts AND a node in the DAG). Ordered so the rendered
// lefthook config is byte-stable regardless of map iteration order.
var HookStages = []string{"pre-commit", "pre-push"}

// HookNodes returns the HookStages that EXIST as nodes in the DAG, in HookStages
// order — the hooks render.Lefthook will emit. The single source for both the
// render and main's "(N hooks)" summary.
func HookNodes(st *ci.Subtree) []string {
	var out []string
	for _, stage := range HookStages {
		if _, ok := st.Nodes[stage]; ok {
			out = append(out, stage)
		}
	}
	return out
}

// LefthookView is the lefthook template input: the hooks to emit plus the target
// (for the regenerate hint). No jobs, runners, or matrix — a git-hook config is a
// flat map of stage -> dispatch command.
type LefthookView struct {
	Target Target
	Hooks  []HookView
}

// HookView is one rendered lefthook hook. Stage is simultaneously the git-hook
// name, the DAG node name, and the `make <stage>` launcher target (m6e's
// select.mk derives one launcher per node), so the dispatch needs nothing more.
type HookView struct {
	Stage string
}

// Lefthook renders the dispatcher-form lefthook config (lefthook.yaml by default):
// for each HookNodes entry, a hook that runs `make <stage>`. The launcher resolves
// the node's tool closure at run time, so routing has ONE source of truth (the DAG)
// and the file regenerates only when a hook node is added/removed — never when its
// membership changes. A project declaring no hook node renders a header-only (no-op)
// config. This is a NODE-level projection: it reads st.Nodes directly and never touches
// the job pipeline (resolve/Build/Workflow), because the hook nodes are non-goals.
func Lefthook(st *ci.Subtree, target Target) ([]byte, error) {
	view := LefthookView{Target: target}
	for _, stage := range HookNodes(st) {
		view.Hooks = append(view.Hooks, HookView{Stage: stage})
	}
	// Parse ONLY the lefthook template: the job templates (workflow/steps) declare
	// funcs (stepbody) bound at render time in Workflow, so pulling them in here
	// would fail the parse over an unbound function.
	root, err := template.New(target.Template).Funcs(funcs).ParseFS(templatesFS, "templates/"+target.Template)
	if err != nil {
		return nil, fmt.Errorf("parse %s template: %w", target.Key, err)
	}
	var buf bytes.Buffer
	if err := root.ExecuteTemplate(&buf, target.Template, view); err != nil {
		return nil, fmt.Errorf("render %s: %w", target.Key, err)
	}
	return buf.Bytes(), nil
}

// EnvVar is one `KEY: ${{ matrix.KEY }}` binding on a CELL job, so the tool reads
// the cell's axis value from its environment unchanged (the axis KEY is the
// literal build variable — no alias layer).
type EnvVar struct {
	Key   string
	Value string
}

// StepView is ONE tool rendered as a step of its node-job (Decision 2: node = job,
// tool = step). It carries the full per-tool shape a step fragment needs — the
// command (a bare host `run:` when no image, else the run-tool action), an `action:`
// dispatch (container-build / oci-push), the env NAMES forwarded into the container,
// the action `with:` inputs, and a generic build-artifact upload. The job header —
// checkout, needs, matrix, the `env:` block, the shared downloads/`docker load` — is
// painted ONCE by the node-job; a step never checks out or sets the matrix. (A tool's
// `guard` predicate is not lowered — see Manifest.)
type StepView struct {
	Name string `json:"name"`
	// If is the step's run guard, rendered verbatim as `if: <If>`. The only producer
	// today is a tool's `when: always` (a fused-job teardown), lowered to the GHA/Forgejo
	// `${{ always() }}` expression so a cleanup member runs even after an earlier step
	// failed. Empty => no guard (the default: skip if an earlier step failed).
	If string `json:"if,omitempty"`
	// Advisory carries Manifest.Advisory: this step reports its failure but never blocks
	// the job. It lowers by step KIND — `continue-on-error: true` on a host `run:` step,
	// the run-tool action's `advisory` input on a containerised one — so the two paths
	// share one meaning and neither hides the failure. False (the default) is blocking.
	Advisory bool   `json:"advisory,omitempty"`
	Run      string `json:"run"`
	Args     string `json:"args,omitempty"`
	Image    string `json:"image,omitempty"` // AGNOSTIC image PATH; RunTool* split it for the action
	// PinnedTag is a tool image's HARD-pinned tag (an external vendor release like
	// `v2.14.0`) carried SEPARATELY from Image, which stays path-only. Set ONLY when the
	// ci.images value's tag is a literal — NOT the mutable `${M6E_BASE_IMAGE_DEFAULT_VERSION}`
	// flip var. RunToolVersion prefers it over the flip expr, so the pin round-trips instead
	// of collapsing to `latest`; RunToolPull reads its presence to pick `--pull missing`
	// (an immutable tag never moves, so re-checking the registry each run only burns pull
	// budget). Empty => the image rides the dev↔latest flip var (the workspace default).
	PinnedTag string `json:"pinned-tag,omitempty"`
	// SelfImage marks a tool that runs in the project's OWN freshly built image — an
	// `image: M6E_IMAGE_FULLNAME` row, the tool-side twin of the build→live contract var
	// the fused live job already exports. That image reached the runner via `docker load`
	// of the build artifact and exists in NO registry, so RunToolPull pins `never`:
	// run-tool's `always` default would try to fetch the run-scoped tag and fail.
	SelfImage bool   `json:"self-image,omitempty"`
	Action    string `json:"action,omitempty"` // action-library path — dispatch to stepfrag providers/<action>
	// Network is the symbolic Manifest.Network (`live` today): the tool's container joins
	// the live compose stack's network to reach it by service name. RunToolNetwork lowers
	// it to the run-tool `network:` input. Empty => the default bridge (every non-live tool).
	Network string `json:"network,omitempty"`
	// Env is the step's own `env:` VALUES (matrix axis bindings, the image-archive
	// path, container-build var/get/ci values). The node-job lifts the UNION to its
	// `env:` block; the keys also name what a run-tool step forwards (EnvArg).
	Env []EnvVar `json:"-"`
	// EnvNames are the manifest credential names this step forwards into its container
	// (the VALUE binds at the node-job via the credentials overlay). Part of EnvArg.
	EnvNames []string `json:"env,omitempty"`
	// EnvForward / BuildArgs are the RESOLVED NAME=VALUE forwards a composite ci-action
	// receives (run-tool `env:` / container-build `build-args:`). A composite action does
	// NOT inherit the caller job's `env:` block as PROCESS env (the Forgejo runner drops
	// it), so a by-NAME `--env`/`--build-arg` passthrough reads an UNSET var → empty. The
	// value must ride the action input instead. Resolved at Workflow time (envPairs) from
	// the job's post-bindEnv env map so matrix axes, the image-archive path, AND a
	// per-target credential ref all reach the container. A name with no known value keeps
	// an empty Value and is forwarded by NAME alone (the pre-fix graceful path).
	EnvForward []EnvVar `json:"-"`
	BuildArgs  []EnvVar `json:"-"`
	// ImageBasename / BuildArgNames / FileArgs / MountsArg are the container-build action
	// `with:` inputs (Phase 2 clean scalars); ImageBasename is also the oci-push push ref.
	ImageBasename string `json:"image-basename,omitempty"`
	// PublishVersion is the oci-push `version:` input — the git tag that drives the
	// SEMVER tag cascade (X.Y.Z fans out to X.Y, X, latest; a pre-release publishes its
	// exact spelling only). It is the SAME `ci:version` context the M6E_VERSION build-arg
	// rides (→ ${{ github.ref_name }}); publish-image is gated `when:[tag]`, so on a tag
	// push ref_name IS the version. The cascade itself is imperative shell in the versioned
	// oci-push action (Law 2), never here — the resolver only hands it the tag.
	PublishVersion string `json:"publish-version,omitempty"`
	// PublishPreview is the oci-push `preview:` input — the BRANCH this run is on, empty
	// on a tag (ci:preview). Non-empty makes the action collapse the cascade to a single
	// `latest-<branch>` tag and publish it to the primary sink alone. Set beside
	// PublishVersion and for the same reason: the resolver hands over the ref and the
	// versioned action owns what tags come out of it. oci-push ONLY — a forge release is
	// an object built around a tag and has no preview form.
	PublishPreview string `json:"publish-preview,omitempty"`
	// PublishRefs is the oci-push `refs:` input — one `<sink> <ref>` line per
	// destination this lowering publishes to, each composed by the document that
	// declared the sink. It is what lets ONE archive land nested on one registry and
	// flattened on another; the resolver threads finished references and knows no path
	// shape. Empty => the project declares no route, and the action falls back to the
	// single OUTPUT_REGISTRY destination it always had. Keyed by LOWERING because a
	// StepView is rendered once for every target: the axes are substituted here (they
	// are target-independent), and the template selects the list its own target
	// publishes.
	PublishRefs map[string][]ci.SinkRef `json:"publish-refs,omitempty"`
	// PullRefs is the audit re-scan target per LOWERING — the composed
	// `publish.<forge>.pull` destination, keyed and axis-substituted exactly as
	// PublishRefs is, and for the same reason: one StepView serves every target. Only a
	// scanner requesting ImagePublishedEnv carries it. Empty on a target whose route
	// declares no pull, where the OUTPUT_REGISTRY prefix in Env stands.
	PullRefs map[string]string `json:"pull-refs,omitempty"`
	// ReleaseTargets is the binaries-plane twin of PublishRefs: where this lowering
	// attaches its release. Keyed by LOWERING for the same reason — one StepView is
	// rendered for every target. Empty => the ambient Forgejo context, i.e. the forge
	// the pipeline runs on, which is every project today.
	ReleaseTargets map[string][]ci.ReleaseTarget `json:"release-targets,omitempty"`
	// ReleaseURL / ReleaseRepo are the forgejo-release `server-url:` / `repo:` inputs,
	// bound to the destination axis's include rows. They travel as matrix variables
	// rather than as the axis itself because the axis carries the NAME the token
	// derives from, and a URL cannot serve as a credential key.
	ReleaseURL  string `json:"-"`
	ReleaseRepo string `json:"-"`
	// PublishSink is the publish action's `sink:` input — WHICH destination this cell
	// owns, bound to the destination matrix axis publishCells appends. It is what turns
	// a loop inside one action into one CELL per destination, so a registry refusing a
	// push fails its own cell rather than the release. Render-only: the axis values are
	// per LOWERING (a GitHub pipeline pushes to ghcr, a kiota one to kiota), so the
	// binding is made per target beside `if:` and the credential refs. Empty on a target
	// this step declares no route for, which is the historical single-job fan-out.
	PublishSink string `json:"-"`
	// PublishIf is this cell's run-time destination gate (sinkGate), rendered as the
	// publish step's own `if:`. It rides the STEP because a JOB-level `if:` may not read
	// the `matrix` context: both forges validate `if:` against a whitelist of github /
	// needs / vars / inputs, and a job gate naming an axis makes the WHOLE workflow file
	// unusable — every job in it, not just the publish. Same per-target lifetime as
	// PublishSink, and reset beside it.
	PublishIf string `json:"-"`
	// Arch is this cell's target architecture, bound to the M6E_ARCH axis ci.Load mints
	// from org.projectfile.architecture. It lowers to container-build's `platform:` (WHAT
	// to build) and oci-push's `arch:` (WHERE to publish it), the producer and consumer
	// ends of one arch cell. Both are needed together: the axis alone would fan three
	// cells that each build the host arch and push it over one another's tag.
	//
	// Read off the JOB's own axes, never from the publish route — a project that declares
	// no route still fans over arch, and would otherwise publish every cell to one ref.
	// Empty when nothing minted the axis, which renders today's workflow unchanged.
	Arch string `json:"arch,omitempty"`
	// Archives pairs each declared architecture with the artifact its build cell uploaded
	// — oci-push's `archives:` input, and the exact complement of Arch: the publish node
	// dropped the arch axis, so it has no cell value to bind and instead publishes every
	// cell's tar at once, as one manifest list per cascade tag. Derived from the PRODUCER's
	// axes when this node consumes an axis it does not fan over. Empty on a project that
	// declares no architecture, where oci-push takes its single-image path unchanged.
	Archives []ArchiveView `json:"archives,omitempty"`
	// ReleaseAssetPath is the forgejo-release `release-asset-path:` input — the
	// UNSUFFIXED binary path resolved from org.projectfile.artifacts (the single
	// kind=binary entry's .path, e.g. dist/pf-cli). The action suffixes it with
	// the cell axes (→ dist/pf-cli-${GOOS}-${GOARCH}) to find the built binary the
	// build→consumer download edge restored to the workspace. The release tag rides
	// PublishVersion (the same ci:version), so this is the only forgejo-release-
	// specific input. EMPTY when the project declares no kind=binary artifact, which
	// the template omits entirely and the action reads as create-only — the release
	// a container-only project mints for its image torrents to attach to.
	ReleaseAssetPath string `json:"release-asset-path,omitempty"`
	BuildArgNames    string `json:"-"`
	FileArgs         string `json:"-"`
	MountsArg        string `json:"-"`
	// PfCliImage is the container-build `pf-cli-image` input: the projectfile/cli ref
	// (registry path + tag) oci-labels.sh reads the projectfile through when the host
	// has no pf-cli. Lowered from the ci.images PF_CLI_IMAGE var like a run-tool image
	// (imageExpr registry path + imageTagExpr tag); empty when the build declares no
	// such var, so the action degrades to host pf-cli or a logged label skip.
	PfCliImage string `json:"pf-cli-image,omitempty"`
	// BuildTarget is the org.projectfile.ci.build-target map (lowering key → Dockerfile
	// stage). The container-build fragment indexes it by the render Target.Key so gha and
	// forgejo each pick their own `--target` (the make plane reads the m6e key itself).
	// Template-only (never serialised); empty/absent => the action omits target => last stage.
	BuildTarget map[string]string `json:"-"`
	// SecretsJSON / SecretsDefaultImage / SecretsImage are the secrets-provision action
	// `with:` inputs (the SYNTHETIC pre-dc-up-d step — see ActionSecretsProvision). They
	// are the cloud half of org.projectfile.ci.secrets, set ONLY on the synthetic step
	// Build injects; no other StepView carries them. SecretsJSON is the RAW declarations
	// subtree (Build.Secrets forwarded verbatim — provision.sh parses the exact pf-cli
	// output). SecretsDefaultImage is the fully-formed misc-tools ref (m6e-secret-* live
	// there). SecretsImage is the project's OWN built image (the `image: self` target),
	// per-cell via the SAME subst the live job computes. Empty-declared-secrets ⇒ the
	// step is not injected at all (no-op invariant), so these stay zero on every real step.
	SecretsJSON         string `json:"secrets-json,omitempty"`
	SecretsDefaultImage string `json:"secrets-default-image,omitempty"`
	SecretsImage        string `json:"secrets-image,omitempty"`
	// Stem is this step's cell-keyed OCI-tar base (container-build artifact-name /
	// oci-push artifact-name). The job pulls/loads it; the action stamps/pushes it.
	Stem string `json:"stem,omitempty"`
	// Emit is the TOOL-level fact emission (ci.Manifest.Emit): one webhook step rendered
	// straight after this step, reporting what the tool published. Non-nil only when the
	// tool declares `emit:` AND the project declares org.projectfile.events. See
	// StepEmitView; the goal-level counterpart is JobView.Emit.
	Emit *StepEmitView `json:"emit,omitempty"`
	// Upload / UploadPath: a generic build-artifact PRODUCER uploads UploadPath under the
	// cell-keyed name Upload right AFTER this step's run (the binary-build hand-off).
	Upload     string `json:"upload,omitempty"`
	UploadPath string `json:"upload-path,omitempty"`
	// Caches are this tool's run-time caches, derived from the NAME entries of
	// Manifest.Mounts (a scanner DB, a pkg cache), from-sorted. The run-tool fragment
	// lowers each to one `mounts` spec (every target) and, on an ephemeral runner, a
	// preceding actions/cache restore step — the per-target host side + restore action
	// come from the Target adapter, so the model stays neutral.
	Caches []Cache `json:"caches,omitempty"`
}

// Gate is the step's `if:`, whichever guard it carries: its OWN (a `when: always`
// teardown) or, on a publish cell, the destination gate publishCells hung on every
// member. The two never coexist — a teardown is exempted from the cell gate — so this
// is a choice, not a conjunction, and neither needs `${{ }}` normalising to reach the
// other. Empty => no `if:`, i.e. the runner's implicit success().
func (s StepView) Gate() string {
	if s.If != "" {
		return s.If
	}
	return s.PublishIf
}

// Cache is one named run-time cache a tool reads (Manifest.Caches): the NAME (the
// host-dir segment and the actions/cache key stem) and the CONTAINER PATH it mounts
// at. Vendor-neutral — the host side and the restore strategy are applied per target
// at render (cacheMounts + the run-tool fragment), never here.
type Cache struct {
	Name string `json:"name"`
	Path string `json:"path"`
	// Mode is the resolved docker `--volume` access opt (`ro`/`rw`) from the mount's
	// mode — RO for a scan that reads the shared DB, RW for a `*-db-update` writer.
	Mode string `json:"mode"`
}

// DownloadView is one artifact a node-job pulls ONCE before its steps run: the
// cell-keyed build OCI tar (shared by every scanner in the node) or a generic
// build-artifact a step consumes (restored to Path; empty Path => cwd).
type DownloadView struct {
	Name string
	Path string
}

// ArchiveView is one architecture's build output: the arch as the projectfile declared
// it, and the artifact its build cell uploaded. One line of oci-push's `archives:` input,
// which is what lets a single publish cell index every architecture into one manifest
// list instead of publishing each under a tag of its own.
type ArchiveView struct {
	Arch string
	Name string
}

// FileRead is one per-cell `file:` build-arg: the build-arg NAME and the repo Path
// (with axis placeholders already substituted to ${{ matrix.<axis> }}). It lowers to
// one `NAME=path` token of the container-build action's file-args input; the action
// reads the path's first data line at build time (Phase 2 — the read moved off the
// workflow into the action).
type FileRead struct {
	Name string
	Path string
}

// JobView is one renderable job — a DAG NODE (Decision 2: node = job, tool = step).
// Its member tools render as ordered Steps; a node with no tools (a pure join) has
// none and renders as an echo GATE. It is also the JSON job-model (template input —
// keep the template dumb; all computation happens in Build).
type JobView struct {
	Name string `json:"name"`
	// Steps are this node's member tools as ordered run-steps. Empty + IsGate => a pure
	// join echo gate; empty + !IsGate never happens. A fused (`live`) node hosts members
	// drawn from several nodes (the others collapse to gates that depend on this one).
	Steps  []StepView `json:"steps,omitempty"`
	Needs  []string   `json:"needs"`            // upstream NODE names (the authored DAG), sorted
	Matrix AxisMap    `json:"matrix,omitempty"` // axes when this node is a CELL (shared by every step)
	// RunsOn overrides the workflow-wide runner for THIS job — set only by archRunners,
	// to the matrix expression that reads the per-cell runner an include row carries.
	// Empty => the job renders the target's `runs-on`, which is every job on a project
	// whose target declares no arch→runner map.
	RunsOn string `json:"runs-on,omitempty"`
	// MaxParallel caps concurrent matrix cells (strategy.max-parallel). 0 => omit (the
	// forge default). Lifted from the owning node; rendered ONLY inside the strategy
	// block, so it is inert on a non-matrix job.
	MaxParallel int `json:"max-parallel,omitempty"`
	// Include is this cell job's strategy.matrix.include rows — the GHA/Forgejo
	// lowering of our matrix.overrides extra per-cell vars, emitted so the forge
	// carries each cell's derived vars (e.g. a per-series LLVM version). Set only for
	// a GLOBAL-matrix cell job (overrides is a global-matrix feature); a per-node
	// matrix cell has none. Render-only: derived from the subtree, joined at Build time.
	Include []MatrixRowView `json:"include,omitempty"`
	// Exclude is this cell job's strategy.matrix.exclude rows — the cells its axes
	// mint that nothing builds (matrix.exclude). Unlike Include it belongs to
	// WHICHEVER matrix the job fans over: a per-node matrix carries its own exclusions,
	// so the rows travel with the axes (resolve.Job) instead of being read off the
	// subtree. Empty => the full grid.
	Exclude []MatrixRowView `json:"exclude,omitempty"`
	// EnvNames is the union of the steps' manifest credential names — what bindEnv
	// resolves against the target's `credentials` overlay at Workflow time (a match →
	// secret ref appended to Env; no match → left to the inherited runner env).
	EnvNames []string `json:"-"`
	// Env is the node-job `env:` block (render-only): the UNION of the steps' env VALUES
	// (matrix axis bindings, container-build var/get/ci values, the image-archive path)
	// plus the credentials bindEnv appends. A run-tool step forwards NAMES from it.
	Env []EnvVar `json:"-"`
	// Downloads are the artifacts the node-job pulls ONCE before its steps (the cell-keyed
	// build OCI tar shared by every scanner, plus any generic build-artifact a step
	// consumes), deduped. Load => `docker load` the cell tar (Stem) into the daemon after
	// the downloads — the fused `live` stack needs the image present for `compose up`
	// (a scanner instead reads the tar as a file via M6E_IMAGE_ARCHIVE, so Load is false).
	Downloads []DownloadView `json:"downloads,omitempty"`
	// ReportsUpload / ReportsPath: ONE diagnostic-report upload for the whole node-job
	// (SARIF/JSON from every scanner member), not one per tool — a single artifact per
	// job is downloadable as one zip instead of a fan-out of per-tool zips. Set when any
	// member tool declares a `reports:` glob; ReportsPath is the whole `reports/` dir so
	// the one upload captures every member's output. Rendered as a trailing upload-artifact
	// step guarded `if: ${{ always() }}` with missing files tolerated, so a fail-closed
	// scan still publishes. Cell-keyed name so per-series cells never collide. Reports
	// never join the build→consumer download edge (see Manifest.Reports).
	ReportsUpload string `json:"reports-upload,omitempty"`
	ReportsPath   string `json:"reports-path,omitempty"`
	Load          bool   `json:"load,omitempty"`
	Stem          string `json:"stem,omitempty"`
	// RmiImage is the run-scoped image ref a Load-ing job stamped into the daemon
	// (via docker load). The node template renders a trailing, always()-guarded
	// `docker rmi -f <RmiImage> || true` so the unique-per-run tag (selfImageTagExpr)
	// is reaped from the runner's docker store at the END of the job — without this,
	// every run accumulates one tagged image forever (the old mutable `latest` tag
	// self-recycled on the next load; the run-unique tag does not). Empty on non-Load
	// jobs (scanners read the tar as a file, never entering the store). Ignored if the
	// tag is not run-scoped (e.g. an external `:tag` ref passes through verbatim).
	RmiImage string `json:"-"`
	// RmNetwork is the live stack's own network (ComposeProjectEnv + composeNetworkSuffix
	// — the SAME name a `network: live` tool joins, so join and reap cannot drift). The
	// node template renders a trailing, always()-guarded `docker network rm` next to the
	// image reap: `down` removes the network only when it can, and a failed/timed-out `up`
	// leaves an endpoint attached long enough for that removal to error out — non-fatally,
	// so the job goes green with an orphan network per run (0 containers, never reused
	// because the name is run-scoped). always(), NOT failure(): a green run whose `down`
	// lost the same race leaks identically. Empty on non-fused jobs (no stack, no network).
	RmNetwork string `json:"-"`
	Class     string `json:"class"`
	// IsGate marks a no-op GATE job materialising a pure-join DAG node (no tools) for a
	// target with only job→job edges (GHA/Forgejo): it runs `echo <node>` and exists
	// only to link the graph, so the emitted `needs:` mirrors the authored DAG. A
	// native-grouping lowering (Tekton, m6e) filters these out. Selects steps/gate.
	IsGate bool `json:"gate,omitempty"`
	// Events is this job's vendor-NEUTRAL trigger predicate (the union of the owning
	// nodes' `when` tokens, see ci.Node.When), sorted+deduped. Empty => the job runs
	// on every event the workflow fires for (today's behaviour). Carried in the model
	// so a Tekton lowering reads the same tokens; the GHA/Forgejo `if:` spelling is
	// composed per target at Workflow time (.If), exactly as credentials/composeImage
	// keep their vendor syntax out of the neutral model. A job UN-gated by ANY owning
	// node is unconditional (empty) — it is needed on some path regardless of event.
	Events []string `json:"when,omitempty"`
	If     string   `json:"-"` // target `if:` expression composed from Events at render time (empty => no gate)
	// Emit marks the SYNTHETIC lifecycle-notify job (the forge half of the events model):
	// a webhook POST of the neutral envelope after every real job. Non-nil => this job
	// renders via steps/emit (no checkout) and its `if:` is the always()+var gate, NOT the
	// event-derived jobIf. nil on every real node-job. See EmitView.
	Emit *EmitView `json:"emit,omitempty"`
	// Concurrency is a JOB-level concurrency block (nil => omit), carried verbatim from
	// the owning node's manifest `concurrency:` — the resolver does not synthesise it. Its
	// use is serialising the publish job so two releases of one image cannot race the
	// shared daemon tag onto a single digest. See JobConcurrencyView.
	Concurrency *JobConcurrencyView `json:"concurrency,omitempty"`
}

// JobConcurrencyView is one job's concurrency block as authored on the node. Group is the
// serialisation key (same group => queue); CancelInProgress mirrors GHA's field — false
// for a publish, which must run to completion rather than be cancelled mid-cascade.
type JobConcurrencyView struct {
	Group            string
	CancelInProgress bool
}

// EmitView carries the data the synthetic notify job stamps into the lifecycle
// envelope: the GOAL name (this workflow's goal) and the forge VAR whose presence
// gates the whole job. The envelope's project/url come from the forge context
// (github.repository / the run URL), status/event are computed from needs.*.result —
// so only these two values are render-time. Field-for-field identical to the m6e-emit
// envelope (the shared contract; a test pins byte parity of the field set).
type EmitView struct {
	Goal       string
	WebhookVar string
}

// Run is the emit job's single shell step: it computes success/failure from the
// aggregated needs results, builds the neutral envelope, and POSTs it with timeout +
// retry + backoff. Built in Go (not the template) because every `${{ }}` expression
// would otherwise collide with Go's own `{{` delimiter. Fail-soft (`|| true`): a
// broken sink never reds the workflow, mirroring the m6e emit script.
func (e EmitView) Run() string {
	// needs.*.result aggregates every needed job; a failure OR a cancellation is a
	// failed run. GHA evaluates the ${{ }} to true/false BEFORE the shell sees it.
	failExpr := "${{ contains(needs.*.result, 'failure') || contains(needs.*.result, 'cancelled') }}"
	return "" +
		"status=success; event=goal.succeeded\n" +
		"if [ \"" + failExpr + "\" = true ]; then\n" +
		"  status=failure; event=goal.failed\n" +
		"fi\n" +
		emitGuard("$event") +
		"payload=$(printf '{\"event\":\"%s\",\"project\":\"%s\",\"goal\":\"%s\",\"status\":\"%s\",\"url\":\"%s\",\"ts\":\"%s\",\"payload\":{}}' \\\n" +
		"  \"$event\" \"${{ github.repository }}\" \"" + e.Goal + "\" \"$status\" \\\n" +
		"  \"${{ github.server_url }}/${{ github.repository }}/actions/runs/${{ github.run_id }}\" \\\n" +
		"  \"$(date -u +%Y-%m-%dT%H:%M:%SZ)\")\n" +
		emitCurl(e.WebhookVar)
}

// StepEmitView is the TOOL-level emission (ci.Manifest.Emit): the FACT that a publish
// produced something, POSTed by one extra step right after the tool that produced it.
// Where EmitView answers "did the goal pass", this answers "what shipped" — the image,
// its whole tag cascade, the content digest and the matrix cell. Those four are what a
// downstream router needs to rebuild the right consumers, and a goal-level event carries
// none of them. Only Event/Goal/WebhookVar are render-time: every payload fact is read
// back at run time from the files the publish action wrote, so the resolver never
// re-derives a ref the action already computed and verified.
type StepEmitView struct {
	Event      string // semantic event name, verbatim from the tool manifest
	Goal       string // the workflow's goal (same value the notify job stamps)
	WebhookVar string // forge var holding the sink URL; empty value => the step no-ops
	// ReleaseOnly withholds the fact on a PREVIEW publish. A downstream router turns
	// ci.image.published into a rebuild dispatch for every consumer of the image, and a
	// preview base is precisely what they must not be rebuilt against. Set on a publish
	// step, so the goal-level "published succeeded" notification still fires — the human
	// signal survives and only the machine-readable rebuild trigger is withheld.
	ReleaseOnly bool
}

// If gates the step on the webhook var AND on everything before it having succeeded —
// an image that failed to push is not a published image. success() is spelled out rather
// than left implicit, so this gate reads next to the notify job's always() one. The cell
// gate (StepView.PublishIf, empty off a publish cell) is conjoined because a SKIPPED
// publish leaves success() true: without it, a withheld destination announces an image
// nothing ever pushed.
func (e StepEmitView) If(gate string) string {
	if gate != "" {
		gate = " && (" + gate + ")"
	}
	if e.ReleaseOnly {
		gate += " && github.ref_type == 'tag'"
	}
	return "${{ success() && vars." + e.WebhookVar + " != ''" + gate + " }}"
}

// Cell is the matrix context as JSON — WHICH cell published. A matrixed publish runs this
// step once per cell, so a consumer that builds several series learns which of its bases
// moved. Off a matrix the context is null, already the JSON the payload wants. It rides
// step env rather than being inlined: an axis value carrying a quote would otherwise
// break the printf that builds the envelope.
func (e StepEmitView) Cell() string { return "${{ toJSON(matrix) }}" }

// CellEnv is the env NAME Cell binds to, and the name Run reads back.
const CellEnv = "M6E_EVENT_CELL"

// Run is the emit step's shell: read back what the publish wrote, wrap it in the neutral
// envelope, POST it. The image and the digest come from ONE file — oci-push writes the
// full `repo@sha256:…` ref it verified, so splitting that beats recomposing a ref the
// resolver can only guess at. The tag cascade comes from its own file for the same
// reason: the semver fan-out is the action's logic and must not be reimplemented here.
func (e StepEmitView) Run() string {
	return "" +
		emitGuard(e.Event) +
		"ref=$(cat \"${M6E_DIGEST_FILE:-image.digest}\")\n" +
		"payload=$(printf '{\"event\":\"%s\",\"project\":\"%s\",\"goal\":\"%s\",\"status\":\"success\",\"url\":\"%s\",\"ts\":\"%s\",\"payload\":{\"image\":\"%s\",\"tags\":%s,\"digest\":\"%s\",\"cell\":%s}}' \\\n" +
		"  \"" + e.Event + "\" \"${{ github.repository }}\" \"" + e.Goal + "\" \\\n" +
		"  \"${{ github.server_url }}/${{ github.repository }}/actions/runs/${{ github.run_id }}\" \\\n" +
		"  \"$(date -u +%Y-%m-%dT%H:%M:%SZ)\" \\\n" +
		"  \"${ref%@*}\" \"$(cat \"${M6E_TAGS_FILE:-image.tags}\")\" \"${ref#*@}\" \"${" + CellEnv + ":-null}\")\n" +
		"echo \"events emitting event=" + e.Event + " ref=${ref}\"\n" +
		emitCurl(e.WebhookVar)
}

// emitGuard is the fail-soft preamble BOTH emitters share. The POST ends in `|| true`,
// which cannot tell a missing curl (exit 127) from a network failure, so without this an
// image without curl would go silent under a green check. Name the degradation instead,
// the wording m6e-emit already uses for its own sinks.
func emitGuard(event string) string {
	return "" +
		"if ! command -v curl > /dev/null 2>&1; then\n" +
		"  echo \"webhook degraded — curl absent (event=" + event + ")\"\n" +
		"  exit 0\n" +
		"fi\n"
}

// emitCurl is the POST both emitters end on: timeout, three retries with backoff, and
// fail-soft. A broken sink never reds a workflow. The URL is never logged.
func emitCurl(webhookVar string) string {
	return "" +
		"curl --silent --show-error --location \\\n" +
		"  --max-time 10 --retry 3 --retry-delay 2 --fail-with-body \\\n" +
		"  --header 'content-type: application/json' \\\n" +
		"  --request POST --data \"$payload\" \\\n" +
		"  \"${{ vars." + webhookVar + " }}\" || true"
}

// emitIf gates the notify job: it runs after every needed job regardless of their
// result (always()), but ONLY when the webhook var is configured — so an opted-in
// repo with no var set (the secure default) emits nothing.
func emitIf(webhookVar string) string {
	return "${{ always() && vars." + webhookVar + " != '' }}"
}

// ClassGate is the Class of a materialised node GATE — distinct from the resolve
// tool classes (source/cell/join) so a consumer of the JSON model can tell a
// structural vertex from a real tool job.
const ClassGate = "gate"

// command is the full shell line: the entrypoint plus any resolved args.
func command(run, args string) string {
	if args == "" {
		return run
	}
	return run + " " + args
}

// A tool that ships an `image:` does NOT lower to a job `container:` — a job
// container on a Forgejo/GHA runner must ship Node (the runner execs the JS
// `checkout`/`download-artifact` actions inside it) AND a keep-alive shell (the
// runner holds the container with `entrypoint=["tail","-f","/dev/null"]` and runs
// `run:` via `sh -c`); a minimal `d9t/*-tools` image has no Node and a distroless
// tool (`hadolint`) has neither, so both classes die before the tool ever runs.
// Instead the step runs on the HOST (which HAS Node/git/docker) and reaches its
// image through the versioned `projectfile/actions/run-tool@v1` action (the
// cloud analogue of m6e's M6E_RUN, declarative — no inline `docker run`, Law 2).
// The action takes the image as TWO inputs (image path + version) and composes
// the ref. Registry threading is DROPPED (agnostic revolution): every consumed tool
// image is now a ci.images var NAME whose value already carries the registry prefix.
// The run-tool action receives:
//
//   - image: the nested CI expression `${{ vars.TOOL || vars.FAMILY || 'path' }}`
//     (for var-name tools) or the bare path (for deferred path tools);
//   - version: the single ImageTagVar expression — one runtime var flips dev↔latest;
//   - NO registry input — it is part of the var value or the literal path.
//
// A `:tag`-bearing external ref (`hadolint/hadolint:v2.14.0`) still SPLITS at `:` so
// the image half and the pinned tag version round-trip correctly through the action.

func runToolImage(image string) string {
	if i := strings.IndexByte(image, ':'); i >= 0 {
		return image[:i]
	}
	return image
}

func runToolVersion(image string) string {
	if i := strings.IndexByte(image, ':'); i >= 0 {
		return image[i+1:]
	}
	return imageTagExpr()
}

// Command, RunTool* and EnvArg make a StepView the unit the step partials render.
func (s StepView) Command() string      { return command(s.Run, s.Args) }
func (s StepView) RunToolImage() string { return runToolImage(s.Image) }

// RunToolVersion is the run-tool `version:` input. A HARD-pinned external tag (PinnedTag,
// set by toolImageParts) wins — it must round-trip verbatim, not collapse to the flip var.
// Otherwise the tag is split off Image (the deprecated literal-manifest form still carries
// its own `:tag`) or defaults to the mutable dev↔latest flip expr (the workspace default).
func (s StepView) RunToolVersion() string {
	if s.PinnedTag != "" {
		return s.PinnedTag
	}
	return runToolVersion(s.Image)
}

// mutableTags are the literal image tags that MOVE (a fresh push reuses the name), so a
// runner must re-pull them each run. The flip var's two runtime values live here; the flip
// EXPRESSION itself is caught by the `${{` test below. Any OTHER literal (`v2.14.0`, a
// digest, a dated tag) is treated as an immutable pin.
var mutableTags = map[string]bool{"latest": true, "dev": true, "edge": true}

// RunToolPull is the run-tool `pull:` input (RUN_TOOL_PULL): "missing" for an IMMUTABLE
// pinned tag (fetch once, then reuse — re-checking an unmovable digest each run only spends
// registry pull budget, the Docker-Hub rate-limit trap), "" for a MUTABLE tag so the action
// keeps its `always` default and never runs a stale freshly-pushed image. A version is
// mutable when it is a `${{ }}` expression (the dev↔latest flip var) or a known moving
// literal (latest/dev/edge); everything else is a pin. Covers BOTH the var-name and the
// literal-manifest image forms.
func (s StepView) RunToolPull() string {
	// The project's own image was `docker load`ed from this run's build artifact and is
	// published nowhere, so any fetch is a guaranteed failure, not a freshness check.
	if s.SelfImage {
		return "never"
	}
	v := s.RunToolVersion()
	if v == "" || strings.Contains(v, "${{") || mutableTags[v] {
		return ""
	}
	return "missing"
}

// ExecContainer is the container-exec action's `container:` input: a reference to
// the live-stack instance the build→live edge derived onto the job env (the
// ContainerInstanceEnv contract var). The action `docker exec`s into it; the value
// rides the job env (per-cell `ci-<basename>`), so the model names no container.
func (s StepView) ExecContainer() string { return "${{ env." + ContainerInstanceEnv + " }}" }

// liveNetwork is the symbolic Manifest.Network value meaning "the live compose stack's
// network"; composeNetworkSuffix mirrors the m6e/container pipeline.yaml convention that
// names that network <project>-network. This is the ONE place the resolver must know that
// naming, so a `network: live` tool can JOIN the stack dc-up-d brought up (companion of
// `fuse: live`, which co-locates the tool into the same job).
//
// liveFuse is that companion: the ONE fuse group whose members need the built image in the
// runner's image store, because compose runs it. Every other group (`publish`, `lint`) acts
// on the archive or on a registry ref, so naming the group here is what keeps `docker load`
// off their jobs.
const (
	liveNetwork          = "live"
	liveFuse             = "live"
	composeNetworkSuffix = "-network"
)

// RunToolNetwork is the run-tool `network:` input. `live` lowers to the stack's own
// ${M6E_COMPOSE_PROJECT_NAME}-network — read off the fused live job's env so the name
// matches whatever the pipeline compose file created THIS run (per-run unique identity).
// Empty stays empty (the default bridge). Any other value is an explicit network name,
// forwarded verbatim.
func (s StepView) RunToolNetwork() string {
	switch s.Network {
	case "":
		return ""
	case liveNetwork:
		return "${{ env." + ComposeProjectEnv + " }}" + composeNetworkSuffix
	default:
		return s.Network
	}
}

// EnvArg is the whitespace NAME list this step forwards (matrix axes + image-archive
// path, then its credential names), insertion-ordered (env values first, then cred
// names), deduped. It is the NAME source envPairs resolves to NAME=VALUE at Workflow
// time (EnvForward) — a composite action can't read the job env: as process env, so the
// values must ride the action input, not just the names.
func (s StepView) EnvArg() string {
	seen := map[string]bool{}
	var names []string
	add := func(n string) {
		if !seen[n] {
			seen[n] = true
			names = append(names, n)
		}
	}
	for _, e := range s.Env {
		add(e.Key)
	}
	for _, n := range s.EnvNames {
		add(n)
	}
	return strings.Join(names, " ")
}

// artifactStem is the cell-keyed artifact name: a base token plus every matrix-axis
// binding, so each cell's artifact is distinct and a producer/consumer in the SAME
// cell compute the IDENTICAL name. The image hand-off keys on base "image"; the
// generic build-artifact hand-off keys on the PRODUCER tool's name (the consumer
// knows the producer via the DAG edge, so it recomputes the same name).
// artifactScopeSuffix binds the name to THIS run (run_id only): two runs recompute the
// same cell-keyed stem, and Forgejo's artifact store (v1 protocol) has been observed
// serving a same-named artifact from a PRIOR run instead of the one this run uploaded.
// NOT run_attempt — a partial re-run leaves a succeeded producer's artifact at the old
// attempt while the re-running consumer asks for the new one (see artifactScopeSuffix).
func artifactStem(base string, m AxisMap) string {
	s := base
	for _, a := range m {
		s += "-${{ matrix." + a.Key + " }}"
	}
	s += artifactScopeSuffix
	return s
}

// archVarExpr binds a step to the M6E_ARCH axis when its JOB actually fans over it,
// and to nothing otherwise. Read off the job's axes rather than the declaration so a
// node that DROPS the axis (the host-arch live test) reports no arch by construction,
// with no second rule to keep in step with the first.
func archVarExpr(axes []ci.Axis) string {
	for _, a := range axes {
		if a.Key == ci.ArchAxis {
			return matrixVarExpr(ci.ArchAxis, nil, nil)
		}
	}
	return ""
}

// declaredArches returns the architecture set ci.Load minted the arch axis from, or nil
// when the project declared none. It reads the SUBTREE's axes, which is the complement
// of archVarExpr above: a node that drops the axis to run once still has to name every
// arch it is indexing, and its own axes no longer carry them.
func declaredArches(st *ci.Subtree) []string {
	for _, a := range st.Axes {
		if a.Key == ci.ArchAxis {
			return a.Values
		}
	}
	return nil
}

// archAxis reports whether these axes fan over architecture.
func archAxis(axes []ci.Axis) bool {
	for _, a := range axes {
		if a.Key == ci.ArchAxis {
			return true
		}
	}
	return false
}

// archArtifactStem is artifactStem with the arch axis bound to a LITERAL value rather
// than a matrix expression. A node that dropped the axis still has to name the artifact
// each producer cell uploaded, and the producer named it by fanning over the axis this
// node no longer has — so the name is built from the PRODUCER's axes, in the producer's
// own (key-sorted) order, which is what keeps the two ends from drifting.
func archArtifactStem(base string, producer []ci.Axis, arch string) string {
	s := base
	for _, a := range producer {
		if a.Key == ci.ArchAxis {
			s += "-" + arch
			continue
		}
		s += "-${{ matrix." + a.Key + " }}"
	}
	return s + artifactScopeSuffix
}

// artifactStems names every artifact ONE consumer job has to download from ONE
// producer. The stem is the PRODUCER's to name — it uploaded under its own axes —
// so every axis is read off the producer and never off the consumer, which is what
// lets the two ends fan differently at all. An axis the consumer ALSO carries stays
// a matrix expression (both ends sit in the same cell); an axis the consumer DROPPED
// binds to each realised value instead, because a job that stopped fanning takes
// every cell's artifact at once. Equal axes therefore reproduce artifactStem exactly,
// so a producer/consumer pair that fans identically renders byte-for-byte as before.
// Cells is the one definition of which cells exist, so an `exclude`d row is asked for
// by nobody.
func artifactStems(base string, producer []ci.Axis, excludes []ci.Exclusion, consumer []ci.Axis) []string {
	kept := make(map[string]bool, len(consumer))
	for _, a := range consumer {
		kept[a.Key] = true
	}
	dropped := false
	for _, a := range producer {
		if !kept[a.Key] {
			dropped = true
			break
		}
	}
	// Nothing dropped covers BOTH the equal-axes case and a producer that fans
	// NARROWER than its consumer (the single release torrent five release cells
	// each attach) — one name either way, built from the producer's axes.
	if !dropped {
		return []string{artifactStem(base, AxisMap(producer))}
	}
	var out []string
	seen := make(map[string]bool)
	for _, cell := range ci.Cells(producer, excludes) {
		s := base
		for _, a := range producer {
			if kept[a.Key] {
				s += "-${{ matrix." + a.Key + " }}"
				continue
			}
			s += "-" + cell[a.Key]
		}
		s += artifactScopeSuffix
		// Cells differing ONLY in an axis the consumer kept collapse to one name —
		// that cell resolves it through its own matrix binding.
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// stepCtx is the data a JOB partial (steps/node or steps/gate) renders against: the
// per-vendor adapter tokens plus the one node-job being lowered.
type stepCtx struct {
	Target Target
	Job    JobView
}

// fragCtx is the data a STEP fragment (frag/run, frag/run-tool, providers/<action>)
// renders against: the adapter tokens plus the one member tool (no checkout — the
// node-job already did it).
type fragCtx struct {
	Target Target
	Step   StepView
}

// Model is the JSON job-model — the documented template input. It is the
// vendor-NEUTRAL output of `pf-ci resolve`; the deployment overlay (PlatformView)
// is target-specific and is therefore joined in only at Workflow time, never
// serialised here (other targets must be able to ignore it).
type Model struct {
	// Name is the workflow name AND the per-goal filename stem: the goal node's name
	// when the subtree is pinned to a single goal (ForGoal — the one-file-per-goal
	// path), else "ci" for the combined neutral model (`pf-ci resolve`). Used verbatim,
	// no case munging (Principle of Least Astonishment).
	Name string    `json:"name"`
	Jobs []JobView `json:"jobs"`
	// Env is the workflow-level `env:` block: NAME=VALUE lines every job inherits.
	// Composed in Build from the subtree's neutral env with the SAME make->forge
	// lowering job env uses (lowerMakeExpr), so a `${REGISTRY}` ref rides a
	// ${{ vars.· }} expression here too. Key-sorted for a byte-stable render. nil => none.
	Env []KV `json:"env,omitempty"`
	// On is the resolved `on:` trigger surface for this file: the GOAL's own `when` +
	// schedule/dispatch when pinned to one goal, else the union over all jobs (combined
	// model). Composed here (Build holds the subtree) so Workflow stays a dumb template.
	On OnView `json:"-"`
	// Triggers is the neutral workflow-level trigger surface (manual dispatch + cron) of
	// the pinned goal, carried so `pf-ci resolve` emits it and every target lowers the
	// same data. nil => push/PR only. The vendor `on:` spelling lives in On above.
	Triggers *TriggersView `json:"triggers,omitempty"`
	// PrimaryBranches is the branch set a push must NOT be on for the `preview` token to
	// fire (eventExpr). Resolved from the document at Build time and carried here so
	// Workflow stays a dumb template — every target lowers the same neutral set. Never
	// empty: primaryBranches applies ci.DefaultPrimaryBranches when the document records
	// no default branch, which is every project in the fleet today.
	PrimaryBranches []string `json:"primary-branches,omitempty"`
	// Err is a fail-fast Build defect that has no place in the rendered output: a node
	// that mixes incompatible matrix axes, or a tool multi-homed across unrelated nodes
	// (constraints #1/#3 of node=job). Build cannot return an error without churning
	// every caller, so it stashes it here; Workflow refuses to render a defective model.
	Err error `json:"-"`
}

// KV is one rendered `key: value` line (workflow-level permissions), sorted by
// key for byte-stable output.
type KV struct{ Key, Value string }

// PlatformView is the resolved per-target deployment overlay the template paints
// onto the workflow: a default runner (override or adapter default), an optional
// per-job timeout, and optional workflow-level permissions/concurrency. Empty
// fields render nothing — a project with no overlay produces exactly today's
// workflow (graceful degradation; the overlay is purely additive).
type PlatformView struct {
	RunsOn         string // already-formatted runner: a bare label or a YAML flow list
	TimeoutMinutes int    // per-job timeout-minutes (0 => omit)
	Permissions    []KV   // workflow-level permission scopes, key-sorted
	Concurrency    *ConcurrencyView
}

// ConcurrencyView is the workflow-level concurrency block (nil => omit).
type ConcurrencyView struct {
	Group            string
	CancelInProgress bool
}

// buildPlatform resolves the overlay for one target. The runner falls back to the
// adapter default when the overlay does not set runs-on, so the existing tests
// (no overlay) keep emitting `runs-on: <target default>`.
func buildPlatform(target Target, p ci.Platform) PlatformView {
	pv := PlatformView{
		RunsOn:         defaultRunner(target, p),
		TimeoutMinutes: p.TimeoutMinutes,
	}
	if len(p.RunsOn) > 1 {
		pv.RunsOn = funcs["yamlList"].(func([]string) string)(p.RunsOn)
	}
	for _, k := range sortedKeys(p.Permissions) {
		pv.Permissions = append(pv.Permissions, KV{Key: k, Value: p.Permissions[k]})
	}
	if c := p.Concurrency; c != nil {
		pv.Concurrency = &ConcurrencyView{Group: c.Group, CancelInProgress: c.CancelInProgress}
	}
	return pv
}

// defaultRunner is the ONE label an unmapped arch cell falls back to: the overlay's
// `default` when it names one, the adapter default otherwise. The multi-label list
// form can never reach here — it is a different JSON shape of the same key than the
// object form arch routing needs, so the two spellings cannot coexist on one target.
func defaultRunner(target Target, p ci.Platform) string {
	if len(p.RunsOn) == 1 {
		return p.RunsOn[0]
	}
	return target.RunsOn
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// fuseGroups implements the CO-LOCATION law: leaves that share a `fuse` group name
// and are connected by `needs` collapse to ONE job at the connected component's
// sink, which runs each member's command as an ordered step. On an ephemeral cloud
// runner a stateful runtime (a compose stack) cannot span jobs — GHA `services:`
// are job-scoped — so a `fuse: live` group's up→test MUST run inside a single job;
// the SAME DAG stays separate recipes for a daemon-native target (m6e), where the
// daemon persists between targets anyway. The law names no node or goal — it reads
// only the `fuse` value and the needs-edges — so any project's region fuses
// identically. A singleton group is a harmless no-op (no fusion, no steps).
//
// It returns the ABSORBED leaves (suppressed as standalone jobs); per surviving
// sink, that job's fused needs (the union of the component's needs minus the
// component itself, so INTERNAL edges vanish while CROSS-group edges — the
// build→live tar hand-off — survive); and per sink the ORDERED member names
// (dependencies first, sink last) the render lowers to sequential run-steps.
func fuseGroups(rm *resolve.Model, st *ci.Subtree) (absorbed map[string]bool, fusedNeeds map[string][]string, ordered map[string][]string) {
	absorbed = map[string]bool{}
	fusedNeeds = map[string][]string{}
	ordered = map[string][]string{}

	// Group the running jobs by their (non-empty) fuse group name.
	byFuse := map[string][]resolve.Job{}
	for _, j := range rm.Jobs {
		if g := st.Tools[j.Name].Fuse; g != "" {
			byFuse[g] = append(byFuse[g], j)
		}
	}
	for _, group := range byFuse {
		member := map[string]bool{}
		for _, j := range group {
			member[j.Name] = true
		}
		// edge: two members are fused-adjacent iff one needs the other (undirected).
		edge := func(a, b resolve.Job) bool {
			return needs(a, b.Name) || needs(b, a.Name)
		}
		seen := map[string]bool{}
		for _, root := range group {
			if seen[root.Name] {
				continue
			}
			// Flood-fill the connected component containing root.
			comp := []resolve.Job{}
			stack := []resolve.Job{root}
			for len(stack) > 0 {
				j := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				if seen[j.Name] {
					continue
				}
				seen[j.Name] = true
				comp = append(comp, j)
				for _, k := range group {
					if !seen[k.Name] && edge(j, k) {
						stack = append(stack, k)
					}
				}
			}
			if len(comp) < 2 {
				continue // singleton group — nothing to fuse
			}
			// Sink = a member no OTHER member needs. A chain has exactly one; a
			// diamond may have several — pick the sorted-first deterministically and
			// fuse the rest into it (one runtime ⇒ one job either way).
			var sinks []string
			for _, j := range comp {
				neededInside := false
				for _, k := range comp {
					if k.Name != j.Name && needs(k, j.Name) {
						neededInside = true
						break
					}
				}
				if !neededInside {
					sinks = append(sinks, j.Name)
				}
			}
			sort.Strings(sinks)
			sink := sinks[0]
			// Fused needs: union of the component's needs, minus the component.
			seenNeed := map[string]bool{}
			var fused []string
			for _, j := range comp {
				if j.Name != sink {
					absorbed[j.Name] = true
				}
				for _, n := range j.Needs {
					if !member[n] && !seenNeed[n] {
						seenNeed[n] = true
						fused = append(fused, n)
					}
				}
			}
			sort.Strings(fused)
			fusedNeeds[sink] = fused
			ordered[sink] = topoOrder(comp)
		}
	}
	return absorbed, fusedNeeds, ordered
}

// topoOrder linearises a fused component dependencies-first (sink last): a member
// is emitted only after every member it `needs` inside the component. Ties break
// on name for byte-stable output. The result is the run-step order — `dc-up-d`
// before `container-test` because the test needs the stack up.
func topoOrder(comp []resolve.Job) []string {
	name := make(map[string]resolve.Job, len(comp))
	for _, j := range comp {
		name[j.Name] = j
	}
	var order []string
	placed := map[string]bool{}
	for len(order) < len(comp) {
		// Among unplaced members whose internal deps are all placed, take the
		// sorted-first — a deterministic Kahn step over the small component.
		var ready []string
		for _, j := range comp {
			if placed[j.Name] {
				continue
			}
			blocked := false
			for _, n := range j.Needs {
				if _, inside := name[n]; inside && !placed[n] {
					blocked = true
					break
				}
			}
			if !blocked {
				ready = append(ready, j.Name)
			}
		}
		sort.Strings(ready)
		placed[ready[0]] = true
		order = append(order, ready[0])
	}
	return order
}

// toolStep lowers ONE tool into a StepView — the per-tool computation shared by every
// node-job (Decision 2: tool = step). It carries what a standalone tool job used to:
// the command (a bare host run, or the run-tool action when an image ships — decided
// at render by .Image), an `action:` dispatch (container-build/oci-push) with its
// clean `with:` inputs, the cell-keyed env (matrix axis bindings + container-build arg
// VALUES), the credential NAMES it forwards, the per-cell artifact Stem, and a generic
// build-artifact upload. The image-archive env + the shared downloads/`docker load`
// are JOB concerns the node loop adds; this function never touches the job header.
//
// dispatchArgs is the goal-scoped set of build-arg NAMES this file exposes as
// workflow_dispatch inputs (dispatchBuildArgs); a container-build arg in it lowers to
// ${{ inputs.NAME || vars.NAME || 'default' }} so a manual run overrides it. Empty for a
// file without the `dispatch:{build-args:true}` flag — the arg lowers exactly as before.
func toolStep(j resolve.Job, st *ci.Subtree, b *ci.Build, dispatchArgs map[string]bool) StepView {
	man := st.Tools[j.Name]
	run := man.Run
	if run == "" {
		// Neutral default: the bare target name (the leaf is a named target); the
		// resolver does NOT synthesise an `auto-<name>` prefix (a D9T manifest convention).
		run = j.Name
	}
	// When `image:` is a ci.images VAR NAME (uppercase identifier), resolve it to its
	// forge-plane registry path (no per-tool override tier); otherwise pass through as a
	// literal (deferred backward-compat for old registry-path form).
	img := man.Image
	var pin string
	var self bool
	switch {
	// ImageFullnameEnv names the project's OWN built image, which the fused live job
	// exports as job env — NOT a ci.images entry. Without this case it falls through to
	// toolImageParts as an orphan var and lowers to an undefined ${{ vars.… }}, the same
	// trap ImagePublishedEnv is stripped from the `env:` forwards to avoid.
	case img == ImageFullnameEnv:
		img, self = EnvRef(ImageFullnameEnv), true
	case imageVarName.MatchString(img):
		img, pin = toolImageParts(img, b)
	}
	step := StepView{
		Name:      j.Name,
		Run:       run,
		Args:      j.Args,
		Image:     img,
		PinnedTag: pin,
		SelfImage: self,
		Action:    man.Action,
		Advisory:  man.Advisory,
		Network:   man.Network,
		Stem:      artifactStem("image", AxisMap(j.Axes)),
		Arch:      archVarExpr(j.Axes),
	}
	// A `when: always` tool is a teardown member (e.g. the fused live job's compose
	// `down`): guard its step so it runs even when an earlier step in the same job
	// failed. always() is GHA/Forgejo-compatible, so it stays a neutral render const.
	if man.When == ci.StepWhenAlways {
		step.If = stepAlwaysExpr
	}
	// ImagePublishedEnv is a resolver-COMPUTED contract var, NOT a forwarded credential:
	// strip it from the forwarded-name set here (else it lowers to an undefined
	// ${{ vars.M6E_IMAGE_PUBLISHED }}) and inject its computed per-cell value below, once
	// the axis substitution is in scope.
	var wantsPublishedImage bool
	if len(man.Env) > 0 {
		names := make([]string, 0, len(man.Env))
		for _, n := range man.Env {
			if n == ImagePublishedEnv {
				wantsPublishedImage = true
				continue
			}
			names = append(names, n)
		}
		sort.Strings(names)
		step.EnvNames = dedupe(names)
	}
	// Run-tool cache mounts: a plain tool's NAME `mounts` entries (a scanner DB, a pkg
	// cache) lower to run-tool cache mounts (+ an actions/cache restore on an ephemeral
	// runner). A real-PATH entry is m6e-only (a host socket / cert store) and is SKIPPED
	// here — an ephemeral runner has no such dir. Container-build actions take their
	// mounts as build-context (below), so this run-tool lowering excludes them. From-
	// sorted so the rendered mounts/restore steps are byte-stable.
	if man.Action == "" && len(man.Mounts) > 0 {
		caches := make([]ci.Mount, 0, len(man.Mounts))
		for _, m := range man.Mounts {
			if !m.IsPath() {
				caches = append(caches, m)
			}
		}
		sort.Slice(caches, func(i, j int) bool { return caches[i].From < caches[j].From })
		for _, m := range caches {
			step.Caches = append(step.Caches, Cache{Name: m.From, Path: m.To, Mode: m.VolOpt()})
		}
	}
	// Matrix axis bindings: the VALUE each run-tool step forwards and the node-job lifts.
	// subst is the {placeholder} key set (axes + matrix.overrides extra vars) used wherever
	// an image basename / file path / envset value carries a {KEY} token; extras is the
	// sorted extra-var NAMES a cell carries (cell-only — a non-cell job has no matrix to
	// bind them). An extra var behaves exactly like an axis at render: a ${{ matrix.<NAME> }}
	// binding the cell resolves, and a skip from build-arg auto-injection (the cell passes it).
	// argDefaults/partial backstop a PARTIAL override (a var some cell leaves unset) so an
	// un-decorated cell keeps the build-arg default instead of an empty matrix ref.
	subst := substKeys(j.Axes, st)
	argDefaults := buildArgDefaults(b)
	partial := st.PartialExtraVars()
	var extras []string
	for _, a := range j.Axes {
		step.Env = append(step.Env, EnvVar{Key: a.Key, Value: "${{ matrix." + a.Key + " }}"})
	}
	if len(j.Axes) > 0 {
		for k := range st.ExtraVarKeys() {
			extras = append(extras, k)
		}
		sort.Strings(extras)
		for _, k := range extras {
			step.Env = append(step.Env, EnvVar{Key: k, Value: matrixVarExpr(k, partial, argDefaults)})
		}
	}
	// Authored literal env (EnvSet): vendor-neutral name->value the tool sets on its
	// job env, axis-templated per cell (substAxes). The compose-runtime vars a fused
	// live job needs (COMPOSE_FILE, the project/instance names). Key-sorted for a
	// byte-stable render.
	if len(man.EnvSet) > 0 {
		keys := make([]string, 0, len(man.EnvSet))
		for k := range man.EnvSet {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			step.Env = append(step.Env, EnvVar{Key: k, Value: substAxes(man.EnvSet[k], subst)})
		}
	}
	// Inject the resolver-computed published-image ref for a scanner that requested it
	// (ImagePublishedEnv). Env carries the no-route FALLBACK; PullRefs carries the
	// composed destination per lowering, which auditTarget binds once the target is
	// known — a StepView is shared across targets, so the per-lowering value cannot be
	// decided here.
	if wantsPublishedImage {
		step.Env = append(step.Env, EnvVar{Key: ImagePublishedEnv, Value: publishedImageRef(st.Image, subst)})
		if b != nil && len(b.PullRefs) > 0 {
			step.PullRefs = map[string]string{}
			for lowering, sr := range b.PullRefs {
				// Per-cell: a composed ref carries `{AXIS}` verbatim, because
				// composition never touches a token with no `$`. The same
				// substitution the publish refs get, so a matrix cell audits its own
				// series rather than a placeholder no registry holds.
				step.PullRefs[lowering] = substAxes(sr.Ref, subst)
			}
		}
	}
	// The publish plane: the action that puts THIS build on a registry, whether that is
	// one image or a manifest list over every declared architecture.
	publishes := man.Action == ActionOciPush
	// Both ends of the image lifecycle need the project basename (per-cell ref): the
	// build PRODUCER stamps it into the OCI archive, the publish CONSUMER re-tags to it.
	if man.Action == ActionContainerBuild || publishes {
		step.ImageBasename = substAxes(st.Image, subst)
	}
	// container-build reads the projectfile for its labels via pf-cli; a hostexecutor
	// runner has none, so it takes the projectfile/cli image (PF_CLI_IMAGE var) as an
	// input. Empty when the build declares no such var — the action then degrades.
	if man.Action == ActionContainerBuild {
		step.PfCliImage = pfCliImageRef(b)
		// Per-lowering Dockerfile stage: the fragment picks build-target[Target.Key].
		if b != nil {
			step.BuildTarget = b.BuildTarget
		}
	}
	// Both publish actions get the git tag (ci:version) so each can lower it into the
	// SAME semver tag cascade (latest / major / minor / patch) — the index has to reach
	// every tag the per-arch images took, or `latest` stays a single-arch image. Same
	// expr as the M6E_VERSION build-arg — the version lives in ONE place (ciContextExpr).
	if publishes {
		step.PublishVersion = ciContextExpr[ci.CIKeyVersion]
		step.PublishPreview = ciContextExpr[ci.CIKeyPreview]
		// Per-cell: a composed ref carries `{AXIS}` verbatim, because composition
		// never touches a token with no `$`. The same substitution the basename
		// above gets, so a matrix cell publishes its own series to every sink.
		if b != nil && len(b.PublishRefs) > 0 {
			step.PublishRefs = map[string][]ci.SinkRef{}
			for lowering, refs := range b.PublishRefs {
				for _, sr := range refs {
					step.PublishRefs[lowering] = append(step.PublishRefs[lowering],
						ci.SinkRef{Sink: sr.Sink, Ref: substAxes(sr.Ref, subst)})
				}
			}
		}
	}
	// The tool-level fact emission, opted into TWICE: the tool names the event, and the
	// project declares org.projectfile.events (the block holding the webhook var). Either
	// one absent renders no step at all — the same secure default the notify job keeps.
	// Goal is stamped later, in Build, where the workflow's goal name is known.
	if man.Emit != "" && b != nil && b.Events != nil {
		step.Emit = &StepEmitView{Event: man.Emit, WebhookVar: b.Events.WebhookVar, ReleaseOnly: publishes}
	}
	// forgejo-release gets the SAME git tag (ci:version) as oci-push, PLUS the
	// resolved binary path (Manifest.ReleaseAssetPath, looked up Load-side from
	// org.projectfile.artifacts). The action suffixes the path per cell and
	// attaches it; the tag is the release title + tag. Both ride `with:` inputs.
	if man.Action == ActionForgejoRelease {
		step.PublishVersion = ciContextExpr[ci.CIKeyVersion]
		step.ReleaseAssetPath = man.ReleaseAssetPath
		// No axis substitution, unlike a sink ref: a forge URL and a repository path
		// are properties of the DESTINATION, not of the build cell.
		if b != nil && len(b.ReleaseTargets) > 0 {
			step.ReleaseTargets = b.ReleaseTargets
		}
	}
	// container-build args — NAMES ride build-args, VALUES the step env (the action
	// forwards by name); a file: arg rides file-args (the action reads it); an axis
	// name is skipped (the cell passes it). Explicit manifest args take priority;
	// org.projectfile.build.args entries are auto-injected for the rest, mirroring the
	// make reader's --build-arg auto-emit (run-images in ci.images are NOT build inputs).
	if man.Action == ActionContainerBuild {
		names := make([]string, 0, len(j.Axes)+len(man.Args)+len(extras))
		axis := make(map[string]bool, len(j.Axes)+len(extras))
		for _, a := range j.Axes {
			axis[a.Key] = true
			names = append(names, a.Key)
		}
		// matrix.overrides extra vars are matrix-passed like axes (the cell carries
		// them), so they join the skip set AND the forwarded names list — their VALUE
		// is the ${{ matrix.<NAME> }} binding added above, not a vars-store lookup.
		for _, k := range extras {
			axis[k] = true
			names = append(names, k)
		}
		// Explicit args first — they override any auto-derived entry of the same name.
		seen := make(map[string]bool, len(man.Args))
		var fileArgs []string
		for _, ba := range man.Args {
			if axis[ba.Name] {
				continue
			}
			seen[ba.Name] = true
			if ba.Source == ci.SourceFile {
				_, fr := lowerBuildArg(ba, subst)
				fileArgs = append(fileArgs, fr.Name+"="+fr.Path)
				continue
			}
			value, _ := lowerBuildArg(ba, subst)
			step.Env = append(step.Env, EnvVar{Key: ba.Name, Value: value})
			names = append(names, ba.Name)
		}
		// Auto-inject every org.projectfile.build.args input not already declared
		// explicitly — the Dockerfile ARGs (FROM refs, versions) container-build needs,
		// eliminating per-image redeclaration in projectfiles. A file: input rides
		// file-args (per-cell read); a literal default rides ${{ vars.NAME || 'default' }};
		// an empty default rides bare ${{ vars.NAME }} (forward only when set); an
		// axis-named OR extra-var-named input is skipped (the cell passes it).
		if b != nil {
			// The matrix-key set (axes + extra vars) so a ${NAME} embedded in a composed
			// default lowers to ${{ matrix.NAME }} when NAME is cell-defined — e.g. the
			// ${B19_LLVM_SERIES} inside a FROM ref when LLVM is a matrix.overrides extra var.
			// argDefaults (hoisted above) supplies the literal fallback a lowered ${NAME}
			// keeps (e.g. ${B19_UBUNTU_SERIES} -> ${{ matrix... || vars... || 'resolute' }}).
			matrixSet := make(map[string]bool, len(subst))
			for _, a := range subst {
				matrixSet[a.Key] = true
			}
			for _, bi := range b.Args {
				if axis[bi.Name] || seen[bi.Name] {
					continue
				}
				if bi.File != "" {
					fileArgs = append(fileArgs, bi.Name+"="+substAxes(bi.File, subst))
					continue
				}
				step.Env = append(step.Env, EnvVar{Key: bi.Name, Value: buildInputValue(bi, b, argDefaults, matrixSet, partial, dispatchArgs)})
				names = append(names, bi.Name)
			}
		}
		sort.Strings(names)
		step.BuildArgNames = strings.Join(names, " ")
		sort.Strings(fileArgs)
		step.FileArgs = strings.Join(fileArgs, " ")
		// container-build build-context binds: NAME `mounts` entries (e.g. fetch) become
		// `from=to` pairs the action realises as build-contexts. A real-PATH entry is
		// m6e-only and is skipped. From-sorted for a byte-stable render.
		if len(man.Mounts) > 0 {
			mounts := make([]ci.Mount, 0, len(man.Mounts))
			for _, m := range man.Mounts {
				if !m.IsPath() {
					mounts = append(mounts, m)
				}
			}
			sort.Slice(mounts, func(i, j int) bool { return mounts[i].From < mounts[j].From })
			pairs := make([]string, len(mounts))
			for i, m := range mounts {
				pairs[i] = m.From + "=" + m.To
			}
			step.MountsArg = strings.Join(pairs, " ")
		}
	}
	// Generic build-artifact PRODUCER: upload the declared path as a per-cell artifact
	// named from THIS tool + its cell axes; a downstream consumer recomputes the name.
	if man.Artifact != "" && man.Action == "" {
		step.Upload = artifactStem(j.Name, AxisMap(j.Axes))
		step.UploadPath = man.Artifact
	}
	return step
}

// secretsStep builds the SYNTHETIC secrets-provision StepView injected before dc-up-d
// in the live (fused) job (see Build). It is the cloud half of org.projectfile.ci.secrets:
// the SAME declarations the m6e lowering reads, forwarded as RAW JSON to provision.sh.
// Three `with:` inputs mirror provision.sh's contract:
//   - SecretsJSON: the verbatim org.projectfile.ci.secrets subtree (Build.Secrets),
//     uninterpreted — provision.sh dispatches each entry (value-write / docker-run).
//   - SecretsDefaultImage: the misc-tools ref (m6e-secret-* / openssl / htpasswd live
//     there), formed exactly as pfCliImageRef forms the cli image.
//   - SecretsImage: the project's OWN built image (the `image: self` target), per-cell
//     via the SAME substAxes the live job's ImageFullnameEnv uses. provision.sh tolerates
//     a non-empty image with no `self` decl (it is only REQUIRED when one exists), so this
//     is always emitted when the project has a built image — matching m6e, which always
//     exports M6E_IMAGE_FULLNAME.
func secretsStep(b *ci.Build, st *ci.Subtree, axes []ci.Axis, arch string) StepView {
	s := StepView{
		Name:                ActionSecretsProvision,
		Action:              ActionSecretsProvision,
		SecretsJSON:         string(b.Secrets),
		SecretsDefaultImage: miscToolsImageRef(b),
	}
	if st.Image != "" {
		s.SecretsImage = composeImage("", substAxes(st.Image, substKeys(axes, st)), arch)
	}
	return s
}

// axesEqual reports whether two axis slices are identical (key order + values) —
// the matrix-homogeneity check a node-job leans on (all member tools fan as ONE
// strategy.matrix, so they must share axes; constraint #1).
func axesEqual(a, b []ci.Axis) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Key != b[i].Key || len(a[i].Values) != len(b[i].Values) {
			return false
		}
		for k := range a[i].Values {
			if a[i].Values[k] != b[i].Values[k] {
				return false
			}
		}
	}
	return true
}

// dedupe removes adjacent duplicates from a sorted slice (small lists; keeps Build's
// env/name unions allocation-light).
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

// needs reports whether job j lists name among its needs (small slice; linear is
// fine and keeps the fusion code allocation-free).
func needs(j resolve.Job, name string) bool {
	for _, n := range j.Needs {
		if n == name {
			return true
		}
	}
	return false
}

// contains reports whether ss already holds s.
func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// reaches reports whether `from` depends — transitively, over the needs
// adjacency — on `target`. Used to keep deferred gate→fused-sink edges acyclic
// when a fused group spans a gate (the sink already reaches the gate).
func reaches(adj map[string][]string, from, target string) bool {
	seen := map[string]bool{}
	var dfs func(string) bool
	dfs = func(n string) bool {
		for _, m := range adj[n] {
			if m == target {
				return true
			}
			if !seen[m] {
				seen[m] = true
				if dfs(m) {
					return true
				}
			}
		}
		return false
	}
	return dfs(from)
}

// Build joins the resolved graph model with the subtree's tool manifests into
// the renderable/serialisable job model. It is render-target-independent; the
// Target only enters at template time.
func Build(rm *resolve.Model, st *ci.Subtree, b *ci.Build) Model {
	if rm == nil {
		return Model{}
	}
	// Producers of the OCI-tar hand-off: every tool whose manifest is a
	// container-build action. A tool that `needs` one of these is an image-scan or
	// live consumer — the build→scan/load wiring is DERIVED from the edge, no
	// manifest field (the locked "ordinary tool" model). Same-cell axes guarantee
	// the consumer's Stem equals the producer's, so the artifact names line up.
	builds := make(map[string]bool)
	for name, man := range st.Tools {
		if man.Action == ActionContainerBuild {
			builds[name] = true
		}
	}
	// Producers of the GENERIC build artifact: every NON-action tool with a manifest
	// `artifact:` path. A tool that `needs` one of these auto-downloads it — the plain-
	// `run:` analogue of the container-build OCI-tar edge above (binary-build → forge-
	// release). Value = the produced path, which the consumer restores to. Disjoint
	// from `builds`: an action uploads its own tar, a plain tool uses this hand-off.
	produces := make(map[string]string)
	for name, man := range st.Tools {
		if man.Artifact != "" && man.Action == "" {
			produces[name] = man.Artifact
		}
	}

	// Fuse co-location: a `fuse:` group's connected leaves collapse to ONE job (a
	// stateful runtime — a compose stack — cannot span jobs on an ephemeral runner).
	// In the node model this MERGES the spanned nodes: the fuse SINK's owning node hosts
	// every member as a step; the other owning nodes lose their work and render as echo
	// gates. The fuse chain mirrors the node chain (members are needs-adjacent ⇒ their
	// nodes are node-dep-adjacent), so the host (most-downstream node) already depends —
	// via node-deps — on the absorbed nodes; no deferred back-edge is needed.
	absorbed, _, fusedOrder := fuseGroups(rm, st)
	// Goal-scoped build-arg override set: non-empty only when this file's single goal
	// declares `dispatch:{build-args:true}`, so a container-build arg lowers to prefer the
	// manual-run input. Computed once (the pinned goal is fixed for the whole file) and
	// threaded into every toolStep; empty for the combined model / a flagless goal.
	dispatchArgs := dispatchBuildArgs(st, b)
	byName := make(map[string]resolve.Job, len(rm.Jobs))
	for _, j := range rm.Jobs {
		byName[j.Name] = j
	}

	nodes := resolve.NodeModel(st)
	if nodes == nil {
		return Model{}
	}

	// toolNode maps each tool to its OWNING node. A tool listed by several nodes is
	// multi-homed: assign it to the EARLIEST (NodeOrder, the first View seen) and
	// fail-fast on a TRUE diamond — two owners with no node-dep path between them, so
	// "earliest" would be an arbitrary choice (constraint #3). origin keeps the FIRST
	// owner per tool for the fused-host event union below.
	nodeAdj := map[string][]string{}
	for _, nv := range nodes.Views {
		nodeAdj[nv.Name] = nv.NodeDeps
	}
	toolNode := map[string]string{}
	var buildErr error
	for _, nv := range nodes.Views {
		for _, t := range nv.Tools {
			prev, ok := toolNode[t]
			if !ok {
				toolNode[t] = nv.Name
				continue
			}
			if !reaches(nodeAdj, nv.Name, prev) && !reaches(nodeAdj, prev, nv.Name) && buildErr == nil {
				buildErr = fmt.Errorf("org.projectfile.ci: tool %q is multi-homed across unrelated nodes %q and %q "+
					"(a true diamond — node=job cannot place it; assign it to one node)", t, prev, nv.Name)
			}
			// earliest (prev, seen first in NodeOrder) wins — leave toolNode[t] = prev.
		}
	}

	whenByNode := map[string][]string{}
	for _, nv := range nodes.Views {
		whenByNode[nv.Name] = nv.When
	}
	// whenOf unions the `when` predicates of a set of nodes: empty (any unconditional
	// node ⇒ the job runs always) else the sorted union of event tokens.
	whenOf := func(names ...string) []string {
		set := map[string]bool{}
		for _, n := range names {
			w := whenByNode[n]
			if len(w) == 0 {
				return nil // an unconditional owner makes the job unconditional
			}
			for _, e := range w {
				set[e] = true
			}
		}
		if len(set) == 0 {
			return nil
		}
		out := make([]string, 0, len(set))
		for e := range set {
			out = append(out, e)
		}
		sort.Strings(out)
		return out
	}

	// One JobView per reachable node. Member tools render as ordered Steps; a node with
	// none (a pure join, or one whose only tool was absorbed into another node's fused
	// job) renders as an echo GATE. Needs are the authored node→node edges (NodeDeps) —
	// the fuse merge needs no extra edge (see above).
	jobs := make([]JobView, 0, len(nodes.Views))
	for _, nv := range nodes.Views {
		needs := append([]string(nil), nv.NodeDeps...)
		sort.Strings(needs)

		// The tools this node renders: its own (earliest-owner) tools, with a fuse SINK
		// expanded to its ordered members (which may be drawn from absorbed sibling
		// nodes); a tool absorbed into a DIFFERENT node's sink is skipped here.
		var memberTools []string
		ownerOf := map[string]string{} // member tool -> its ORIGINAL owning node (for the event union)
		for _, t := range nv.Tools {
			if toolNode[t] != nv.Name {
				continue // multi-homed: a different node owns it
			}
			if order, ok := fusedOrder[t]; ok {
				for _, m := range order {
					memberTools = append(memberTools, m)
					ownerOf[m] = toolNode[m]
				}
				continue
			}
			if absorbed[t] {
				continue // its fuse sink lives in another node — that node hosts it
			}
			memberTools = append(memberTools, t)
			ownerOf[t] = nv.Name
		}

		if len(memberTools) == 0 {
			jobs = append(jobs, JobView{Name: nv.Name, Class: ClassGate, IsGate: true, Needs: needs, Events: nv.When})
			continue
		}

		// MaxParallel rides from the owning node; it only surfaces in the template's
		// strategy block, so a node that never fans (no matrix) carries it inertly.
		job := JobView{Name: nv.Name, Needs: needs, MaxParallel: nv.MaxParallel}
		// Concurrency rides from the owning node (like MaxParallel): the author declares a
		// serialisation group in the manifest; the resolver only carries it to the template.
		if nv.Concurrency != nil {
			job.Concurrency = &JobConcurrencyView{Group: nv.Concurrency.Group, CancelInProgress: nv.Concurrency.CancelInProgress}
		}
		// Event predicate: the union over this node + every node that contributed a
		// hosted member (a fused host gathers its absorbed members' nodes' `when`).
		ownerSet := map[string]bool{nv.Name: true}
		ownerNames := []string{nv.Name}
		for _, m := range memberTools {
			if o := ownerOf[m]; o != "" && !ownerSet[o] {
				ownerSet[o] = true
				ownerNames = append(ownerNames, o)
			}
		}
		job.Events = whenOf(ownerNames...)

		// Aggregate job-level env (union of step env VALUES) + credential NAMES, and the
		// shared downloads/load. Matrix homogeneity (constraint #1): every member must
		// share the same axes (or none) — they fan as ONE strategy.matrix.
		envSeen := map[string]bool{}
		credSeen := map[string]bool{}
		dlSeen := map[string]bool{}
		// The daemon-side arch this job's loaded image is named under, resolved where the
		// build hand-off is (empty until then, and on a job that consumes no build).
		jobLoadArch := ""
		hasReports := false
		var axes []ci.Axis
		var excludes []ci.Exclusion
		axesSet := false
		for _, t := range memberTools {
			j := byName[t]
			man := st.Tools[t]
			if j.Class == resolve.ClassCell && len(j.Axes) > 0 {
				if axesSet && !axesEqual(axes, j.Axes) && buildErr == nil {
					buildErr = fmt.Errorf("org.projectfile.ci: node %q mixes incompatible matrix axes "+
						"(tool %q) — a node=job renders one strategy.matrix, so its tools must share axes", nv.Name, t)
				}
				if !axesSet {
					// Exclusions belong to the matrix the axes came from, so they are taken
					// with them — a fused job never mixes one node's grid with another's cuts.
					axes, excludes = j.Axes, j.Excludes
					axesSet = true
				}
			}
			step := toolStep(j, st, b, dispatchArgs)
			// Any member that emits a `reports:` glob flags the job for ONE rolled-up
			// reports upload (set after the loop), not a per-tool upload.
			if man.Reports != "" && man.Action == "" {
				hasReports = true
			}
			// Build-tar / artifact consumer — DERIVED from the contracted needs edge, no
			// manifest field. A LIVE-fused member loads the tar into the daemon at the
			// JOB level; a non-fused scanner reads it as a file via M6E_IMAGE_ARCHIVE on
			// its own step. A generic build-artifact consumer pulls the producer's upload.
			// Only the LIVE group loads: it is the one whose compose stack runs the image.
			// A `publish`/`lint` member takes neither branch — oci-push and cosign act on
			// the archive and on the registry digest, so a load there enters an image
			// nothing reads into the runner's SHARED containers-storage graphroot, and the
			// paired docker-cleanup then reaps it. That pair is a writer AND a reaper in
			// the race documented in .agents/CONTAINERS.md: a concurrent job resolving a
			// blob it is reusing finds the layer deleted underneath it and dies with
			// `layer for blob … not found` (podman reports it as "payload does not match
			// any of the supported image formats", exit 125). Cheapest fix for a flake is
			// not emitting the step that causes it.
			for _, need := range j.Needs {
				if builds[need] {
					// The producer may fan over an axis this node DROPPED, in which case one
					// cell-keyed stem cannot name what it has to consume: the arch cells each
					// uploaded their own tar and this job must take all of them. Bind the
					// axis to each declared value instead of to a matrix expression, off the
					// PRODUCER's axes so the two ends cannot drift.
					stems := []string{step.Stem}
					// loadArch is the value the DAEMON-side names bind to. It follows the
					// cell while the job fans over arch, and pins to one declared value when
					// the job stopped fanning but its producer did not — the build stamped
					// its tar with an arch-suffixed ref, so a ref composed without one names
					// an image the load never created (nothing to run, nothing to reap).
					loadArch := archVarExpr(j.Axes)
					if archAxis(byName[need].Axes) && !archAxis(j.Axes) {
						stems = nil
						for _, arch := range declaredArches(st) {
							name := archArtifactStem("image", byName[need].Axes, arch)
							stems = append(stems, name)
							step.Archives = append(step.Archives, ArchiveView{Arch: arch, Name: name})
						}
						if arches := declaredArches(st); len(arches) > 0 {
							loadArch = arches[0]
						}
					}
					for _, stem := range stems {
						if !dlSeen[stem] {
							dlSeen[stem] = true
							job.Downloads = append(job.Downloads, DownloadView{Name: stem})
						}
					}
					if man.Fuse == liveFuse {
						job.Load = true
						// The FIRST declared architecture stands in when the node consumes
						// several: a daemon ref names one image, and the arch set is written
						// host-arch-first (amd64 everywhere in this fleet), which is the only
						// member a runner can actually run without emulation. Stem and
						// loadArch move together — they name the same tar.
						job.Stem = stems[0]
						// build→live contract, all DERIVED from the per-cell basename so the
						// loaded image, the compose `name:`, and a local `make` agree with no
						// per-include hardcode: M6E_IMAGE_FULLNAME = the ref container-build
						// stamped (composeImage, registry-less — line 113/131 of steps.tmpl);
						// M6E_COMPOSE_PROJECT_NAME / M6E_CONTAINER_INSTANCE = the `ci-<basename>`
						// stem m6e's make plane uses (no `app` placeholder).
						img := substAxes(st.Image, substKeys(j.Axes, st))
						arch := loadArch
						jobLoadArch = loadArch
						loadedRef := composeImage("", img, arch)
						// run-scoped (not just cell-scoped) so two runs of the same
						// pipeline never name one stack; image ref stays unscoped.
						// Arch-scoped for the cell half of the same problem: the basename
						// carries every axis but the derived one, so two arch cells of one
						// series would otherwise share a pod and reap each other's stack.
						proj := composeProject(img) + archSuffix(arch) + runScopeSuffix
						step.Env = append(step.Env,
							EnvVar{Key: ImageFullnameEnv, Value: loadedRef},
							EnvVar{Key: ComposeProjectEnv, Value: proj},
							EnvVar{Key: ContainerInstanceEnv, Value: proj})
						// Reap the run-scoped tag at job end (selfImageTagExpr is unique per
						// run, so without this the store gains one image per run forever).
						job.RmiImage = loadedRef
						// Same reasoning for the stack's network: `down` cannot remove one
						// that still has an endpoint attached (the `up`-failed path), and it
						// says so without failing the step. Run-scoped => never reused.
						job.RmNetwork = proj + composeNetworkSuffix
					} else if man.Fuse == "" && man.Action == "" {
						step.Env = append(step.Env, EnvVar{Key: ImageArchiveEnv, Value: step.Stem + ".tar"})
					}
					break
				}
				if path, ok := produces[need]; ok {
					// The producer may fan over axes this node DROPPED (one torrent over
					// every cell's binaries), or over FEWER than it (five release cells
					// attaching that one torrent). Both ends are named off the PRODUCER,
					// so neither shape needs the other to agree about a matrix it does
					// not have; several names restore into the SAME path and merge there.
					for _, name := range artifactStems(need, byName[need].Axes, byName[need].Excludes, j.Axes) {
						if !dlSeen[name] {
							dlSeen[name] = true
							job.Downloads = append(job.Downloads, DownloadView{Name: name, Path: path})
						}
					}
					break
				}
			}
			for _, e := range step.Env {
				if !envSeen[e.Key] {
					envSeen[e.Key] = true
					job.Env = append(job.Env, e)
				}
			}
			for _, n := range step.EnvNames {
				credSeen[n] = true
			}
			job.Steps = append(job.Steps, step)
		}
		// secrets-provision SYNTHETIC step (cloud half of org.projectfile.ci.secrets):
		// a compose `secrets:` block mounts a `.secrets/` tree provision.sh materialises
		// BEFORE dc-up-d. `secrets-provision` is declared gha:false/forgejo:false (m6e-
		// only), so ForTarget strips it and it never reaches Resolve as a tool — the
		// resolver instead lowers its EFFECT: a leading step in the LIVE (fused) job,
		// where dc-up-d lives. Detection is DATA-ONLY (no node/tool name hardcode): the
		// job hosts a member whose manifest declares the LIVE fuse group (never `publish`
		// or `lint`: they run no compose stack, so a `.secrets/` tree materialised there
		// is written for a reader that does not exist)
		// AND the org.projectfile.ci.secrets subtree is declared. The empty-subtree no-op
		// invariant: nil/empty Build.Secrets ⇒ no step, byte-identical to before. Placed
		// FIRST (the fused members are already DAG-ordered; secrets must exist before the
		// stack references them). Carries no Env/EnvNames/Image — invisible to the
		// matrix-homogeneity check, the event union, and the env/cred folds above.
		if b != nil && len(b.Secrets) > 0 {
			for _, t := range memberTools {
				if st.Tools[t].Fuse == liveFuse {
					secretsArch := jobLoadArch
					if secretsArch == "" {
						secretsArch = archVarExpr(axes)
					}
					job.Steps = append([]StepView{secretsStep(b, st, axes, secretsArch)}, job.Steps...)
					break
				}
			}
		}
		if axesSet {
			job.Matrix = AxisMap(axes)
			job.Exclude = excludeViews(excludes)
			job.Class = string(resolve.ClassCell)
			// matrix.overrides is a GLOBAL-matrix feature: emit it only when this cell
			// job's axes ARE the global axes (a per-node matrix cell has different
			// dimensions the global include cannot match, and its extra vars would
			// arrive empty). axesEqual keeps the include off per-node cell jobs.
			if len(st.Overrides) > 0 && axesEqual(axes, st.Axes) {
				job.Include = includeViews(st.Overrides)
			}
		} else {
			job.Class = string(byName[memberTools[0]].Class)
		}
		if len(credSeen) > 0 {
			names := make([]string, 0, len(credSeen))
			for n := range credSeen {
				names = append(names, n)
			}
			sort.Strings(names)
			job.EnvNames = names
		}
		// ONE reports upload for the whole job: a single per-job artifact (the entire
		// `reports/` dir) instead of one zip per scanner. Cell-keyed so per-series cells
		// stay distinct; rendered always()-guarded at the end of the node (steps.tmpl).
		if hasReports {
			job.ReportsUpload = artifactStem("reports-"+nv.Name, AxisMap(axes))
			job.ReportsPath = "reports/"
		}
		// Fan-out happens HERE, after the steps, env and artifacts are settled, because a
		// chain link is the SAME node run at one axis value — splitting it earlier would
		// hand two nodes the same tools, and tool ownership is per node (a multi-homed
		// tool is assigned to exactly one owner, so the second link would render hollow).
		if axesSet && nv.Serialise != "" {
			chain, err := serialiseChain(job, nv.Serialise, axes)
			if err != nil {
				if buildErr == nil {
					buildErr = err
				}
				continue
			}
			jobs = append(jobs, chain...)
			continue
		}
		jobs = append(jobs, job)
	}

	// One ordering rule for the whole workflow: name-sorted (node-jobs and gates alike).
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].Name < jobs[j].Name })

	// File identity + trigger DATA. Pinned to ONE goal (ForGoal) => the file is named
	// for that goal, its timer/button come from THAT goal node's schedule/dispatch, and
	// the goal's own `when` SCOPES the file's trigger surface (a `when:[schedule]` goal
	// fires only on schedule — its interior un-gated jobs do not widen it to push/PR).
	// Otherwise it is the combined neutral model ("ci") whose surface derives from the
	// jobs' union (interior `when` gates must be able to fire).
	name, triggers := "ci", (*TriggersView)(nil)
	var goalScope []string
	if st.GoalsExplicit && len(st.Goals) == 1 {
		g := st.Nodes[st.Goals[0]]
		name = g.Name
		var buildInputs []ci.BuildInput
		if b != nil {
			buildInputs = b.Args // fuels `dispatch:{build-args:true}` — one input per declared arg
		}
		triggers = buildTriggers(g.Schedule, g.Dispatch, buildInputs, b)
		goalScope = g.When
	}
	// The `on:` surface derives from the REAL jobs only. The synthetic notify job is
	// appended AFTER: it is un-gated (empty Events), so folding it into buildOn would
	// force the broad push/PR surface back on and re-break a schedule-only goal (the
	// exact bug goalScope fixes). It instead rides whatever events the file already
	// fires for, gated by its own always()+var `if:`.
	on := buildOn(jobs, triggers, goalScope)
	if b != nil && b.Events != nil {
		// Stamp the goal into every tool-level emission before the notify job joins them:
		// a fact event and the goal event describe one run and MUST name the same goal,
		// and the goal name is only resolved here (toolStep runs before it is known).
		for ji := range jobs {
			for si := range jobs[ji].Steps {
				if e := jobs[ji].Steps[si].Emit; e != nil {
					e.Goal = name
				}
			}
		}
		jobs = append(jobs, emitJob(jobs, name, b.Events.WebhookVar))
	}
	// Workflow-level env: lower each neutral value the SAME way job env is lowered
	// (a ${REGISTRY} ref -> ${{ vars.· }}), key-sorted for byte-stable output. The tag
	// hoist lowerMakeExpr emits is expanded back (inlineHoists): a workflow `env:` value
	// cannot read the env context it is itself defining.
	var env []KV
	if len(st.Env) > 0 {
		defs := buildArgDefaults(b)
		for _, k := range sortedKeys(st.Env) {
			val := lowerMakeExpr(st.Env[k], defs, nil, nil)
			if ref, ok := declaredImageRef(k, b); ok { // a declared image outranks its own authored literal
				val = ref
			}
			env = append(env, KV{Key: k, Value: inlineHoists(val)})
		}
	}
	return Model{
		Name:            name,
		Jobs:            jobs,
		On:              on,
		Triggers:        triggers,
		Env:             env,
		PrimaryBranches: primaryBranches(b),
		Err:             buildErr,
	}
}

// emitJob synthesises the lifecycle-notify job (the forge half of the events model):
// it `needs` every real job so it runs last, and carries the EmitView the steps/emit
// partial + emitIf gate read. reserved name "notify" — a project MUST NOT declare a CI
// node by that name (phase 1 keeps it a fixed reserved token, KISS).
func emitJob(realJobs []JobView, goal, webhookVar string) JobView {
	needs := make([]string, 0, len(realJobs))
	for _, j := range realJobs {
		needs = append(needs, j.Name)
	}
	sort.Strings(needs)
	return JobView{
		Name:  "notify",
		Needs: needs,
		Class: ClassGate,
		Emit:  &EmitView{Goal: goal, WebhookVar: webhookVar},
	}
}

// JSON serialises the job model (the resolver's `resolve` output, and the
// template's documented input).
func (m Model) JSON() ([]byte, error) {
	return json.MarshalIndent(m, "", "  ")
}

// Workflow renders the job model to a target's CI YAML, applying that target's
// deployment overlay (org.projectfile.ci.<target>). A zero-value plat yields the
// adapter defaults — the overlay is purely additive.
//
// One template set is parsed (the target workflow + every step partial) so the
// `stepbody` dispatcher can select a partial by name at render time: `steps/
// default` for a portable tool, `providers/<provider>` for a provider leaf. A
// provider leaf with no registered provider partial is a HARD ERROR — never a
// silent fallthrough to `make` in the cloud (the defect this layer exists to kill).
func Workflow(m Model, target Target, plat ci.Platform) ([]byte, error) {
	// Fail-fast on a Build defect (incompatible per-node matrix axes / a multi-homed
	// tool) — never render a structurally-broken workflow.
	if m.Err != nil {
		return nil, m.Err
	}
	// Resolve the effective container-build backend: the overlay (org.projectfile.
	// ci.<target>.builder) overrides the per-target adapter default. `target` is a
	// value copy, so this never leaks across renders. An unknown builder fails fast
	// rather than emitting a dangling `container-build/<typo>` action ref.
	if plat.Builder != "" {
		if !Builders[plat.Builder] {
			return nil, fmt.Errorf("org.projectfile.ci.%s.builder %q is not a known build backend %v",
				target.Key, plat.Builder, sortedBuilders())
		}
		target.Builder = plat.Builder
	}
	// Robot-account checkout token: the overlay (org.projectfile.ci.<target>.checkout-
	// token) is a plain opt-in, and checkoutTokenExpr names the fleet-wide secret behind
	// a `||` fallback to the run-scoped token — so opting in never breaks a checkout
	// that works today. Same value-copy discipline as Builder above; an absent knob
	// leaves the field zero and steps/node emits no `token:` line at all, i.e.
	// byte-identical to before.
	if plat.CheckoutToken {
		target.CheckoutToken = checkoutTokenExpr()
	}
	// Action refs: the overlay (org.projectfile.ci.<target>.actions) overrides the
	// per-target adapter defaults — same value-copy discipline as Builder above (a set
	// slot replaces the constant, an absent one keeps it, so no overlay renders
	// byte-identical to before). Values are full `name@ref` pins, the form you'd
	// hand-write in `uses:`. Iterated sorted so an unknown-slot error is deterministic.
	for _, slot := range sortedKeys(plat.Actions) {
		ref := plat.Actions[slot]
		switch slot {
		case SlotCheckout:
			target.Checkout = ref
		case SlotDownloadArtifact:
			target.Download = ref
		case SlotUploadArtifact:
			target.Upload = ref
		default:
			return nil, fmt.Errorf("org.projectfile.ci.%s.actions: unknown slot %q "+
				"(known: checkout, download-artifact, upload-artifact)", target.Key, slot)
		}
	}
	// Library: a `repo@tag` PREFIX the lowering recomposes per action path
	// (`<repo>/<provider>@<tag>`), NOT a pinned `uses:` ref like the slots above — which
	// is why it sits BESIDE `actions` rather than inside it. Absent => adapter default.
	if ref := plat.Library; ref != "" {
		at := strings.LastIndex(ref, "@")
		if at <= 0 || at == len(ref)-1 {
			return nil, fmt.Errorf("org.projectfile.ci.%s.library %q must be a `repo@tag` ref", target.Key, ref)
		}
		target.ActionLib, target.ActionVer = ref[:at], ref[at+1:]
	}
	var root *template.Template
	root = template.New("root").Funcs(funcs).Funcs(template.FuncMap{
		// stepbody paints a whole job: a pure-join node is an `echo` GATE; every other
		// node is `steps/node` — one checkout, the shared downloads/`docker load`, then
		// each member tool as a step fragment.
		"stepbody": func(tgt Target, j JobView) (string, error) {
			name := "steps/node"
			switch {
			case j.Emit != nil:
				name = "steps/emit" // the synthetic notify job: one webhook step, no checkout
			case j.IsGate:
				name = "steps/gate"
			}
			var b strings.Builder
			if err := root.ExecuteTemplate(&b, name, stepCtx{Target: tgt, Job: j}); err != nil {
				return "", err
			}
			return b.String(), nil
		},
		// stepfrag paints ONE member tool inside its node-job (no checkout — that is the
		// job's): an `action:` tool dispatches to its action-library fragment; an image tool
		// to the run-tool fragment; a plain tool to a bare host `run:`. A missing action
		// fragment is a HARD ERROR — never a silent fall-through to `make` in the cloud.
		"stepfrag": func(tgt Target, s StepView) (string, error) {
			name := "frag/run"
			switch {
			case s.Action != "":
				name = "providers/" + s.Action
			case s.Image != "":
				name = "frag/run-tool"
			}
			if root.Lookup(name) == nil {
				return "", fmt.Errorf("step %q: no fragment %q for target %q "+
					"(an action step MUST lower to an action-library ref — never `make` in the cloud)", s.Name, name, tgt.Key)
			}
			var b strings.Builder
			if err := root.ExecuteTemplate(&b, name, fragCtx{Target: tgt, Step: s}); err != nil {
				return "", err
			}
			// A tool that declares `emit:` gets one webhook step appended to its own —
			// the same tail-step shape `frag/run` already uses for an artifact upload,
			// so the emission works for any fragment rather than one provider.
			if s.Emit != nil {
				if err := root.ExecuteTemplate(&b, "frag/emit", fragCtx{Target: tgt, Step: s}); err != nil {
					return "", err
				}
			}
			return b.String(), nil
		},
	})
	if _, err := root.ParseFS(templatesFS, "templates/*.tmpl"); err != nil {
		return nil, fmt.Errorf("parse templates: %w", err)
	}
	// Bind each job's neutral env NAMES to this target's credential secrets (a fresh
	// slice per job so the shared model is never mutated). A job with no secret name
	// renders byte-identical to before — the overlay is purely additive.
	jobs := make([]JobView, len(m.Jobs))
	copy(jobs, m.Jobs)
	for i := range jobs {
		jobs[i].Env = bindEnv(jobs[i], plat.Credentials)
		// Compose the per-job `if:` from the neutral event set (the GHA/Forgejo
		// spelling stays out of the model — same boundary as bindEnv/composeImage).
		// The synthetic notify job is the exception: its gate is always()+webhook-var,
		// not the event-derived jobIf (which would be empty for an un-gated job).
		if jobs[i].Emit != nil {
			jobs[i].If = emitIf(jobs[i].Emit.WebhookVar)
		} else {
			jobs[i].If = jobIf(jobs[i].Events, m.PrimaryBranches)
		}
		// Fan the publish job over its destinations, per lowering. Before the env
		// folds below: the axis is an action INPUT, not an env binding, so nothing
		// downstream reads it — but a job whose matrix grows must do so before its
		// view is handed to the template.
		jobs[i] = publishCells(jobs[i], target.Key)
		// Route this job's arch cells to their runners, after publishCells so the two
		// include lowerings compose on one final matrix rather than one overwriting the
		// other's rows.
		jobs[i] = archRunners(jobs[i], plat.RunsOnByArch, defaultRunner(target, plat))
		// Bind the audit re-scan target to what THIS lowering's route pulls from, before
		// valOf is taken: the step's `env:` input is read back from the job env below, so
		// the two spellings of the ref stay one value.
		jobs[i] = auditTarget(jobs[i], target.Key)
		// Resolve every forwarded env/build-arg NAME to its VALUE from this job's
		// post-bindEnv env (matrix axes, the image-archive path, the credential refs
		// bindEnv just appended). A composite ci-action does NOT inherit the job `env:`
		// as process env, so the value must ride the action input — see EnvForward.
		valOf := make(map[string]string, len(jobs[i].Env))
		for _, e := range jobs[i].Env {
			valOf[e.Key] = e.Value
		}
		for s := range jobs[i].Steps {
			st := &jobs[i].Steps[s]
			st.EnvForward = envPairs(st.EnvArg(), valOf)
			st.BuildArgs = envPairs(st.BuildArgNames, valOf)
		}
		// LAST: the job `env:` block is evaluated before the env context exists, so its
		// own values must carry the expanded hoists. Steps (evaluated later, with env in
		// scope) keep the compact form — hence after the forwards are taken from valOf.
		jobs[i].Env = inlineHoistsEnv(jobs[i].Env)
	}
	var buf bytes.Buffer
	data := struct {
		Name        string
		Target      Target
		Platform    PlatformView
		On          OnView
		ResolverEnv []KV
		Env         []KV
		Jobs        []JobView
	}{Name: m.Name, Target: target, Platform: buildPlatform(target, plat), On: m.On, ResolverEnv: resolverEnv(), Env: m.Env, Jobs: jobs}
	if err := root.ExecuteTemplate(&buf, target.Template, data); err != nil {
		return nil, fmt.Errorf("render %s: %w", target.Key, err)
	}
	return buf.Bytes(), nil
}

// envPairs resolves a whitespace NAME list (a run-tool `env:` / container-build
// `build-args:` forward) to NAME→VALUE bindings using the job's post-bindEnv env map.
// The value MUST ride the action input: a composite ci-action does not inherit the
// caller job's `env:` block as PROCESS env (the Forgejo runner drops it), so a by-NAME
// `--env NAME` / `--build-arg NAME` passthrough would read an unset var. A name with a
// known value (matrix axis, the image-archive path, a credential the overlay supplied)
// carries it; a name with no value keeps Value empty and is forwarded by NAME alone —
// the pre-fix behaviour (left to the runner's own OS env), so an unsupplied credential
// degrades gracefully rather than emitting `NAME=`.
func envPairs(names string, valOf map[string]string) []EnvVar {
	fields := strings.Fields(names)
	if len(fields) == 0 {
		return nil
	}
	out := make([]EnvVar, 0, len(fields))
	for _, n := range fields {
		out = append(out, EnvVar{Key: n, Value: valOf[n]})
	}
	return out
}

// bindEnv resolves a job's vendor-neutral env NAMES against the target's credentials
// overlay, APPENDING one secret binding per matching name AFTER the render-time Env
// (matrix axes, image-archive) the job already carries. A name the overlay supplies
// takes its secret ref (`GH_TOKEN` → ${{ secrets.GITHUB_TOKEN }}), scoped to exactly
// the job that declared the need; a name with no entry is skipped (left to inherited
// runner env). No names / no creds => the job's Env is returned untouched, so a
// project without the overlay produces today's workflow (graceful degradation).
func bindEnv(j JobView, creds map[string]string) []EnvVar {
	if len(j.EnvNames) == 0 || len(creds) == 0 {
		return j.Env
	}
	out := append([]EnvVar(nil), j.Env...)
	for _, name := range j.EnvNames { // already sorted in Build
		if ref, ok := creds[name]; ok {
			out = append(out, EnvVar{Key: name, Value: ref})
		}
	}
	return out
}

// PublishSinkAxis is the DESTINATION matrix axis: one publish cell per place the
// artifact goes, exactly as a build runs one cell per platform. Its values are sink
// NAMES, which carry no `$` and so survive composition verbatim like every other axis
// here. Prefixed like the other resolver-injected variables so it cannot collide with
// a project's own axis.
const PublishSinkAxis = "M6E_PUBLISH_SINK"

// PublishSinksVar is the forge-level variable that narrows the sink axis at RUN time: a
// comma list of the sink names this forge may publish to. UNSET publishes to every
// declared sink, so a project that sets nothing keeps the behaviour it has today — the
// only default a fleet-wide regeneration can safely carry.
//
// The gate is at STEP level: it names a matrix axis, and neither forge admits the
// `matrix` context in a job-level `if:` — Forgejo rejects the whole workflow file for
// it. A withheld destination therefore costs its cell's checkout and artifact download
// before skipping the push, which is the price of a per-cell gate. Deriving the matrix
// itself from the variable would skip even that and is deliberately not done: a
// misspelt or unset value would yield an EMPTY matrix, and a publish job with zero
// cells passes green having published nothing.
const PublishSinksVar = "CI_PUBLISH_SINKS"

// sinkGate is the run-time narrowing expression for one publish cell. Both operands are
// comma-wrapped so the match is on a WHOLE name: a bare `contains` would let a sink
// named `ghcr` ride a list that names only `ghcr-mirror`.
func sinkGate() string {
	list := "vars." + PublishSinksVar
	return list + " == '' || contains(format(',{0},', " + list + "), format(',{0},', matrix." + PublishSinkAxis + "))"
}

// publishCells makes the destination an AXIS of a publish job, so the fan-out over
// registries happens in the MATRIX rather than inside one action's loop. Three things
// follow, and none of them is coded for: a destination that fails fails ITS cell
// instead of the release, each cell reaches one credential, and a per-destination step
// (ECR's create-repository) becomes an ordinary per-cell step.
//
// It runs per TARGET because a route is a fact about a forge — a GitHub pipeline
// pushes to ghcr, a kiota one to kiota — so the axis VALUES differ per lowering while
// the neutral model carries them all. Same boundary as `if:` and the credential refs.
//
// The axis is APPENDED to the job's build axes rather than replacing them: a matrix
// image publishes each series to each destination, so the grid is the product. It
// never reaches artifact naming — the archive is one per BUILD cell, shared by every
// destination — because Build already fixed the stems from the build axes alone.
func publishCells(j JobView, targetKey string) JobView {
	var sinks []string
	var rows []MatrixRowView
	for si := range j.Steps {
		st := &j.Steps[si]
		// Assigned unconditionally: StepViews are shared across the per-target
		// renders, so a step left untouched here would keep the PREVIOUS target's
		// binding and publish a cell this target never declared.
		st.PublishSink, st.PublishIf, st.ReleaseURL, st.ReleaseRepo = "", "", "", ""
		refs, targets := st.PublishRefs[targetKey], st.ReleaseTargets[targetKey]
		if len(refs) == 0 && len(targets) == 0 {
			continue
		}
		st.PublishSink, st.PublishIf = matrixVarExpr(PublishSinkAxis, nil, nil), sinkGate()
		for _, r := range refs {
			sinks = appendUnique(sinks, r.Sink)
		}
		// A release destination carries coordinates the axis cannot: a forge URL and
		// the repository path ON that forge, which differ per destination and are
		// static per cell. That is exactly a matrix `include` row, so they ride one
		// instead of becoming two more axes nothing would ever fan over.
		if len(targets) == 0 {
			continue
		}
		st.ReleaseURL = matrixVarExpr(releaseURLVar, nil, nil)
		st.ReleaseRepo = matrixVarExpr(releaseRepoVar, nil, nil)
		for _, t := range targets {
			sinks = appendUnique(sinks, t.Sink)
			rows = append(rows, MatrixRowView{Fields: []KVView{
				{Key: PublishSinkAxis, Value: t.Sink},
				{Key: releaseRepoVar, Value: t.Repo},
				{Key: releaseURLVar, Value: t.URL},
			}})
		}
	}
	if len(sinks) == 0 {
		return j
	}
	// The gate belongs to the CELL, not to the push step: every member of a withheld
	// destination must skip with it. A member that READS what the push wrote — cosign
	// signing the digest oci-push recorded — otherwise runs in a cell that published
	// nothing and fails on the absent file, reporting a missing digest for what is
	// really a destination the operator withheld. A `when: always` teardown keeps its
	// own guard: it reaps what the cell itself created, published or not.
	for si := range j.Steps {
		if j.Steps[si].If == "" {
			j.Steps[si].PublishIf = sinkGate()
		}
	}
	genlog.Decision("publish_cells", j.Name+" -> "+strings.Join(sinks, ","),
		"org.projectfile.publish (lowering "+targetKey+")", "org.projectfile.sinks · vars."+PublishSinksVar)
	j.Matrix = append(append(AxisMap{}, j.Matrix...), ci.Axis{Key: PublishSinkAxis, Values: sinks})
	j.Include = append(append([]MatrixRowView{}, j.Include...), rows...)
	j.Class = string(resolve.ClassCell)
	return j
}

// archRunners routes each arch CELL of a job to the runner its target declares for
// that arch. Per target, beside publishCells, because which arches a forge serves
// natively is a fact about the forge: GitHub hosts arm64 and riscv64 machines, a
// single-host Forgejo emulates everything foreign.
//
// `runs-on` is one value per JOB and the arch cells share a job, so the choice cannot
// be made by writing three jobs. It rides a matrix include row keyed by the arch axis
// — the same lowering publishCells already uses for the per-destination release
// coordinates — and the job's runs-on reads that row's variable. EVERY arch in the
// axis gets a row, mapped or not: an unmapped one carries the default label
// explicitly, because a cell whose M6E_RUNNER resolved to empty would render an
// invalid `runs-on` rather than falling back.
func archRunners(j JobView, byArch map[string]string, def string) JobView {
	if len(byArch) == 0 {
		return j
	}
	var arches []string
	for _, a := range j.Matrix {
		if a.Key == ci.ArchAxis {
			arches = a.Values
			break
		}
	}
	if len(arches) == 0 {
		return j
	}
	rows := make([]MatrixRowView, 0, len(arches))
	for _, arch := range arches {
		label, native := byArch[arch]
		source := "runs-on." + arch
		if !native {
			label, source = def, "runs-on."+ci.RunsOnDefaultKey+" (emulated: arch unmapped)"
		}
		genlog.Decision("arch_runner", j.Name+" "+arch+" -> "+label, source, "runs-on."+arch)
		rows = append(rows, MatrixRowView{Fields: []KVView{
			{Key: ci.ArchAxis, Value: arch},
			{Key: runnerVar, Value: label},
		}})
	}
	j.Include = append(append([]MatrixRowView{}, j.Include...), rows...)
	j.RunsOn = matrixVarExpr(runnerVar, nil, nil)
	return j
}

// runnerVar carries one cell's chosen runner label, bound beside the arch axis on a
// matrix include row.
const runnerVar = "M6E_RUNNER"

// releaseURLVar / releaseRepoVar are the per-cell release coordinates, carried as
// matrix include fields beside the destination axis.
const (
	releaseURLVar  = "M6E_RELEASE_URL"
	releaseRepoVar = "M6E_RELEASE_REPO"
)

// appendUnique keeps the destination axis a SET in declaration order: a job hosting
// both publish planes must fan each destination once, not once per plane.
func appendUnique(vals []string, v string) []string {
	for _, have := range vals {
		if have == v {
			return vals
		}
	}
	return append(vals, v)
}

// composeImage resolves the project's OWN built image to a concrete ref (the
// generation-time composition that keeps the source vendor-neutral). It is the SINGLE
// choke point for the artifact tag: container-build stamps it into the tar,
// M6E_IMAGE_FULLNAME reads it back on `docker load`, compose `image:` reads that env,
// and secrets-provision `docker run`s it — all four agree because all four flow through
// here. Three cases, in order:
//   - an image already carrying a `:tag` is a complete external ref
//     (`hadolint/hadolint:v2.14.0`) — emitted VERBATIM (registry + tag-var ignored);
//   - a registry-relative path WITH a `registry` var name composes
//     `${{ vars.<registry> }}/<image>:<self-tag>` (the private-registry opt-in);
//   - a registry-relative path with NO registry is left bare `<image>:<self-tag>` —
//     the Docker Hub DEFAULT, so this project's locally-built/loaded image needs no
//     registry config.
//
// The tag is selfImageTagExpr — the run-SCOPED variant (dev/latest flip + run_id),
// NOT the shared imageTagExpr. The run scope makes each pipeline run's
// artifact tag UNIQUE so a freshly docker-loaded image cannot be shadowed by a stale
// same-named image in the runner's docker store (the bug: docker run f5m/tor:latest
// resolved to a stale docker.io/f5m/tor:latest). Base/tool images keep the shared
// imageTagExpr (they never enter the docker store via load). oci-push's image: is also
// routed here, but skopeo copies the tar DIRECTLY to the registry and ignores the tag
// half, so the run scope is harmless there.
func composeImage(registry, img, arch string) string {
	if strings.Contains(img, ":") {
		return img
	}
	if registry == "" {
		return img + ":" + selfImageTagExpr(arch)
	}
	return VarRef(registry) + "/" + img + ":" + selfImageTagExpr(arch)
}

// publishedImageRef renders the FALLBACK published ref of a project's per-cell image at the
// mutable `:latest` tag — the ImagePublishedEnv value for a project whose route declares no
// `pull` sink. Registry is the OUTPUT_REGISTRY oci-push prefixes the push ref with; basename
// is the per-cell built-image basename (substAxes over the subtree image, the SAME transform
// container-build/oci-push apply); `latest` is fixed (not imageTagExpr's dev/latest flip) — the
// audit deliberately scans the published rolling tag.
//
// It is a PREFIX composition and therefore knows one path grammar: `<registry>/<nested
// path>:latest`. A destination refusing a nested path (Docker Hub holds exactly
// `namespace/name`) cannot be named this way, which is why a declared route audits through
// auditTarget instead. This path survives for the ~130 projects that declare no route.
// NOTE: an empty OUTPUT_REGISTRY yields a leading-slash ref; the fleet always sets it.
func publishedImageRef(image string, subst []ci.Axis) string {
	return outputRegistryExpr() + "/" + substAxes(image, subst) + ":latest"
}

// auditTarget binds the `audited` re-scan target for THIS lowering: the composed
// `publish.<forge>.pull` destination replaces the OUTPUT_REGISTRY prefix the neutral model
// carries. Per target, beside publishCells and the credential refs, because a route is a
// fact about a forge — a GitHub pipeline audits what it pushed to ghcr, a kiota one what it
// pushed to kiota.
//
// It rewrites the JOB env only. The step's own `env:` input is taken from that job env
// afterwards (EnvForward via valOf), so binding it once here reaches both spellings — and
// the StepView, which is SHARED across targets, is never mutated.
func auditTarget(j JobView, targetKey string) JobView {
	ref := ""
	for _, st := range j.Steps {
		if r, ok := st.PullRefs[targetKey]; ok && r != "" {
			ref = r
			break
		}
	}
	if ref == "" {
		return j
	}
	env := append([]EnvVar(nil), j.Env...)
	for i := range env {
		if env[i].Key == ImagePublishedEnv {
			genlog.Decision("audit_target", j.Name+" -> "+ref,
				"org.projectfile.publish.pull (lowering "+targetKey+")", env[i].Value)
			env[i].Value = ref
		}
	}
	j.Env = env
	return j
}

// cacheHostPath is the host-side dir a named cache mounts from: the target's CacheDir
// base joined with the cache name (a persistent runner dir on a self-hosted runner, the
// actions/cache restore target on an ephemeral one). The run-tool fragment uses it as
// both the actions/cache `path:` and the host half of the mount spec.
func cacheHostPath(t Target, name string) string { return t.CacheDir + "/" + name }

// cacheMounts renders a run-tool step's `mounts` input from its named caches: ONE bind
// per cache (`<host>:<container>:<mode>`) — RO for a scan that READS the refresh-owned
// shared DB, RW for a `*-db-update` writer that REFRESHES it in place. Space-joined to
// match run-tool's whitespace contract. Only the host side is per-target (cacheHostPath),
// so the spec SHAPE is identical on both forges (Law 3).
func cacheMounts(t Target, s StepView) string {
	specs := make([]string, 0, len(s.Caches))
	for _, c := range s.Caches {
		specs = append(specs, cacheHostPath(t, c.Name)+":"+c.Path+":"+c.Mode)
	}
	return strings.Join(specs, " ")
}

// cacheKey is the actions/cache key stem for a named cache (ephemeral runner). The scan
// side restores `<prefix>-<name>-` (trailing `-` so the exact key never hits and the
// restore-keys prefix always picks the newest dated entry the refresh saved). One
// source for the prefix so the refresh pipeline's save key agrees.
func cacheKey(name string) string { return CacheKeyPrefix + "-" + name + "-" }

// cacheSaveKey is the actions/cache SAVE key a `*-db-update` writer persists its refresh
// under: the scan-side stem plus a UNIQUE roll (`${{ github.run_id }}`). GitHub cache keys
// are immutable, so the roll makes every refresh a new entry; the scan's `restore-keys`
// prefix (cacheKey, trailing `-`) then matches and picks the most-recently-created one.
// Same stem as cacheKey so the two sides provably agree.
func cacheSaveKey(name string) string { return cacheKey(name) + "${{ github.run_id }}" }

// writerCaches is the `rw` subset of a step's caches — the `*-db-update` writers whose
// refreshed DB must be saved back on an ephemeral runner. Scans (ro) restore only, so
// they are filtered out here rather than gated in the template (logic stays in Build).
func writerCaches(s StepView) []Cache {
	out := make([]Cache, 0, len(s.Caches))
	for _, c := range s.Caches {
		if c.Mode == "rw" {
			out = append(out, c)
		}
	}
	return out
}

// funcs are the dumb formatting helpers the template leans on (the template
// computes nothing of substance — that all happens in Build).
// publishRefsFor selects the destinations THIS target publishes to. The step carries
// every lowering's list, because one StepView is rendered for each target; picking one
// is a lookup, not a computation, which is why it belongs in a template helper.
func publishRefsFor(target Target, refs map[string][]ci.SinkRef) []ci.SinkRef {
	return refs[target.Key]
}

var funcs = template.FuncMap{
	"publishRefsFor": publishRefsFor,
	"composeImage":   composeImage,
	"cacheHostPath":  cacheHostPath,
	"cacheMounts":    cacheMounts,
	"cacheKey":       cacheKey,
	"cacheSaveKey":   cacheSaveKey,
	"writerCaches":   writerCaches,
	// alwaysExpr is the `${{ always() }}` step guard (GHA/Forgejo-identical) the report
	// upload renders so a fail-closed scan still publishes its SARIF/JSON. A func, not an
	// inline literal, because `${{ … }}` collides with Go template's own `{{ }}` delimiters.
	"alwaysExpr": func() string { return stepAlwaysExpr },
	// outputRegistryExpr is the oci-push push-target expression `${{ vars.OUTPUT_REGISTRY }}`
	// (empty => Docker Hub). A func, not an inline literal, because `${{ … }}` collides
	// with Go template's own `{{ }}` delimiters — same reason alwaysExpr is a func. See
	// OutputRegistryVar for the input/output registry distinction.
	"outputRegistryExpr": outputRegistryExpr,
	// cellEnv names the step-env key the emit fragment binds the matrix JSON to, so the
	// template and the shell that reads it back share ONE literal.
	"cellEnv": func() string { return CellEnv },
	// indent left-pads every line of s by n spaces — used to place a multi-line shell
	// script (the notify job's emit run) under a `run: |` YAML block scalar. The first
	// line is padded too, so the caller writes `run: |` then `{{ indent 10 … }}`.
	"indent": func(n int, s string) string {
		pad := strings.Repeat(" ", n)
		return pad + strings.ReplaceAll(s, "\n", "\n"+pad)
	},
	// yamlList renders a string slice as a YAML flow sequence with each item
	// double-quoted: ["a", "b"]. Quoting keeps version-like values ("8.5") and
	// label strings unambiguous.
	"yamlList": func(items []string) string {
		quoted := make([]string, len(items))
		for i, s := range items {
			quoted[i] = `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
		}
		return "[" + strings.Join(quoted, ", ") + "]"
	},
	// actionRef resolves a library PATH to the target's external action ref
	// (`<ActionLib>/<path>@<ActionVer>`): pf-ci keeps "which action + ordering"
	// and the external library keeps the recipe. The path is composed by each action
	// partial, so a backend/variant (`container-build/buildx`) needs no new func.
	"actionRef": func(t Target, path string) string {
		return t.ActionLib + "/" + path + "@" + t.ActionVer
	},
}

// AxisMap renders an ordered axis slice as a JSON object {KEY: [values]} while
// preserving the spec's key order (encoding/json would otherwise demand a map
// and re-sort). Ordered output keeps the generated workflow byte-stable.
type AxisMap []ci.Axis

func (a AxisMap) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, axis := range a {
		if i > 0 {
			buf.WriteByte(',')
		}
		key, _ := json.Marshal(axis.Key)
		vals, _ := json.Marshal(axis.Values)
		buf.Write(key)
		buf.WriteByte(':')
		buf.Write(vals)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// MatrixRowView is one row of a rendered strategy.matrix ADJUSTER — an `include`
// entry (a matrix.overrides row) or an `exclude` entry (a matrix.exclude row); both
// take the same shape, a key-sorted field list, and differ only in which block they
// land in. GHA matches a row to a cell where the fields that name matrix axes align:
// for include, the remaining fields are the extra vars the cell gains; for exclude,
// there are no remaining fields and the matched cell is dropped. Values are emitted
// double-quoted (strings) so a numeric-looking value ("0.16", "21") matches the
// double-quoted axis values for cell matching and stays a string the way build-args
// consume them.
type MatrixRowView struct {
	Fields []KVView `json:"fields"`
}

// KVView is one key→value pair of a rendered strategy.matrix include/exclude entry.
type KVView struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// includeViews lowers the parsed matrix.overrides rows to the render view: each row
// flattens its MATCH (axis-values) and VARS (extra vars) into one key-sorted field
// list, the shape strategy.matrix.include takes.
func includeViews(entries []ci.OverrideEntry) []MatrixRowView {
	out := make([]MatrixRowView, 0, len(entries))
	for _, e := range entries {
		all := append([]ci.KV(nil), e.Match...)
		all = append(all, e.Vars...)
		sort.Slice(all, func(i, j int) bool { return all[i].Key < all[j].Key })
		out = append(out, MatrixRowView{Fields: kvViews(all)})
	}
	return out
}

// excludeViews lowers the parsed matrix.exclude rows to the render view. A row is
// already key-sorted axis KEY→value pairs (buildExcludes), which is exactly what
// strategy.matrix.exclude takes — the forge then mints the product minus these
// combinations, so the cells a workflow runs equal the cells every other lowering
// of the same DAG runs.
func excludeViews(rows []ci.Exclusion) []MatrixRowView {
	out := make([]MatrixRowView, 0, len(rows))
	for _, r := range rows {
		out = append(out, MatrixRowView{Fields: kvViews(r)})
	}
	return out
}

// jobIDUnsafe matches every run of characters a forge will not accept in a job id
// (which is `[A-Za-z_][A-Za-z0-9_-]*`), so an axis value like `3.14` or `linux/amd64`
// can key a job name. The suffix position means a leading digit is already legal.
var jobIDUnsafe = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

// serialiseChain lowers a node's `serialise: <AXIS>` (ci.Node.Serialise) into the one
// ordering primitive EVERY forge honours: one job per value of that axis, each waiting
// on the one before, plus a join carrying the AUTHORED node name so not a single
// downstream `needs:` has to know the split happened.
//
// Why an edge and not a cap: `strategy.max-parallel` is a GitHub guarantee only. Forgejo
// expands a matrix statically into independent job rows and dispatches each to any runner
// with a free slot, with no scheduler stage in between for a cap to act in — so the cap
// renders and is ignored (go-gitea/gitea#35561), while `needs` is stored on the job row
// the dispatcher actually reads.
//
// Only the named axis is walked; every other axis keeps fanning inside each link, so a
// memory-bound build gets one series at a time with its arches still parallel. An axis the
// project does not declare (or one carrying a single value) is a logged NO-OP returning
// the job untouched — the same graceful degradation as matrix.without / matrix.pin, and
// what lets ONE shared m6e declaration render byte-identically fleet-wide.
func serialiseChain(job JobView, axis string, axes []ci.Axis) ([]JobView, error) {
	idx := -1
	for i, a := range axes {
		if a.Key == axis {
			idx = i
			break
		}
	}
	if idx < 0 {
		genlog.Info("serialise: no such axis, job keeps its fan-out", "job", job.Name, "axis", axis, "declared", len(axes))
		return []JobView{job}, nil
	}
	values := axes[idx].Values
	if len(values) < 2 {
		genlog.Info("serialise: axis carries one value, nothing to chain", "job", job.Name, "axis", axis, "values", values)
		return []JobView{job}, nil
	}

	links := make([]JobView, 0, len(values))
	names := make([]string, 0, len(values))
	seen := make(map[string]string, len(values))
	for _, v := range values {
		name := job.Name + "-" + jobIDUnsafe.ReplaceAllString(v, "-")
		// Two values may slug to ONE id (`3.14` and `3-14`), which would silently drop a
		// link and build one series twice. Refuse rather than emit a workflow that is
		// short a cell nobody would miss until the release is wrong.
		if prev, dup := seen[name]; dup {
			return nil, fmt.Errorf("org.projectfile.ci: node %q serialise %q: values %q and %q both key job %q "+
				"(rename one value, or drop serialise on this axis)", job.Name, axis, prev, v, name)
		}
		seen[name] = v

		link := job
		link.Name = name
		// The axis stays DECLARED at one value rather than being dropped: every artifact
		// name, report stem and env binding in the steps reads `${{ matrix.<AXIS> }}`, so a
		// link without the axis would ask for a tar nobody uploaded (the reason matrix.pin
		// exists at all).
		link.Matrix = pinAxis(axes, idx, v)
		link.Include = rowsMatching(job.Include, axis, v)
		link.Exclude = rowsMatching(job.Exclude, axis, v)
		link.Needs = append(append([]string(nil), job.Needs...), names...)
		sort.Strings(link.Needs)
		links = append(links, link)
		names = append(names, name)
		genlog.Decision("serialise_link", job.Name+" "+axis+"="+v+" -> "+name,
			"chained after "+strings.Join(names[:len(names)-1], ","), "nodes."+job.Name+".serialise")
	}
	// The join keeps the authored name AND the node's event predicate: a consumer gated on
	// the same events must not wait on a gate that never runs.
	return append(links, JobView{Name: job.Name, Class: ClassGate, IsGate: true, Needs: names, Events: job.Events}), nil
}

// pinAxis returns axes with the one at idx narrowed to a single value — the grid a chain
// link fans over. The other axes are carried untouched, so they still fan in parallel.
func pinAxis(axes []ci.Axis, idx int, value string) AxisMap {
	out := make([]ci.Axis, len(axes))
	copy(out, axes)
	out[idx] = ci.Axis{Key: axes[idx].Key, Values: []string{value}}
	return AxisMap(out)
}

// rowsMatching keeps the include/exclude rows that still belong to a link's narrowed grid:
// a row naming the serialised axis survives only for THIS value, a row silent about it
// applies to every link. Without the filter an `include` row for another value would MINT
// the cell the chain just took apart (GHA grows the matrix for an unmatched include row).
func rowsMatching(rows []MatrixRowView, axis, value string) []MatrixRowView {
	if len(rows) == 0 {
		return nil
	}
	out := make([]MatrixRowView, 0, len(rows))
	for _, r := range rows {
		keep := true
		for _, f := range r.Fields {
			if f.Key == axis && f.Value != value {
				keep = false
				break
			}
		}
		if keep {
			out = append(out, r)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func kvViews(kvs []ci.KV) []KVView {
	fields := make([]KVView, 0, len(kvs))
	for _, kv := range kvs {
		fields = append(fields, KVView{Key: kv.Key, Value: kv.Value})
	}
	return fields
}

// substKeys returns the {placeholder} substitution set: the job's real matrix axes
// PLUS a phantom (value-less) axis per matrix.overrides extra-var name, so an image
// basename, a file-arg path, or an envset value carrying {EXTRA} lowers to
// ${{ matrix.EXTRA }} exactly as an {AXIS} placeholder does. substAxes reads only
// .Key, so phantom axes substitute correctly. Cell-uniqueness keys (artifactStem)
// keep using the REAL axes only — extra vars are constant per axis-cell, so they
// must not broaden the stem.
func substKeys(axes []ci.Axis, st *ci.Subtree) []ci.Axis {
	if st == nil {
		return axes
	}
	extras := st.ExtraVarKeys()
	if len(extras) == 0 {
		return axes
	}
	// Sorted phantom keys for a byte-stable substitution order.
	keys := make([]string, 0, len(extras))
	for k := range extras {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]ci.Axis, 0, len(axes)+len(keys))
	out = append(out, axes...)
	for _, k := range keys {
		out = append(out, ci.Axis{Key: k})
	}
	return out
}
