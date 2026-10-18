// SPDX-FileCopyrightText: 2026 Damián Búho <damian.buho@proton.me>
//
// SPDX-License-Identifier: MIT

package ci

import (
	"reflect"
	"strings"
	"testing"

	"kiota.ch/projectfile/core/v2/pkg/projectfile"
)

// TestValidEvent pins the closed event vocabulary plus its ONE open form: the five
// stand-alone events, and `push:<branch>` which must carry a branch name after the
// colon. A bare `push:` names no branch, so it is refused rather than gating on
// every push.
func TestValidEvent(t *testing.T) {
	for name, tc := range map[string]struct {
		event string
		want  bool
	}{
		"tag":          {EventTag, true},
		"preview":      {EventPreview, true},
		"primary":      {EventPrimary, true},
		"dispatch":     {EventDispatch, true},
		"schedule":     {EventSchedule, true},
		"push-branch":  {EventPushPrefix + "dev", true},
		"push-bare":    {EventPushPrefix, false},
		"push-branchy": {EventPushPrefix + "release/1.0", true},
		"unknown":      {"pushover", false},
		"empty-event":  {"", false},
		"case":         {"TAG", false},
		"push-upper":   {"PUSH:dev", false},
	} {
		if got := validEvent(tc.event); got != tc.want {
			t.Errorf("%s: validEvent(%q) = %v, want %v", name, tc.event, got, tc.want)
		}
	}
}

// TestBuildsContainerGatesArchFanOut pins why the arch axis is not minted from a
// declaration alone: a project declaring architectures but building no container
// keeps its pipeline unfanned, so every job is not doubled over a dimension no step
// reads.
func TestBuildsContainer(t *testing.T) {
	for name, tc := range map[string]struct {
		st   *Subtree
		want bool
	}{
		"container-tool": {
			&Subtree{Tools: map[string]Manifest{"build": {Action: ActionContainerBuild}}}, true,
		},
		"run-only-tool": {
			&Subtree{Tools: map[string]Manifest{"build": {Run: "make container-build"}}}, false,
		},
		"no-tools": {&Subtree{}, false},
	} {
		if got := tc.st.buildsContainer(); got != tc.want {
			t.Errorf("%s: buildsContainer() = %v, want %v", name, got, tc.want)
		}
	}
}

// TestForGoalPinsOneGoal pins the per-goal narrowing main's generate uses: the
// result declares exactly the one goal with GoalsExplicit set (so the resolver's
// closure is that goal's alone), the receiver is left untouched, and a nil subtree
// stays nil.
func TestForGoal(t *testing.T) {
	var nilSt *Subtree
	if nilSt.ForGoal("ready") != nil {
		t.Fatal("ForGoal on a nil subtree must stay nil, not mint an empty one")
	}
	st := &Subtree{Goals: []string{"a", "b"}, GoalsExplicit: true}
	out := st.ForGoal("b")
	if len(out.Goals) != 1 || out.Goals[0] != "b" || !out.GoalsExplicit {
		t.Fatalf("narrowed = %+v, want the single explicit goal b", out.Goals)
	}
	if len(st.Goals) != 2 {
		t.Fatalf("receiver mutated: goals = %v, want the original pair", st.Goals)
	}
}

// TestDecodeNodeDispatchCarriesBuildArgs pins the manual-dispatch decode: inputs
// decode name-sorted and the build-args flag rides through, so the run form exposes
// org.projectfile.build.args when the document asks for it.
func TestDecodeNodeDispatchCarriesBuildArgs(t *testing.T) {
	sched, dispatch, err := decodeNodeTriggers("ready", rawNode{
		Goal: true,
		Dispatch: &rawDispatch{
			BuildArgs: true,
			Inputs: map[string]rawInput{
				"zeta":        {Type: InputString},
				"alpha-input": {Type: InputBoolean, Required: true, Default: jsonScalar(BoolFalse)},
			},
		},
	})
	if err != nil {
		t.Fatalf("decodeNodeTriggers: %v", err)
	}
	if sched != nil {
		t.Fatalf("sched = %v, want none on a dispatch-only node", sched)
	}
	if dispatch == nil || !dispatch.BuildArgs {
		t.Fatalf("dispatch = %+v, want build-args exposed", dispatch)
	}
	if len(dispatch.Inputs) != 2 || dispatch.Inputs[0].Name != "alpha-input" || dispatch.Inputs[1].Name != "zeta" {
		t.Fatalf("inputs = %+v, want alpha then zeta", dispatch.Inputs)
	}
	if !dispatch.Inputs[0].Required || !dispatch.Inputs[0].HasDefault || dispatch.Inputs[0].Default != BoolFalse {
		t.Fatalf("alpha = %+v, want required with its false default", dispatch.Inputs[0])
	}
}

