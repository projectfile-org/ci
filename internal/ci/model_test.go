// SPDX-FileCopyrightText: 2026 Damián Búho <damian.buho@proton.me>
//
// SPDX-License-Identifier: MIT

package ci

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// TestMountVolOpt pins the secure default: every mount is read-only unless it
// declares `mode: read-write`, so an absent or misspelled mode fails closed.
func TestMountVolOpt(t *testing.T) {
	for _, tc := range []struct {
		from, mode, want string
	}{
		{testMountFrom, "", "ro"},
		{testMountFrom, "read-only", "ro"},
		{testMountFrom, "read-write", "rw"},
		{testMountFrom, "rw", "ro"}, // the grammar is read-write, not rw
		{testMountFrom, "READ-WRITE", "ro"},
	} {
		if got := (Mount{From: tc.from, Mode: tc.mode}).VolOpt(); got != tc.want {
			t.Errorf("Mount{mode:%q}.VolOpt() = %q, want %q", tc.mode, got, tc.want)
		}
	}
}

// TestMountIsPath pins the path/name discriminator the forge lowering reads: a
// `from` carrying a separator is a host path (m6e-only) and anything else is a
// managed-volume name both planes can mount.
func TestMountIsPath(t *testing.T) {
	for from, want := range map[string]bool{
		"/app/ws":      true,
		"app-cache":    false,
		"cache/extra":  true,
		"":             false,
		"/":            true,
		"relative/dir": true,
	} {
		if got := (Mount{From: from}).IsPath(); got != want {
			t.Errorf("Mount{from:%q}.IsPath() = %v, want %v", from, got, want)
		}
	}
}

// TestManifestEnabledFor pins the per-lowering opt-out: gha and forgejo each
// honour their own flag, an absent flag means enabled, and a target that is not a
// cloud lowering (m6e) is never gated by either.
func TestManifestEnabledFor(t *testing.T) {
	no, yes := false, true
	for _, tc := range []struct {
		name   string
		man    Manifest
		target string
		want   bool
	}{
		{"unflagged-gha", Manifest{}, LoweringGHA, true},
		{"unflagged-forgejo", Manifest{}, LoweringForgejo, true},
		{"gha-off", Manifest{GHA: &no}, LoweringGHA, false},
		{"gha-on", Manifest{GHA: &yes}, LoweringGHA, true},
		{"gha-flag-spares-forgejo", Manifest{GHA: &no}, LoweringForgejo, true},
		{"forgejo-off", Manifest{Forgejo: &no}, LoweringForgejo, false},
		{"forgejo-on", Manifest{Forgejo: &yes}, LoweringForgejo, true},
		{"forgejo-flag-spares-gha", Manifest{Forgejo: &no}, LoweringGHA, true},
		{"m6e-ignores-both", Manifest{GHA: &no, Forgejo: &no}, "m6e", true},
		{"lefthook-ignores-both", Manifest{GHA: &no, Forgejo: &no}, "lefthook", true},
	} {
		if got := tc.man.EnabledFor(tc.target); got != tc.want {
			t.Errorf("%s: EnabledFor(%q) = %v, want %v", tc.name, tc.target, got, tc.want)
		}
	}
}

// TestAddAxisKeepsAuthoredOne pins convention-over-configuration in the axis set:
// a DERIVED axis (the arch axis minted from org.projectfile.architecture) never
// overrides an axis the author declared under the same key, and the set stays
// key-sorted for byte-stable output.
func TestAddAxis(t *testing.T) {
	st := &Subtree{Axes: []Axis{{Key: testSeriesAxis, Values: []string{"authored"}}}}
	st.addAxis(Axis{Key: testGoarchAxis, Values: []string{"amd64"}})
	st.addAxis(Axis{Key: testSeriesAxis, Values: []string{"derived"}})
	want := []Axis{{Key: testGoarchAxis, Values: []string{"amd64"}}, {Key: testSeriesAxis, Values: []string{"authored"}}}
	if !reflect.DeepEqual(st.Axes, want) {
		t.Fatalf("axes = %+v, want %+v (derived dropped, keys sorted)", st.Axes, want)
	}
}

// TestDecodeTargetListShapes pins every `needs` wire shape: null and absent are no
// needs, a scalar is the one-entry sequence, a list is name-ordered, and the map
// form decodes name-sorted with its false entries dropped.
func TestDecodeTargetListShapes(t *testing.T) {
	for name, tc := range map[string]struct {
		raw  string
		want []Need
	}{
		"null":              {"null", nil},
		"absent":            {"", nil},
		"scalar":            {`"build"`, []Need{{Target: testBuildTool}}},
		"list":              {`["build","test"]`, []Need{{Target: testBuildTool}, {Target: testLintTool}}},
		"map-true":          {`{"test":true}`, []Need{{Target: testLintTool}}},
		"map-false-dropped": {`{"test":false,"build":true}`, []Need{{Target: testBuildTool}}},
		"map-empty-object":  {`{"test":{}}`, []Need{{Target: testLintTool}}},
		"map-sorted":        {`{"zebra":true,"alpha":true}`, []Need{{Target: "alpha"}, {Target: "zebra"}}},
		"map-args":          {`{"test":{"args":"--flag"}}`, []Need{{Target: testLintTool, Args: "--flag", HasArgs: true}}},
	} {
		got, err := decodeTargetList([]byte(tc.raw))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got %+v, want %+v", name, got, tc.want)
		}
	}
}

