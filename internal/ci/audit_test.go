// SPDX-FileCopyrightText: 2026 Damián Búho <damian.buho@proton.me>
//
// SPDX-License-Identifier: MIT

package ci

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kiota.ch/projectfile/core/v2/pkg/projectfile"
)

// auditDoc writes a projectfile with the given `org:` body and reads it back.
func auditDoc(t *testing.T, org string) *Reader {
	t.Helper()
	path := filepath.Join(t.TempDir(), "projectfile.yaml")
	body := "$schema: https://projectfile.org/schema/v1.json\nidentity:\n  namespace: org.projectfile\n  name: ubuntu\n" +
		"repositories:\n  - role: origin\n    type: git\n    url: ssh://git@kiota.ch/b19/ubuntu.git\norg:\n" + org
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	doc, err := projectfile.Read(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return &Reader{doc: doc}
}

// auditTagOf resolves the audit tag the way the Build load does.
func auditTagOf(t *testing.T, r *Reader) string {
	t.Helper()
	naming, err := r.imageNaming(nil)
	if err != nil {
		t.Fatalf("imageNaming: %v", err)
	}
	return r.auditTag(naming, r.primaryBranches())
}

// branchDev and branchMain are the trunk and stable branch names the fixtures declare.
const (
	branchDev  = "dev"
	branchMain = "main"
)

// heads is the m6e container trait's head set.
const heads = `      heads:
        release: ["${version}", latest]
        trunk: ["${branch}"]
        branch: ["${branch}"]
`

func TestAuditTag(t *testing.T) {
	cases := []struct {
		name, org, want string
	}{
		{"trunk head", "  projectfile:\n    release:\n      trunk: dev\n    image:\n      tag: latest\n" + heads, branchDev},
		{"override wins over trunk", "  ci:\n    audited:\n      branch: main\n  projectfile:\n    release:\n      trunk: dev\n    image:\n      tag: latest\n" + heads, branchMain},
		{"override without trunk", "  ci:\n    audited:\n      branch: next\n  projectfile:\n    image:\n      tag: latest\n" + heads, "next"},
		{"trunk without heads takes oci-push edge", "  projectfile:\n    release:\n      trunk: dev\n    image:\n      tag: latest\n", "edge"},
		{"branch without heads takes oci-push preview", "  ci:\n    audited:\n      branch: feature/x\n  projectfile:\n    release:\n      trunk: dev\n    image:\n      tag: latest\n", "latest-feature-x"},
		{"series variant", "  projectfile:\n    release:\n      trunk: dev\n    image:\n      series: \"pg{O9S_POSTGRESQL_SERIES}\"\n      tag: ${series}\n" + heads +
			"      variant:\n        parts: [series]\n        tag: \"${head}-${variant}\"\n        bare: [latest]\n", "dev-pg{O9S_POSTGRESQL_SERIES}"},
		{"bare head is the variant alone", "  projectfile:\n    release:\n      trunk: dev\n    image:\n      series: \"pg{O9S_POSTGRESQL_SERIES}\"\n      tag: ${series}\n" + heads +
			"      variant:\n        parts: [series]\n        tag: \"${head}-${variant}\"\n        bare: [dev]\n", "pg{O9S_POSTGRESQL_SERIES}"},
		{"no branch keeps the image tag", "  projectfile:\n    image:\n      tag: latest\n" + heads, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := auditTagOf(t, auditDoc(t, c.org)); got != c.want {
				t.Fatalf("auditTag: got %q, want %q", got, c.want)
			}
		})
	}
}

func TestBranchSlugClamp(t *testing.T) {
	for _, n := range []int{110, 111} {
		if got := len(branchSlug(strings.Repeat("a", n))); got != 110 {
			t.Fatalf("branchSlug(%d bytes): got %d bytes, want 110", n, got)
		}
	}
	if got := branchSlug("feat/ñ x"); got != "feat----x" {
		t.Fatalf("branchSlug: got %q", got)
	}
}

func TestPullRefsReadAuditTag(t *testing.T) {
	r := auditDoc(t, "  projectfile:\n    image:\n      org: b19\n      name: ubuntu\n      path: ${org}/${name}\n      tag: latest\n"+
		"    sinks:\n      kiota:\n        ref: kiota.ch/${path}:${tag}\n    publish:\n      kiota:\n        push: [kiota]\n        pull: kiota\n")
	for tag, want := range map[string]string{"dev": "kiota.ch/b19/ubuntu:dev", "": "kiota.ch/b19/ubuntu:latest"} {
		got, err := r.pullRefs(tag)
		if err != nil {
			t.Fatalf("pullRefs: %v", err)
		}
		if got[LoweringForgejo].Ref != want {
			t.Fatalf("pullRefs(%q): got %#v, want %q", tag, got, want)
		}
	}
}
