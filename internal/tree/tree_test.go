package tree

import (
	"os"
	"path/filepath"
	"testing"
)

func setupTestProject(t *testing.T) string {
	t.Helper()
	proj := t.TempDir()

	_ = os.MkdirAll(filepath.Join(proj, "src"), 0755)
	_ = os.WriteFile(filepath.Join(proj, "src", "main.py"), []byte("# main"), 0644)
	_ = os.WriteFile(filepath.Join(proj, "src", "server.py"), []byte("# server"), 0644)
	_ = os.WriteFile(filepath.Join(proj, "README.md"), []byte("# readme"), 0644)

	_ = os.MkdirAll(filepath.Join(proj, "node_modules"), 0755)
	_ = os.WriteFile(filepath.Join(proj, "node_modules", "pkg.js"), []byte("x"), 0644)
	_ = os.MkdirAll(filepath.Join(proj, ".git"), 0755)
	_ = os.MkdirAll(filepath.Join(proj, "__pycache__"), 0755)

	return proj
}

func TestBuildTreeRoot(t *testing.T) {
	proj := setupTestProject(t)

	entries, err := Build(proj, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	names := make(map[string]bool)
	for _, e := range entries {
		names[e.Name] = true
	}

	if !names["src"] {
		t.Errorf("expected 'src' in tree entries")
	}
	if !names["README.md"] {
		t.Errorf("expected 'README.md' in tree entries")
	}
	if names["node_modules"] {
		t.Errorf("node_modules should be ignored")
	}
	if names[".git"] {
		t.Errorf(".git should be ignored")
	}
	if names["__pycache__"] {
		t.Errorf("__pycache__ should be ignored")
	}
}

func TestBuildTreeHiddenFiles(t *testing.T) {
	proj := setupTestProject(t)
	_ = os.WriteFile(filepath.Join(proj, ".env"), []byte("DEBUG=1"), 0644)
	_ = os.MkdirAll(filepath.Join(proj, ".docs"), 0755)

	entries, err := Build(proj, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	names := make(map[string]bool)
	for _, e := range entries {
		names[e.Name] = true
	}

	if !names[".env"] {
		t.Errorf("expected '.env' in tree entries")
	}
	if !names[".docs"] {
		t.Errorf("expected '.docs' in tree entries")
	}
}

func TestBuildTreeDirsBeforeFiles(t *testing.T) {
	proj := setupTestProject(t)
	entries, err := Build(proj, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var dirs, files []int
	for idx, e := range entries {
		if e.IsDir {
			dirs = append(dirs, idx)
		} else {
			files = append(files, idx)
		}
	}

	if len(dirs) > 0 && len(files) > 0 {
		lastDir := dirs[len(dirs)-1]
		firstFile := files[0]
		if lastDir > firstFile {
			t.Errorf("all dirs should appear before files: lastDir=%d, firstFile=%d", lastDir, firstFile)
		}
	}
}

func TestBuildTreeSubdirectory(t *testing.T) {
	proj := setupTestProject(t)
	entries, err := Build(proj, "src")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	names := make(map[string]bool)
	paths := make(map[string]bool)
	for _, e := range entries {
		names[e.Name] = true
		paths[e.Path] = true
	}

	if !names["main.py"] || !names["server.py"] {
		t.Errorf("expected main.py and server.py in src entries")
	}
	if !paths["src/main.py"] || !paths["src/server.py"] {
		t.Errorf("expected src/main.py in src entry paths")
	}
}

func TestBuildTreeMissingDir(t *testing.T) {
	entries, err := Build("/does/not/exist/at/all", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected empty entries, got %d", len(entries))
	}
}

func TestBuildTreeBrokenSymlink(t *testing.T) {
	proj := t.TempDir()
	_ = os.WriteFile(filepath.Join(proj, "z_file.txt"), []byte("file"), 0644)
	_ = os.Symlink("non_existent_target", filepath.Join(proj, "broken_link"))

	entries, err := Build(proj, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	// broken_link is not a file (isFile=false), so it sorts before z_file.txt (isFile=true)
	if entries[0].Name != "broken_link" {
		t.Errorf("expected broken_link to sort before z_file.txt, got %s", entries[0].Name)
	}
	if entries[0].IsDir {
		t.Errorf("broken_link is_dir should be false")
	}
	if entries[0].Size != nil {
		t.Errorf("broken_link size should be nil")
	}
}