// TestDecodeTargetListRejects pins the fail-fast on a value that is neither
// scalar, list nor mapping — a need silently dropped would sever the subtree
// from the goal closure.
func TestDecodeTargetListRejects(t *testing.T) {
	for name, raw := range map[string]string{
		"number":     `42`,
		"bool":       `true`,
		"bad-list":   `[1,2]`,
		"bad-value":  `{"test":"maybe"}`,
		"bad-scalar": `17`,
	} {
		if _, err := decodeTargetList([]byte(raw)); err == nil {
			t.Errorf("%s: got nil error for %s, want a parse failure", name, raw)
		}
	}
}

// TestDecodeNodeTriggersOnGoalOnly pins that trigger data is refused on a
// non-goal node: each goal lowers to its own file, so a schedule on a node no
// file is generated for is dead config that reads as a live trigger.
func TestDecodeNodeTriggersOnGoalOnly(t *testing.T) {
	_, _, err := decodeNodeTriggers("joined", rawNode{Schedule: []rawSchedule{{Cron: testNightlyCron}}})
	if err == nil || !strings.Contains(err.Error(), "only valid on a goal node") {
		t.Fatalf("err=%v, want the goal-only refusal", err)
	}
	// A node declaring neither is the common case: no triggers, no error.
	sched, dispatch, err := decodeNodeTriggers("plain", rawNode{})
	if sched != nil || dispatch != nil || err != nil {
		t.Fatalf("plain node: sched=%v dispatch=%v err=%v, want all nil", sched, dispatch, err)
	}
}

