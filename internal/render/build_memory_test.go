// SPDX-FileCopyrightText: 2026 Damián Búho <damian.buho@proton.me>
//
// SPDX-License-Identifier: MIT

package render

import (
	"strings"
	"testing"

	"projectfile.org/projectfile/ci/v3/internal/ci"
	"projectfile.org/projectfile/ci/v3/internal/resolve"
)

// TestContainerBuildMemory pins the memory input: only a buildah build on a declared lowering carries it.
func TestContainerBuildMemory(t *testing.T) {
	src := `{
	  "image": "b19/fd",
	  "tools": {"container-build": {"action": "container-build"}},
	  "nodes": {"published": {"goal": true, "needs": {"container-build": true}}}
	}`
	st, err := ci.Parse([]byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rm, err := resolve.Resolve(st)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	declared := &ci.Build{BuildMemory: map[string]string{"forgejo": "16g"}}
	const input = "memory: 16g"
	for name, tc := range map[string]struct {
		build    *ci.Build
		target   string
		platform ci.Platform
		want     bool
	}{
		"forgejo declared":       {declared, TargetForgejo, ci.Platform{}, true},
		"forgejo undeclared":     {&ci.Build{}, TargetForgejo, ci.Platform{}, false},
		"forgejo on buildx":      {declared, TargetForgejo, ci.Platform{Builder: BackendBuildx}, false},
		"gha has no key":         {declared, TargetGHA, ci.Platform{}, false},
		"gha buildah has no key": {declared, TargetGHA, ci.Platform{Builder: BackendBuildah}, false},
	} {
		out, err := Workflow(Build(rm, st, tc.build), Targets[tc.target], tc.platform)
		if err != nil {
			t.Fatalf("%s: render: %v", name, err)
		}
		if got := strings.Contains(string(out), input); got != tc.want {
			t.Errorf("%s: %q rendered = %v, want %v:\n%s", name, input, got, tc.want, out)
		}
	}
}
