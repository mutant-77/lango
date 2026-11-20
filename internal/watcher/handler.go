package watcher

import (
	"os"
	"path/filepath"
	"strings"
)

var ignoreMap = map[string]struct{}{
	".git":         {},
	"node_modules": {},
	"__pycache__":  {},
	"venv":         {},
	".venv":        {},
	"dist":         {},
	"build":        {},
	".idea":        {},
	".vscode":      {},
}

func RelPath(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	rel = filepath.ToSlash(rel)
	if rel == "." {
		return ""
	}
	return rel
}

func isIgnored(rel string) bool {
	if rel == "" {
		return false
	}
	for _, part := range strings.Split(rel, "/") {
		if _, ok := ignoreMap[part]; ok {
			return true
		}
	}
	return false
}

func isGitState(rel string) bool {
	return rel == ".git/HEAD" || rel == ".git/index" || strings.HasPrefix(rel, ".git/refs/")
}

func safeStat(path string) (int, any) {
	fi, err := os.Stat(path)
	if err != nil {
		return 0, nil
	}
	mtime := int(fi.ModTime().Unix())
	if fi.IsDir() {
		return mtime, nil
	}
	return mtime, fi.Size()
}

func safeMtime(path string) int {
	fi, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return int(fi.ModTime().Unix())
}

type Handler struct {
	root string
	cb   func(map[string]any)
}

func NewHandler(root string, cb func(map[string]any)) *Handler {
	return &Handler{
		root: root,
		cb:   cb,
	}
}

func (h *Handler) OnCreated(path string, isDir bool) {
	rel := RelPath(h.root, path)
	if isGitState(rel) {
		h.cb(map[string]any{"kind": "git-changed"})
		return
	}
	if isIgnored(rel) {
		return
	}
	mtime, size := safeStat(path)
	if isDir {
		size = nil
	}
	h.cb(map[string]any{
		"kind":   "created",
		"path":   rel,
		"is_dir": isDir,
		"mtime":  mtime,
		"size":   size,
	})
}

func (h *Handler) OnDeleted(path string, isDir bool) {
	rel := RelPath(h.root, path)
	if isGitState(rel) {
		h.cb(map[string]any{"kind": "git-changed"})
		return
	}
	if isIgnored(rel) {
		return
	}
	h.cb(map[string]any{
		"kind":   "deleted",
		"path":   rel,
		"is_dir": isDir,
	})
}

func (h *Handler) OnModified(path string, isDir bool) {
	rel := RelPath(h.root, path)
	if isGitState(rel) {
		h.cb(map[string]any{"kind": "git-changed"})
		return
	}
	if isIgnored(rel) {
		return
	}
	if isDir {
		h.cb(map[string]any{
			"kind": "dir-changed",
			"path": rel,
		})
		return
	}
	h.cb(map[string]any{
		"kind":   "modified",
		"path":   rel,
		"is_dir": false,
		"mtime":  safeMtime(path),
	})
}

func (h *Handler) OnMoved(src, dest string, isDir bool) {
	relSrc := RelPath(h.root, src)
	relDest := RelPath(h.root, dest)
	if isGitState(relDest) || isGitState(relSrc) {
		h.cb(map[string]any{"kind": "git-changed"})
		return
	}
	if isIgnored(relSrc) || isIgnored(relDest) {
		return
	}
	h.cb(map[string]any{
		"kind":   "moved",
		"path":   relSrc,
		"dest":   relDest,
		"is_dir": isDir,
	})
}

func (h *Handler) Handle(ev Event) {
	switch ev.Type {
	case EventCreated:
		h.OnCreated(ev.Path, ev.IsDir)
	case EventDeleted:
		h.OnDeleted(ev.Path, ev.IsDir)
	case EventModified:
		h.OnModified(ev.Path, ev.IsDir)
	case EventMoved:
		h.OnMoved(ev.Path, ev.Dest, ev.IsDir)
	}
}