// TestDecodeNodeTriggersCronFieldCount pins the cron arity gate at BOTH edges: a
// 4-field and a 6-field expression are refused, a 5-field one survives, and
// leading/trailing whitespace does not change the count.
func TestDecodeNodeTriggersCronFieldCount(t *testing.T) {
	for name, tc := range map[string]struct {
		cron string
		want []string
		err  bool
	}{
		"five":   {testNightlyCron, []string{testNightlyCron}, false},
		"padded": {"  " + testNightlyCron + "  ", []string{testNightlyCron}, false},
		"four":   {"0 3 * *", nil, true},
		"six":    {testNightlyCron + " 2026", nil, true},
		"empty":  {"", nil, true},
	} {
		sched, _, err := decodeNodeTriggers("ready", rawNode{Goal: true, Schedule: []rawSchedule{{Cron: tc.cron}}})
		if tc.err {
			if err == nil {
				t.Errorf("%s: cron %q parsed, want a field-count failure", name, tc.cron)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if !reflect.DeepEqual(sched, tc.want) {
			t.Errorf("%s: sched=%v, want %v", name, sched, tc.want)
		}
	}
}

// TestDecodeInputTypes pins the manual-dispatch input grammar: an omitted type is
// string, every declared type decodes, an unknown type is refused, and options
// pair with choice and nothing else.
func TestDecodeInputTypes(t *testing.T) {
	if in, err := decodeInput("note", rawInput{}); err != nil || in.Type != InputString || in.HasDefault {
		t.Fatalf("bare input: %+v err=%v, want a default-to-string input", in, err)
	}
	for typ := range InputTypes {
		ri := rawInput{Type: typ}
		if typ == InputChoice {
			ri.Options = []string{"a", "b"}
		}
		if _, err := decodeInput("x", ri); err != nil {
			t.Errorf("type %q: %v", typ, err)
		}
	}
	if _, err := decodeInput("x", rawInput{Type: "colour"}); err == nil || !strings.Contains(err.Error(), "unknown type") {
		t.Errorf("unknown type: err=%v, want the unknown-type refusal", err)
	}
	if _, err := decodeInput("x", rawInput{Type: InputChoice}); err == nil || !strings.Contains(err.Error(), "options are required") {
		t.Errorf("choice without options: err=%v, want the pairing refusal", err)
	}
	if _, err := decodeInput("x", rawInput{Type: InputString, Options: []string{"a"}}); err == nil || !strings.Contains(err.Error(), "options are required") {
		t.Errorf("string with options: err=%v, want the pairing refusal", err)
	}
}

// TestDecodeInputDefaultMustSatisfyType pins every default-validity edge: a boolean
// outside true/false, a non-numeric number, a choice value outside its options,
// and a non-scalar default are all refused, so the run form never ships an
// unselectable default.
func TestDecodeInputDefaultMustSatisfyType(t *testing.T) {
	for name, tc := range map[string]struct {
		in   rawInput
		want string
	}{
		"boolean-ok": {rawInput{Type: InputBoolean, Default: jsonScalar(BoolTrue)}, ""},
		"boolean-bad": {
			rawInput{Type: InputBoolean, Default: jsonScalar("yes")}, "boolean default must be true or false",
		},
		"number-ok":  {rawInput{Type: InputNumber, Default: jsonScalar("3.14")}, ""},
		"number-bad": {rawInput{Type: InputNumber, Default: jsonScalar("many")}, "is not numeric"},
		"choice-ok": {
			rawInput{Type: InputChoice, Options: []string{"a", "b"}, Default: jsonScalar("b")}, "",
		},
		"choice-bad": {
			rawInput{Type: InputChoice, Options: []string{"a", "b"}, Default: jsonScalar("c")}, "is not one of",
		},
	} {
		in, err := decodeInput("x", tc.in)
		if tc.want == "" {
			if err != nil || !in.HasDefault {
				t.Errorf("%s: in=%+v err=%v, want the default accepted", name, in, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err=%v, want one containing %q", name, err, tc.want)
		}
	}
	// A non-scalar default is refused before the type check ever sees it.
	if _, err := decodeInput("x", rawInput{Default: jsonScalar([]string{"a"})}); err == nil || !strings.Contains(err.Error(), "must be a scalar") {
		t.Errorf("non-scalar default: err=%v, want the scalar refusal", err)
	}
}

// jsonScalar marshals one wire value into the raw pointer the decoders take.
func jsonScalar(v any) *json.RawMessage {
	raw := json.RawMessage(mustMarshal(v))
	return &raw
}

// mustMarshal is json.Marshal for a test fixture that cannot fail to encode.
func mustMarshal(v any) []byte {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return raw
}

// TestValidateDefaultAcceptsStringType pins the pass-through half: a string
// input imposes no constraint on its default, so any value is accepted.
func TestValidateDefaultAcceptsStringType(t *testing.T) {
	if err := validateDefault("x", InputString, "anything at all", nil); err != nil {
		t.Fatalf("string default: %v, want no constraint", err)
	}
}

// TestPartialExtraVars pins the partial-override split: a var some cell leaves
// unset is partial (its matrix ref resolves empty, so the render backstops it
// with the build-arg default), a var every cell binds is absent (its ref always
// resolves, so it keeps the bare matrix spelling), and no overrides at all mints
// nothing.
func TestPartialExtraVars(t *testing.T) {
	none := (&Subtree{Axes: []Axis{{Key: testSeriesAxis, Values: []string{testSeriesResolute}}}}).PartialExtraVars()
	if none != nil {
		t.Fatalf("no overrides: %v, want nil", none)
	}
	// Two cells, the var decorated on one: PARTIAL.
	partial := &Subtree{
		Axes: []Axis{{Key: testSeriesAxis, Values: []string{testSeriesResolute, testSeriesNoble}}},
		Overrides: []OverrideEntry{{
			Match: []KV{{Key: testSeriesAxis, Value: testSeriesResolute}},
			Vars:  []KV{{Key: testLLVMExtraVar, Value: "19"}},
		}},
	}
	got := partial.PartialExtraVars()
	if len(got) != 1 || !got[testLLVMExtraVar] {
		t.Fatalf("partial = %v, want LLVM marked partial", got)
	}
	// The same var decorated on EVERY cell: fully enumerated, absent.
	full := &Subtree{
		Axes: []Axis{{Key: testSeriesAxis, Values: []string{testSeriesResolute, testSeriesNoble}}},
		Overrides: []OverrideEntry{
			{Match: []KV{{Key: testSeriesAxis, Value: testSeriesResolute}}, Vars: []KV{{Key: testLLVMExtraVar, Value: "19"}}},
			{Match: []KV{{Key: testSeriesAxis, Value: testSeriesNoble}}, Vars: []KV{{Key: testLLVMExtraVar, Value: "18"}}},
		},
	}
	if got := full.PartialExtraVars(); len(got) != 0 {
		t.Fatalf("full = %v, want no partial var", got)
	}
	// An override row matching no cell binds nothing, so the var stays partial.
	unmatched := &Subtree{
		Axes:      []Axis{{Key: testSeriesAxis, Values: []string{testSeriesResolute}}},
		Overrides: []OverrideEntry{{Match: []KV{{Key: testSeriesAxis, Value: "focal"}}, Vars: []KV{{Key: testLLVMExtraVar, Value: "14"}}}},
	}
	if got := unmatched.PartialExtraVars(); len(got) != 1 || !got[testLLVMExtraVar] {
		t.Fatalf("unmatched = %v, want LLVM partial", got)
	}
	// An axes-less subtree still yields the one empty cell the loop runs over.
	bare := &Subtree{Overrides: []OverrideEntry{{Vars: []KV{{Key: testLLVMExtraVar, Value: "19"}}}}}
	if got := bare.PartialExtraVars(); len(got) != 0 {
		t.Fatalf("axes-less = %v, want LLVM fully bound", got)
	}
}
