package pypath

import (
	"os"
	"path/filepath"
	"testing"
)

func TestJoin(t *testing.T) {
	root := `E:\proj`
	cases := []struct {
		rel  string
		want string
	}{
		{"", `E:\proj`},
		{"a/b", `E:\proj\a\b`},
		{`C:\Windows`, `C:\Windows`},
		{"D:x", "D:x"},
		{"/x", `E:\x`},
		{`\x`, `E:\x`},
		{"..config", `E:\proj\..config`},
	}

	for _, tc := range cases {
		got := Join(root, tc.rel)
		if got != tc.want {
			t.Errorf("Join(%q, %q) = %q, want %q", root, tc.rel, got, tc.want)
		}
	}
}

func TestResolveAndContained(t *testing.T) {
	tempDir := t.TempDir()
	projRoot := filepath.Join(tempDir, "proj")
	if err := os.MkdirAll(projRoot, 0755); err != nil {
		t.Fatal(err)
	}

	subFile := filepath.Join(projRoot, "sub.txt")
	if err := os.WriteFile(subFile, []byte("sub"), 0644); err != nil {
		t.Fatal(err)
	}

	dotDotConfigFile := filepath.Join(projRoot, "..config")
	if err := os.WriteFile(dotDotConfigFile, []byte("cfg"), 0644); err != nil {
		t.Fatal(err)
	}

	brokenLink := filepath.Join(projRoot, "broken_link")
	_ = os.Symlink("non_existent_target", brokenLink)

	siblingDir := filepath.Join(tempDir, "proj2")
	if err := os.MkdirAll(siblingDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Contained tests
	if !Contained(projRoot, subFile) {
		t.Errorf("expected %q to be contained in %q", subFile, projRoot)
	}
	if !Contained(projRoot, dotDotConfigFile) {
		t.Errorf("expected %q to be contained in %q", dotDotConfigFile, projRoot)
	}
	if !Contained(projRoot, brokenLink) {
		t.Errorf("expected broken link %q to be contained in %q", brokenLink, projRoot)
	}
	if Contained(projRoot, siblingDir) {
		t.Errorf("sibling dir %q should NOT be contained in %q", siblingDir, projRoot)
	}
	outsideFile := filepath.Join(tempDir, "outside.txt")
	if Contained(projRoot, outsideFile) {
		t.Errorf("outside file %q should NOT be contained in %q", outsideFile, projRoot)
	}

	// Case insensitivity test
	upperProj := filepath.Join(tempDir, "PROJ", "SUB.TXT")
	if !Contained(projRoot, upperProj) {
		t.Errorf("expected case-insensitive match for %q in %q", upperProj, projRoot)
	}
}
