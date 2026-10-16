// SPDX-FileCopyrightText: 2026 Damián Búho <damian.buho@proton.me>
//
// SPDX-License-Identifier: MIT

package ci

import (
	"encoding/json"
	"fmt"

	"kiota.ch/projectfile/core/v2/pkg/genlog"
	"kiota.ch/projectfile/core/v2/pkg/projectfile"
)

// Cell axes a `matrix: {artifact: NAME}` node fans over, and the exclude-cell key each one reads.
const (
	TargetOSAxis   = "TARGET_OS"
	TargetArchAxis = "TARGET_ARCH"
)

var artifactAxes = []struct{ axis, list, cell string }{
	{TargetArchAxis, "architecture", "arch"},
	{TargetOSAxis, "operating-system", "os"},
}

// expandArtifactMatrices rewrites every node `matrix: {artifact: NAME}` into own axes and excludes read from that artifact record.
func (r *Reader) expandArtifactMatrices(raw []byte) ([]byte, error) {
	var ci map[string]any
	if err := json.Unmarshal(raw, &ci); err != nil {
		return nil, fmt.Errorf("decode org.projectfile.ci: %w", err)
	}
	nodes, _ := ci["nodes"].(map[string]any)
	changed := false
	for name, n := range nodes {
		node, _ := n.(map[string]any)
		m, _ := node["matrix"].(map[string]any)
		artifact, ok := m["artifact"].(string)
		if !ok {
			continue
		}
		if len(m) != 1 {
			return nil, fmt.Errorf("node %q: matrix.artifact is exclusive — the record already states every cell", name)
		}
		expanded, err := r.artifactMatrix(artifact)
		if err != nil {
			return nil, fmt.Errorf("node %q: %w", name, err)
		}
		genlog.Debug("node matrix from artifact record", "node", name, "artifact", artifact, "axes", expanded["axes"], "exclude", expanded["exclude"])
		node["matrix"] = expanded
		changed = true
	}
	if !changed {
		return raw, nil
	}
	return json.Marshal(ci)
}

// artifactMatrix builds `{axes, exclude}` from one artifact's platform lists, falling back to the project-level ones.
func (r *Reader) artifactMatrix(name string) (map[string]any, error) {
	ext, _ := projectfile.Extension(r.doc, archNamespace)
	arts, _ := ext["artifacts"].(map[string]any)
	art, ok := arts[name].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("matrix.artifact %q: no such org.projectfile.artifacts entry", name)
	}
	axes := map[string]any{}
	for _, a := range artifactAxes {
		list, ok := art[a.list].([]any)
		if !ok {
			list, ok = ext[a.list].([]any)
		}
		if !ok && a.list == "operating-system" {
			list, ok = []any{"linux"}, true
		}
		if !ok || len(list) == 0 {
			return nil, fmt.Errorf("matrix.artifact %q: declares no %s, here or at org.projectfile.%s", name, a.list, a.list)
		}
		axes[a.axis] = list
	}
	cells, _ := art["exclude"].([]any)
	exclude := make([]any, 0, len(cells))
	for i, c := range cells {
		cell, _ := c.(map[string]any)
		row := map[string]any{}
		for _, a := range artifactAxes {
			if v, ok := cell[a.cell]; ok {
				row[a.axis] = v
			}
		}
		if len(row) == 0 {
			return nil, fmt.Errorf("matrix.artifact %q: exclude[%d] names neither os nor arch", name, i)
		}
		exclude = append(exclude, row)
	}
	out := map[string]any{"axes": axes}
	if len(exclude) > 0 {
		out["exclude"] = exclude
	}
	return out, nil
}
