// SPDX-FileCopyrightText: 2026 Damián Búho <damian.buho@proton.me>
//
// SPDX-License-Identifier: MIT

package render

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"projectfile.org/projectfile/ci/internal/ci"
	"projectfile.org/projectfile/ci/internal/resolve"
)

// variantDoc is b19/php's shape: no ci.image, axes declared sapi-first, variant series-first.
const variantDoc = `
$schema: https://projectfile.org/schema/v1.json
identity: {namespace: org.test, name: php}
license: {spdx: MIT}
org:
  projectfile:
    image:
      org: b19
      name: php
      path: ${org}/${name}
      sapi: "{B19_PHP_SAPI}"
      series: "{B19_PHP_SERIES}"
      tag: ${series}-${sapi}
      variant: {parts: [series, sapi], join: "-", tag: "${head}-${variant}"}
    ci:
      matrix: {axes: {B19_PHP_SAPI: [cli, fpm], B19_PHP_SERIES: ["8.5", "8.4"]}}
      tools:
        container-build: {action: container-build}
        oci-push: {action: oci-push}
        dc-up-d: {fuse: live, run: docker compose up --detach}
        dc-down: {fuse: live, when: always, run: docker compose down --volumes}
      nodes:
        image-built: {matrix: true, needs: {container-build: true}}
        container-is-verified: {matrix: true, needs: {image-built: true, dc-up-d: true, dc-down: true}}
        published: {matrix: true, goal: true, needs: {container-is-verified: true, oci-push: true}}
`

// TestVariantNamesTheWorkingTag pins one image name for every cell, told apart by the declared variant in the tag.
func TestVariantNamesTheWorkingTag(t *testing.T) {
	pf := filepath.Join(t.TempDir(), "projectfile.yaml")
	if err := os.WriteFile(pf, []byte(variantDoc), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := ci.Load(pf)
	if err != nil {
		t.Fatalf("Load must accept a bare image under a declared variant: %v", err)
	}
	if st.Image != "b19/php" || !st.Variant {
		t.Fatalf("image = %q variant = %v, want b19/php with a variant", st.Image, st.Variant)
	}
	b, err := ci.LoadBuild(pf, ci.LoweringForgejo)
	if err != nil {
		t.Fatal(err)
	}
	rm, err := resolve.Resolve(st)
	if err != nil {
		t.Fatal(err)
	}
	m := Build(rm, st, b)
	env := map[string]string{}
	for _, e := range jobOf(m, testDCDown).Env {
		env[e.Key] = e.Value
	}
	cell := "-${{ matrix.B19_PHP_SERIES }}-${{ matrix.B19_PHP_SAPI }}"
	if want := "b19/php:" + selfImageTagExpr(cell[1:], ""); inlineHoists(env[ImageFullnameEnv]) != inlineHoists(want) {
		t.Errorf("%s = %q, want %q", ImageFullnameEnv, env[ImageFullnameEnv], want)
	}
	if !strings.HasPrefix(env[ComposeProjectEnv], "ci-b19-php-${{ matrix.B19_PHP_SERIES_SLUG }}-${{ matrix.B19_PHP_SAPI }}-") {
		t.Errorf("%s = %q, want the slugged variant in the stack identity", ComposeProjectEnv, env[ComposeProjectEnv])
	}
	out, err := Workflow(m, Targets[TargetForgejo], ci.Platform{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "b19/php/") || strings.Contains(string(out), "b19/php-") {
		t.Errorf("rendered workflow still names a per-cell image:\n%s", out)
	}
	if n := strings.Count(string(out), "image: b19/php:"); n < 2 {
		t.Errorf("want the build stamp and the push input on b19/php, got %d:\n%s", n, out)
	}
}
