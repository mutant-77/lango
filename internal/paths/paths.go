package paths

import (
	"os"
	"path/filepath"
)

// ExeDir returns the directory of the running executable, resolving any symlinks.
func ExeDir() string {
	exe, err := os.Executable()
	if err != nil {
		cwd, err := os.Getwd()
		if err != nil {
			return "."
		}
		return cwd
	}
	resolved, err := filepath.EvalSymlinks(exe)
	if err == nil {
		exe = resolved
	}
	return filepath.Dir(exe)
}
