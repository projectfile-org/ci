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

// namingDoc writes a projectfile whose org.projectfile.image subtree is body, and
// returns its path.
func namingDoc(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "projectfile.yaml")
	doc := "$schema: https://projectfile.org/schema/v1.json\nidentity:\n  name: demo\n  version: 1.0.0\n  released: \"2026-01-01\"\n" + body
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return path
}

// loadNaming reads one image-naming subtree through the public loader.
func loadNaming(t *testing.T, body string) (*ImageNaming, error) {
	t.Helper()
	b, err := LoadBuildWithOptions(namingDoc(t, body), "gha", projectfile.ReadOptions{})
	if err != nil {
		return nil, err
	}
	if b == nil {
		return nil, nil
	}
	return b.Naming, nil
}

// TestImageNamingReadsHeadsInSchemaOrder pins the head vocabulary: the declared
// kinds are emitted in the schema's order regardless of how the author sorted
// them, each as `<kind> <template…>`.
func TestImageNamingReadsHeadsInSchemaOrder(t *testing.T) {
	n, err := loadNaming(t, `org:
  projectfile:
    image:
      path: ${org}/${name}
      heads:
        trunk: ["${path}:${version}"]
        release: ["${path}:${version}", "${path}:X", "${path}:${major}.${minor}"]
        prerelease: ["${path}:${version}-rc.${ci_number}"]
`)
	if err != nil {
		t.Fatalf("LoadBuild: %v", err)
	}
	want := []string{
		"release ${path}:${version} ${path}:X ${path}:${major}.${minor}",
		"prerelease ${path}:${version}-rc.${ci_number}",
		"trunk ${path}:${version}",
	}
	if strings.Join(n.Heads, "|") != strings.Join(want, "|") {
		t.Fatalf("heads = %#v, want %#v", n.Heads, want)
	}
	if n.Variant != nil {
		t.Fatalf("variant = %+v, want nil when the document declares none", n.Variant)
	}
}

// TestImageNamingIgnoresEmptyAndUnknownHeads pins the graceful half: the head
// vocabulary is CLOSED, so a kind with an empty template list and a kind outside
// the vocabulary mint no head line — a typo drops a head rather than naming a tag
// shape the publish action cannot expand.
func TestImageNamingIgnoresEmptyAndUnknownHeads(t *testing.T) {
	n, err := loadNaming(t, `org:
  projectfile:
    image:
      path: ${org}/${name}
      heads:
        trunk: []
        nightly: ["${path}:nightly"]
        release: ["${path}:${version}"]
`)
	if err != nil {
		t.Fatalf("LoadBuild: %v", err)
	}
	if len(n.Heads) != 1 || n.Heads[0] != "release ${path}:${version}" {
		t.Fatalf("heads = %#v, want the single release head", n.Heads)
	}
}

// TestImageNamingAbsentIsNoOp pins the whole-subtree absence: a project
// declaring neither heads nor variant reports no naming at all, which is what
// keeps LoadBuild returning nil for a document with nothing to publish.
func TestImageNamingAbsentIsNoOp(t *testing.T) {
	for name, body := range map[string]string{
		"no-image-subtree": "org:\n  projectfile:\n    architecture: [amd64]\n",
		"empty-subtree":    "org:\n  projectfile:\n    image: {}\n",
		"scalar-subtree":   "org:\n  projectfile:\n    image: \"demo/app\"\n",
		"unrelated-keys":   "org:\n  projectfile:\n    image:\n      path: demo/app\n      name: app\n",
	} {
		n, err := loadNaming(t, body)
		if err != nil {
			t.Fatalf("%s: LoadBuild: %v", name, err)
		}
		if n != nil {
			t.Fatalf("%s: naming = %+v, want nil", name, n)
		}
	}
}

// TestImageNamingHeadsOnlyKeepsVariantNil pins that heads alone is a legal
// declaration: every cell publishes the heads and the variant machinery stays off.
func TestImageNamingHeadsOnlyKeepsVariantNil(t *testing.T) {
	n, err := loadNaming(t, `org:
  projectfile:
    image:
      heads:
        trunk: ["${path}:dev"]
`)
	if err != nil {
		t.Fatalf("LoadBuild: %v", err)
	}
	if n == nil || n.Variant != nil || len(n.Heads) != 1 {
		t.Fatalf("naming = %+v, want one head and no variant", n)
	}
}

