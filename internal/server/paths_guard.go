package server

import (
	"lango/internal/pypath"
)

// resolve resolves symlinks on the longest existing ancestor and appends the non-existent remainder.
func resolve(p string) string {
	return pypath.Resolve(p)
}

// pyJoin joins root and rel with Python pathlib semantics on Windows.
func pyJoin(root, rel string) string {
	return pypath.Join(root, rel)
}

// Contained reports whether p is inside root.
func Contained(root, p string) bool {
	return pypath.Contained(root, p)
}