// TestDecodeNodeDispatchInputErrorNamesTheNode pins that a bad input fails the
// whole parse and names the node that owns it, so the message points at the file to
// edit rather than at the parser.
func TestDecodeNodeDispatchInputErrorNamesTheNode(t *testing.T) {
	_, _, err := decodeNodeTriggers("published", rawNode{
		Goal:     true,
		Dispatch: &rawDispatch{Inputs: map[string]rawInput{"region": {Type: "continent"}}},
	})
	if err == nil || !strings.Contains(err.Error(), `node "published"`) {
		t.Fatalf("err=%v, want the failing node named", err)
	}
}

// TestValidateEventGatesNeedsTheTrigger pins the paired-event gate: a node gating
// on `dispatch` or `schedule` while no goal declares the matching trigger is
// refused, since the gate would never be evaluated.
func TestValidateEventGatesNeedsTheTrigger(t *testing.T) {
	for name, doc := range map[string]string{
		"dispatch-without-trigger": `{
			"tools": {"build": {"action": "container-build"}},
			"nodes": {
				"built": {"goal": true, "needs": {"build": true}},
				"done": {"goal": true, "when": {"events": ["dispatch"]}, "needs": {"built": true}}
			}}`,
		"schedule-without-trigger": `{
			"tools": {"build": {"action": "container-build"}},
			"nodes": {
				"built": {"goal": true, "needs": {"build": true}},
				"done": {"goal": true, "when": {"events": ["schedule"]}, "needs": {"built": true}}
			}}`,
	} {
		if _, err := Parse([]byte(doc)); err == nil {
			t.Errorf("%s: parsed, want the unpaired-event gate refusal", name)
		} else if !strings.Contains(err.Error(), "no goal declares") {
			t.Errorf("%s: err=%v, want the unpaired-event refusal", name, err)
		}
	}
}