// TestImageNamingReadsVariant pins the variant lowering: parts resolve in the
// declared order, each template's document references expanded, and
// join/aliases/tag/bare carry through verbatim.
func TestImageNamingReadsVariant(t *testing.T) {
	n, err := loadNaming(t, `org:
  projectfile:
    image:
      path: ${org}/${name}
      distro: "${identity.name}-{DISTRO}"
      variant:
        parts: [distro]
        join: "-"
        aliases: "latest"
        tag: "${path}:{DISTRO}"
        bare: ["${path}:${version}"]
`)
	if err != nil {
		t.Fatalf("LoadBuild: %v", err)
	}
	if n.Variant == nil {
		t.Fatal("variant = nil, want the declared variant")
	}
	v := n.Variant
	if v.Join != "-" || v.Aliases != "latest" {
		t.Fatalf("join=%q aliases=%q, want %q / %q", v.Join, v.Aliases, "-", "latest")
	}
	if v.Tag != "${path}:{DISTRO}" {
		t.Fatalf("tag = %q", v.Tag)
	}
	if len(v.Bare) != 1 || v.Bare[0] != "${path}:${version}" {
		t.Fatalf("bare = %#v", v.Bare)
	}
	if len(v.Parts) != 1 || v.Parts[0].Name != "distro" {
		t.Fatalf("parts = %+v, want the single distro part", v.Parts)
	}
	if v.Parts[0].Template != "demo-{DISTRO}" {
		t.Fatalf("part template %q, want it read verbatim", v.Parts[0].Template)
	}
}

// TestImageNamingVariantPartExpandsDocumentRefs pins that a part template's
// `${…}` references are resolved against the merged document at Load time, so
// the renderer never carries a document reference into an image tag. Only the
// image's own `{AXIS}` placeholders survive: `${path}` is not a document
// reference, and a template naming one is refused rather than shipped literal.
func TestImageNamingVariantPartExpandsDocumentRefs(t *testing.T) {
	n, err := loadNaming(t, `org:
  projectfile:
    image:
      path: ${org}/${name}
      distro: "${identity.name}-{DISTRO}"
      variant:
        parts: [distro]
        tag: "${path}:x"
`)
	if err != nil {
		t.Fatalf("LoadBuild: %v", err)
	}
	if got := n.Variant.Parts[0].Template; got != "demo-{DISTRO}" {
		t.Fatalf("part template %q, want the document reference expanded to %q", got, "demo-{DISTRO}")
	}
}

// TestVariantPartFillPrefersValueThenDefault pins the substitution precedence: a
// matrix value wins, a declared build-arg default fills a placeholder the value
// set does not name, and a placeholder with neither is reported unresolvable.
func TestVariantPartFillPrefersValueThenDefault(t *testing.T) {
	part := VariantPart{Name: "distro", Template: "{DISTRO}-{SUFFIX}", defaults: map[string]string{"SUFFIX": "glibc"}}
	got, ok := part.Fill(map[string]string{"DISTRO": "noble"})
	if !ok || got != "noble-glibc" {
		t.Fatalf("Fill = %q ok=%v, want %q ok=true", got, ok, "noble-glibc")
	}
	// A value overrides the declared default for the same key.
	got, ok = part.Fill(map[string]string{"DISTRO": "noble", "SUFFIX": "musl"})
	if !ok || got != "noble-musl" {
		t.Fatalf("Fill = %q ok=%v, want the value to win over the default", got, ok)
	}
	if got, ok = part.Fill(nil); ok || got != "{DISTRO}-glibc" {
		t.Fatalf("Fill(nil) = %q ok=%v, want the default applied and ok=false for the unnamed axis", got, ok)
	}
}

// TestVariantPartFillLeavesNonAxisText pins that only `{AXIS}` placeholders are
// substituted — a brace that does not spell an identifier is part of the tag
// template, not an axis.
func TestVariantPartFillLeavesNonAxisText(t *testing.T) {
	part := VariantPart{Template: "{not an axis}-{9lives}-{}-{$DOLLAR}"}
	got, ok := part.Fill(map[string]string{"DOLLAR": "x", "9lives": "y"})
	if !ok || got != "{not an axis}-{9lives}-{}-{$DOLLAR}" {
		t.Fatalf("Fill = %q ok=%v, want only the well-formed axis name substituted", got, ok)
	}
}

