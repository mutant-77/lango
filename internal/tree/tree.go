package tree

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"lango/internal/pypath"
)

var ignoreNames = map[string]bool{
	".git":         true,
	"node_modules": true,
	"__pycache__":  true,
	"venv":         true,
	".venv":        true,
	"dist":         true,
	"build":        true,
	".idea":        true,
	".vscode":      true,
}

// Entry represents a single file or directory item.
type Entry struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	IsDir bool   `json:"is_dir"`
	Mtime int64  `json:"mtime"`
	Size  *int64 `json:"size"`
}

type itemWithSortKey struct {
	entry  Entry
	isFile bool
	lower  string
}

// Build returns one level of directory entries at root/rel, sorted dirs-first.
func Build(root, rel string) ([]Entry, error) {
	base := root
	if rel != "" {
		base = pypath.Join(root, rel)
	}

	st, err := os.Stat(base)
	if err != nil || !st.IsDir() {
		return []Entry{}, nil
	}

	dirEntries, err := os.ReadDir(base)
	if err != nil {
		if os.IsPermission(err) {
			return []Entry{}, nil
		}
		return nil, err
	}

	items := make([]itemWithSortKey, 0, len(dirEntries))
	for _, de := range dirEntries {
		name := de.Name()
		if ignoreNames[name] {
			continue
		}

		var entryPath string
		if rel != "" {
			entryPath = strings.TrimLeft(rel+"/"+name, "/")
		} else {
			entryPath = name
		}

		itemPath := filepath.Join(base, name)
		itemStat, err := os.Stat(itemPath)

		var isDir bool
		var isFile bool
		var mtime int64
		var size *int64

		if err != nil {
			isDir = false
			isFile = false
			mtime = 0
			size = nil
		} else {
			isDir = itemStat.IsDir()
			isFile = !itemStat.IsDir()
			mtime = itemStat.ModTime().Unix()
			if isDir {
				size = nil
			} else {
				sz := itemStat.Size()
				size = &sz
			}
		}

		items = append(items, itemWithSortKey{
			entry: Entry{
				Name:  name,
				Path:  entryPath,
				IsDir: isDir,
				Mtime: mtime,
				Size:  size,
			},
			isFile: isFile,
			lower:  strings.ToLower(name),
		})
	}

	sort.SliceStable(items, func(i, j int) bool {
		if items[i].isFile != items[j].isFile {
			return !items[i].isFile && items[j].isFile
		}
		return items[i].lower < items[j].lower
	})

	result := make([]Entry, len(items))
	for i, item := range items {
		result[i] = item.entry
	}
	return result, nil
}
