// SPDX-FileCopyrightText: 2026 Damián Búho <damian.buho@proton.me>
//
// SPDX-License-Identifier: MIT

package ci

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"kiota.ch/projectfile/core/v2/pkg/genlog"
	"kiota.ch/projectfile/core/v2/pkg/interp"
)

// headKinds is the build-kind vocabulary of org.projectfile.image.heads, in schema order.
var headKinds = []string{"release", "prerelease", "trunk", "branch"}

// ImageNaming is the tag rules org.projectfile.image declares; the publish action expands them.
type ImageNaming struct {
	Heads   []string // `<kind> <template>…` lines, one per declared kind
	Variant *Variant // nil => every cell publishes the heads alone
}

// Variant is org.projectfile.image.variant with every part resolved to its template.
type Variant struct {
	Parts   []VariantPart
	Join    string
	Aliases string
	Tag     string
	Bare    []string
}

// VariantPart is one variant part: its template over `{AXIS}` and the build-arg defaults those axes fall back to.
type VariantPart struct {
	Name     string
	Template string
	defaults map[string]string
}

// axisRefRe matches one `{AXIS}` placeholder in a part template.
var axisRefRe = regexp.MustCompile(`\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// Fill replaces each `{AXIS}` with its value in vals, else its build-arg default; ok is false when one has neither.
func (p VariantPart) Fill(vals map[string]string) (string, bool) {
	ok := true
	out := axisRefRe.ReplaceAllStringFunc(p.Template, func(m string) string {
		key := m[1 : len(m)-1]
		if v, found := vals[key]; found {
			return v
		}
		if v, found := p.defaults[key]; found {
			return v
		}
		ok = false
		return m
	})
	return out, ok
}

// rawVariant mirrors org.projectfile.image.variant on the wire.
type rawVariant struct {
	Parts   []string `json:"parts"`
	Join    string   `json:"join"`
	Aliases string   `json:"aliases"`
	Tag     string   `json:"tag"`
	Bare    []string `json:"bare"`
}

// imageNaming reads the heads and variant of imageScope; nil when the document declares neither.
func (r *Reader) imageNaming(args []BuildInput) (*ImageNaming, error) {
	raw, err := r.subtree(imageScope)
	if err != nil || len(raw) == 0 {
		return nil, err
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		return nil, fmt.Errorf("%s: parse: %w", imageScope, err)
	}
	naming := &ImageNaming{}
	if h := keys["heads"]; len(h) > 0 {
		var heads map[string][]string
		if err := json.Unmarshal(h, &heads); err != nil {
			return nil, fmt.Errorf("%s.heads: parse: %w", imageScope, err)
		}
		for _, kind := range headKinds {
			if tmpl, ok := heads[kind]; ok && len(tmpl) > 0 {
				genlog.DebugRow("image_heads", strings.Join(tmpl, " "), imageScope+".heads."+kind, "")
				naming.Heads = append(naming.Heads, kind+" "+strings.Join(tmpl, " "))
			}
		}
	}
	if v := keys["variant"]; len(v) > 0 {
		var rv rawVariant
		if err := json.Unmarshal(v, &rv); err != nil {
			return nil, fmt.Errorf("%s.variant: parse: %w", imageScope, err)
		}
		if len(rv.Parts) == 0 || rv.Tag == "" {
			return nil, fmt.Errorf("%s.variant: parts and tag are required", imageScope)
		}
		defaults := map[string]string{}
		for _, a := range args {
			if a.Default != "" {
				defaults[a.Name] = a.Default
			}
		}
		variant := &Variant{Join: rv.Join, Aliases: rv.Aliases, Tag: rv.Tag, Bare: rv.Bare}
		for _, name := range rv.Parts {
			var tmpl string
			if err := json.Unmarshal(keys[name], &tmpl); err != nil || tmpl == "" {
				return nil, fmt.Errorf("%s.variant: part %q is not declared as a string under %s", imageScope, name, imageScope)
			}
			expanded, ok := interp.ExpandIn(r.doc, tmpl, imageScope)
			if !ok {
				return nil, fmt.Errorf("%s.%s: template %q left unresolved", imageScope, name, tmpl)
			}
			genlog.DebugRow("image_variant_part", expanded, imageScope+"."+name, "")
			variant.Parts = append(variant.Parts, VariantPart{Name: name, Template: expanded, defaults: defaults})
		}
		naming.Variant = variant
	}
	if len(naming.Heads) == 0 && naming.Variant == nil {
		return nil, nil
	}
	return naming, nil
}
