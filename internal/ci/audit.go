// SPDX-FileCopyrightText: 2026 Damián Búho <damian.buho@proton.me>
//
// SPDX-License-Identifier: MIT

package ci

import (
	"strings"

	"kiota.ch/projectfile/core/v2/pkg/genlog"
	"kiota.ch/projectfile/core/v2/pkg/interp"
)

// auditBranchField overrides which branch's published image the `audited` re-scan reads; absent => the declared trunk.
const auditBranchField = "org.ci.audited.branch"

// tagPlaceholder is the image part every sink ref template spells its tag with.
const tagPlaceholder = "${tag}"

// branchSlug spells a branch as oci-push does: each byte outside [a-zA-Z0-9._-] flattened to `-`, clamped to 110 bytes.
func branchSlug(branch string) string {
	slug := []byte(branch)
	for i, c := range slug {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '.' && c != '_' && c != '-' {
			slug[i] = '-'
		}
	}
	if len(slug) > 110 {
		slug = slug[:110]
	}
	return string(slug)
}

// auditBranch reads org.ci.audited.branch, else the declared trunk; empty => the audit keeps the image's own tag.
func (r *Reader) auditBranch(primary []string) string {
	if raw, ok := interp.ExpandIn(r.doc, "${"+auditBranchField+"}", imageScope); ok && strings.TrimSpace(raw) != "" {
		genlog.Decision("audit_branch", strings.TrimSpace(raw), auditBranchField, "")
		return strings.TrimSpace(raw)
	}
	if len(primary) > 0 {
		genlog.Decision("audit_branch", primary[0], "org.projectfile.release.trunk", auditBranchField+" unset")
		return primary[0]
	}
	genlog.Decision("audit_branch", "<none>", auditBranchField, "no trunk declared, audit keeps "+tagPlaceholder)
	return ""
}

// auditHead is the first tag a push of branch publishes: its declared head, else oci-push's own default.
func auditHead(naming *ImageNaming, branch string, primary []string) string {
	kind, head := "branch", "latest-${branch}"
	for _, p := range primary {
		if p == branch {
			kind, head = "trunk", "edge"
		}
	}
	if naming != nil {
		for _, line := range naming.Heads {
			if fields := strings.Fields(line); len(fields) > 1 && fields[0] == kind {
				head = fields[1]
			}
		}
	}
	head = strings.ReplaceAll(head, "${branch}", branchSlug(branch))
	genlog.Decision("audit_head", head, imageScope+".heads."+kind, "branch="+branch)
	return head
}

// auditTag composes the tag the `audited` re-scan reads, with `{AXIS}` left for render; empty => no audit branch.
func (r *Reader) auditTag(naming *ImageNaming, primary []string) string {
	branch := r.auditBranch(primary)
	if branch == "" {
		return ""
	}
	head := auditHead(naming, branch, primary)
	tmpl := head
	if naming != nil && naming.Variant != nil {
		tmpl = tagPlaceholder
		bare := false
		for _, b := range naming.Variant.Bare {
			bare = bare || b == head
		}
		if !bare {
			tmpl = strings.NewReplacer("${head}", head, "${variant}", tagPlaceholder).Replace(naming.Variant.Tag)
		}
	}
	tag, ok := interp.ExpandIn(r.doc, tmpl, imageScope)
	if !ok {
		genlog.Warn("audit tag left unresolved, audit keeps the image tag", "template", tmpl, "composed", tag)
		return ""
	}
	genlog.Decision("audit_tag", tag, auditBranchField, "head="+head)
	return tag
}
