// Package layout builds a deterministic destination folder tree shared by the
// bucket and filesystem targets. Paths are fixed (no per-run identifier) so that
// re-running overwrites in place rather than accumulating data.
package layout

import (
	"fmt"
	"path"
)

// Plan holds the deterministic leaf directories.
type Plan struct {
	Tree []string // leaf relative dirs; a single "" element when depth == 0 (flat)
}

// NewPlan builds a deterministic depth x breadth folder tree.
func NewPlan(depth, breadth int) Plan {
	if breadth < 1 {
		breadth = 1
	}
	return Plan{Tree: buildTree(depth, breadth)}
}

func buildTree(depth, breadth int) []string {
	if depth <= 0 {
		return []string{""}
	}
	leaves := []string{""}
	for d := 0; d < depth; d++ {
		next := make([]string, 0, len(leaves)*breadth)
		for _, base := range leaves {
			for b := 1; b <= breadth; b++ {
				next = append(next, path.Join(base, fmt.Sprintf("dir_%d", b)))
			}
		}
		leaves = next
	}
	return leaves
}

// Rel returns the destination-relative path (no base prefix) for the i-th file
// of a stably-ordered file list: <leaf>/<rel>.
func (p Plan) Rel(i int, rel string) string {
	leaf := p.Tree[i%len(p.Tree)]
	return path.Join(leaf, rel)
}

// LeafCount returns the number of leaf directories (breadth^depth).
func (p Plan) LeafCount() int { return len(p.Tree) }