// TestValidateEventGatesAcceptsPairedTriggers pins the positive half: with the
// trigger declared on a goal, the very same gate parses.
func TestValidateEventGatesAcceptsPairedTriggers(t *testing.T) {
	for name, doc := range map[string]string{
		"dispatch": `{
			"tools": {"build": {"action": "container-build"}},
			"nodes": {
				"built": {"goal": true, "dispatch": {"build-args": true}, "needs": {"build": true}},
				"done": {"goal": true, "when": {"events": ["dispatch"]}, "needs": {"built": true}}
			}}`,
		"schedule": `{
			"tools": {"build": {"action": "container-build"}},
			"nodes": {
				"built": {"goal": true, "schedule": [{"cron": "0 3 * * *"}], "needs": {"build": true}},
				"done": {"goal": true, "when": {"events": ["schedule"]}, "needs": {"built": true}}
			}}`,
	} {
		if _, err := Parse([]byte(doc)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// TestNodeConcurrencyGroupMustBeNamed pins the fail-fast on an empty concurrency
// group: the forge rejects a group-less block, so the parse refuses it rather than
// emitting YAML the runner refuses.
func TestNodeConcurrencyGroupMustBeNamed(t *testing.T) {
	doc := `{
		"tools": {"build": {"action": "container-build"}},
		"nodes": {"ready": {"goal": true, "concurrency": {"cancel-in-progress": true}, "needs": {"build": true}}}}`
	if _, err := Parse([]byte(doc)); err == nil || !strings.Contains(err.Error(), "concurrency.group must be non-empty") {
		t.Fatalf("err=%v, want the empty-group refusal", err)
	}
}

// TestBuildInputRejectsNonScalarDefault pins that a build-arg default must be a
// scalar: the renderer coerces it straight into a workflow input, so a map or a
// list would ship a value the forge cannot read.
func TestBuildInputRejectsNonScalarDefault(t *testing.T) {
	if _, err := decodeBuildInputs([]byte(`{"X": {"default": {"a": "b"}}}`)); err == nil ||
		!strings.Contains(err.Error(), "default must be a scalar") {
		t.Fatalf("err=%v, want the non-scalar default refusal", err)
	}
}

// TestBuildInputRejectsDefaultAndFile pins mutual exclusion: a build arg is either
// a literal default or a file ref, and declaring both leaves no rule for which
// wins.
func TestBuildInputRejectsDefaultAndFile(t *testing.T) {
	if _, err := decodeBuildInputs([]byte(`{"X": {"default": "a", "file": "p.txt"}}`)); err == nil ||
		!strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("err=%v, want the mutual-exclusion refusal", err)
	}
}

// TestBuildInputScalarForms pins every accepted default spelling, including the
// number and boolean coercions that keep `SERIES: 24` and `DEBUG: true` out of
// quoting.
func TestBuildInputScalarForms(t *testing.T) {
	got, err := decodeBuildInputs([]byte(`{
		"A": "", "B": 16, "C": 3.14, "D": true, "E": {"default": "resolute"},
		"F": {"file": "p/{SERIES}.txt"}, "G": {"default": 24}
	}`))
	if err != nil {
		t.Fatalf("decodeBuildInputs: %v", err)
	}
	want := map[string]string{"A": "", "B": "16", "C": testFloatLiteral, "D": BoolTrue, "E": "resolute", "G": "24"}
	for _, bi := range got {
		if w, ok := want[bi.Name]; ok && bi.Default != w {
			t.Errorf("%s: default = %q, want %q", bi.Name, bi.Default, w)
		}
	}
	if got[5].File != "p/{SERIES}.txt" {
		t.Fatalf("F: file = %q", got[5].File)
	}
}

// TestLoadBuildRejectsMalformedSubtrees pins each overlay's own parse failure, so a
// malformed escape hatch fails loudly at the field that carries it instead of
// vanishing from the rendered workflow.
func TestLoadBuildRejectsMalformedSubtrees(t *testing.T) {
	for name, tc := range map[string]struct{ body, want string }{
		"ci-images": {
			body: "org:\n  projectfile:\n    ci:\n      images: [a, b]\n",
			want: "org.projectfile.ci.images: parse",
		},
		"build-target": {
			body: "org:\n  projectfile:\n    ci:\n      build-target: [a]\n",
			want: "org.projectfile.ci.build-target: parse",
		},
		"build-args": {
			body: "org:\n  projectfile:\n    build:\n      args:\n        X: {\"default\": [a]}\n",
			want: "org.projectfile.build:",
		},
		"events": {
			body: "org:\n  projectfile:\n    events:\n      webhook: not-a-map\n",
			want: "org.projectfile.events: parse",
		},
	} {
		_, err := LoadBuildWithOptions(namingDoc(t, tc.body), LoweringGHA, projectfile.ReadOptions{})
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err=%v, want one containing %q", name, err, tc.want)
		}
	}
}

// TestLoadBuildSecretsAreCapturedVerbatim pins that org.projectfile.ci.secrets is
// carried RAW, byte for byte: provision.sh parses it as-is, so any re-encoding
// here would rewrite a secret document the consumer did not write.
func TestLoadBuildSecretsAreCapturedVerbatim(t *testing.T) {
	b, err := LoadBuildWithOptions(namingDoc(t, `org:
  projectfile:
    ci:
      secrets:
        REGISTRY_PASSWORD:
          env: CI_REGISTRY_PASSWORD
`), LoweringGHA, projectfile.ReadOptions{})
	if err != nil {
		t.Fatalf("LoadBuild: %v", err)
	}
	if b == nil || !strings.Contains(string(b.Secrets), "REGISTRY_PASSWORD") {
		t.Fatalf("secrets = %q, want the subtree carried verbatim", b.Secrets)
	}
}

// TestLoadBuildBuildTargetPerLowering pins build-target as the per-lowering stage
// selector: the forge keys feed the container-build action, and a target with no
// entry yields no key rather than the other forge's.
func TestLoadBuildBuildTargetPerLowering(t *testing.T) {
	body := `org:
  projectfile:
    ci:
      build-target:
        gha: final
        forgejo: builder
`
	for lowering, want := range map[string]string{LoweringGHA: "final", LoweringForgejo: "builder"} {
		b, err := LoadBuildWithOptions(namingDoc(t, body), lowering, projectfile.ReadOptions{})
		if err != nil {
			t.Fatalf("%s: LoadBuild: %v", lowering, err)
		}
		if b == nil || !reflect.DeepEqual(b.BuildTarget, map[string]string{LoweringGHA: "final", LoweringForgejo: "builder"}) {
			t.Fatalf("%s: buildTarget = %v, want both forge keys", lowering, b.BuildTarget)
		}
		if b.BuildTarget[lowering] != want {
			t.Fatalf("%s: stage = %q, want %q", lowering, b.BuildTarget[lowering], want)
		}
	}
}

// TestStableBranchNeedsStableDispatch pins the opt-in: a declared stable branch is
// honoured only with release.stable-dispatch true, because it is what decides
// whether the stable branch's push releases the tag its commit carries. Without the
// opt-in the field is absent, and the renderer keeps its tag-push trigger.
func TestStableBranchNeedsStableDispatch(t *testing.T) {
	off := `org:
  projectfile:
    release:
      trunk: dev
      stable-branch: main
`
	if b := loadBuild(t, off); b == nil || b.StableBranch != "" {
		t.Fatalf("without the opt-in: stableBranch = %q, want none", b.StableBranch)
	}
	on := `org:
  projectfile:
    release:
      trunk: dev
      stable-branch: main
      stable-dispatch: true
`
	if b := loadBuild(t, on); b == nil || b.StableBranch != "main" {
		t.Fatalf("with the opt-in: stableBranch = %q, want main", b.StableBranch)
	}
	// An opt-in naming anything but true leaves it off.
	for _, value := range []string{"false", "\"TRUE\"", "yes"} {
		body := `org:
  projectfile:
    release:
      trunk: dev
      stable-branch: main
      stable-dispatch: ` + value + "\n"
		if b := loadBuild(t, body); b != nil && b.StableBranch != "" {
			t.Errorf("stable-dispatch: %s: stableBranch = %q, want none", value, b.StableBranch)
		}
	}
}

// TestPrimaryBranchesPrefersDeclaredTrunk pins the branch set a push is measured
// against to tell a release from a preview: the declared trunk when the document
// records one, else the forge default pair.
func TestPrimaryBranchesPrefersDeclaredTrunk(t *testing.T) {
	declared := loadBuild(t, `org:
  projectfile:
    release:
      trunk: dev
      branches: [{pattern: dev}]
`)
	if declared == nil || !reflect.DeepEqual(declared.PrimaryBranches, []string{"dev"}) {
		t.Fatalf("declared trunk: primaries = %v, want [dev]", declared.PrimaryBranches)
	}
	undocumented := loadBuild(t, `org:
  projectfile:
    images:
      D9T_TOOLS_IMAGE: {org: d9t, name: tools}
`)
	if undocumented != nil && len(undocumented.PrimaryBranches) != 0 {
		t.Fatalf("no release block: primaries = %v, want none", undocumented.PrimaryBranches)
	}
	if len(DefaultPrimaryBranches) == 0 {
		t.Fatal("DefaultPrimaryBranches is empty, want the forge default pair")
	}
}

// loadBuild reads one build plane through the public loader.
func loadBuild(t *testing.T, body string) *Build {
	t.Helper()
	b, err := LoadBuildWithOptions(namingDoc(t, body), LoweringGHA, projectfile.ReadOptions{})
	if err != nil {
		t.Fatalf("LoadBuild: %v", err)
	}
	return b
}