// TestImageNamingVariantRequiresPartsAndTag pins the fail-fast: a variant without
// parts or without a tag cannot name a cell, so it is refused rather than
// rendered into an empty tag.
func TestImageNamingVariantRequiresPartsAndTag(t *testing.T) {
	for name, variant := range map[string]string{
		"no-parts": "      variant:\n        parts: []\n        tag: \"${path}:x\"\n",
		"no-tag":   "      variant:\n        parts: [distro]\n",
	} {
		body := "org:\n  projectfile:\n    image:\n      path: ${org}/${name}\n" + variant
		if _, err := loadNaming(t, body); err == nil || !strings.Contains(err.Error(), "parts and tag are required") {
			t.Errorf("%s: err=%v, want the required-fields refusal", name, err)
		}
	}
}

// TestImageNamingVariantPartMustBeDeclared pins that a part named in `parts` but
// absent (or non-string) beside `variant` is refused: a silently dropped part
// would mint a tag missing a segment.
func TestImageNamingVariantPartMustBeDeclared(t *testing.T) {
	for name, body := range map[string]string{
		"missing": `org:
  projectfile:
    image:
      path: ${org}/${name}
      variant:
        parts: [distro]
        tag: "${path}:x"
`,
		"empty": `org:
  projectfile:
    image:
      path: ${org}/${name}
      distro: ""
      variant:
        parts: [distro]
        tag: "${path}:x"
`,
		"not-string": `org:
  projectfile:
    image:
      path: ${org}/${name}
      distro: [a, b]
      variant:
        parts: [distro]
        tag: "${path}:x"
`,
	} {
		_, err := loadNaming(t, body)
		if err == nil || !strings.Contains(err.Error(), "is not declared as a string") {
			t.Errorf("%s: err=%v, want the undeclared-part refusal", name, err)
		}
	}
}

// TestImageNamingVariantPartTemplateMustResolve pins that a part template the
// document cannot answer is refused: it would otherwise ship a literal `${…}`
// into every image tag.
func TestImageNamingVariantPartTemplateMustResolve(t *testing.T) {
	body := `org:
  projectfile:
    image:
      path: ${org}/${name}
      distro: "${path}-${no.such.ref}"
      variant:
        parts: [distro]
        tag: "${path}:x"
`
	if _, err := loadNaming(t, body); err == nil || !strings.Contains(err.Error(), "left unresolved") {
		t.Fatalf("err=%v, want the unresolved-template refusal", err)
	}
}

// TestImageNamingVariantDefaultsComeFromBuildArgs pins that a part placeholder
// falls back to the build-arg default of the same NAME, which is how
// `B19_UBUNTU_SERIES` reaches a tag without a matrix axis.
func TestImageNamingVariantDefaultsComeFromBuildArgs(t *testing.T) {
	body := `org:
  projectfile:
    build:
      args:
        B19_UBUNTU_SERIES: {default: resolute}
    image:
      path: ${org}/${name}
      distro: "${identity.name}-{B19_UBUNTU_SERIES}"
      variant:
        parts: [distro]
        tag: "${path}:x"
`
	b, err := LoadBuildWithOptions(namingDoc(t, body), "gha", projectfile.ReadOptions{})
	if err != nil {
		t.Fatalf("LoadBuild: %v", err)
	}
	if b == nil || b.Naming == nil || b.Naming.Variant == nil {
		t.Fatalf("build = %+v, want a naming variant", b)
	}
	got, ok := b.Naming.Variant.Parts[0].Fill(nil)
	if !ok || got != "demo-resolute" {
		t.Fatalf("Fill = %q ok=%v, want the build-arg default substituted", got, ok)
	}
}

// TestImageNamingRejectsMalformedSubtrees pins the parse failures: a heads value
// that is not the kind→list map, and a variant value that is not an object, each
// naming the field it read.
func TestImageNamingRejectsMalformedSubtrees(t *testing.T) {
	for name, tc := range map[string]struct{ body, want string }{
		"heads-not-a-map": {
			body: "org:\n  projectfile:\n    image:\n      path: p\n      heads: [a, b]\n",
			want: "image.heads: parse",
		},
		"variant-not-a-map": {
			body: "org:\n  projectfile:\n    image:\n      path: p\n      variant: [a]\n",
			want: "image.variant: parse",
		},
	} {
		_, err := loadNaming(t, tc.body)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err=%v, want one containing %q", name, err, tc.want)
		}
	}
}
