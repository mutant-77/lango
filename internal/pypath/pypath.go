package pypath

import (
	"os"
	"path/filepath"
	"strings"
)

// Join joins root and rel with Python pathlib semantics on Windows.
func Join(root, rel string) string {
	if rel == "" {
		return root
	}
	if filepath.VolumeName(rel) != "" {
		return filepath.Clean(rel)
	}
	if os.IsPathSeparator(rel[0]) {
		vol := filepath.VolumeName(root)
		return filepath.Clean(vol + rel)
	}
	return filepath.Join(root, rel)
}

// Resolve resolves symlinks on the longest existing ancestor and appends the non-existent remainder.
func Resolve(p string) string {
	absP, err := filepath.Abs(p)
	if err != nil {
		absP = filepath.Clean(p)
	}
	curr := absP
	var tailParts []string

	for {
		resolved, err := filepath.EvalSymlinks(curr)
		if err == nil {
			if strings.HasPrefix(resolved, `\\?\`) && len(resolved) > 6 && resolved[5] == ':' {
				resolved = resolved[4:]
			}
			if len(tailParts) == 0 {
				return filepath.Clean(resolved)
			}
			parts := make([]string, 0, 1+len(tailParts))
			parts = append(parts, resolved)
			for i := len(tailParts) - 1; i >= 0; i-- {
				parts = append(parts, tailParts[i])
			}
			return filepath.Clean(filepath.Join(parts...))
		}

		parent := filepath.Dir(curr)
		if parent == curr {
			return absP
		}
		tailParts = append(tailParts, filepath.Base(curr))
		curr = parent
	}
}

// Contained reports whether p is inside root, resolving both with Resolve.
func Contained(root, p string) bool {
	resRoot := Resolve(root)
	resP := Resolve(p)

	rel, err := filepath.Rel(resRoot, resP)
	if err != nil {
		return false
	}
	if rel == ".." || strings.HasPrefix(rel, `..\`) || strings.HasPrefix(rel, `../`) {
		return false
	}
	return true
}
