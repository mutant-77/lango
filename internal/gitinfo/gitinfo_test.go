package gitinfo

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func skipIfNoGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
}

func initTestRepo(t *testing.T) string {
	t.Helper()
	skipIfNoGit(t)
	dir := t.TempDir()
	runGit(t, dir, "init")
	runGit(t, dir, "config", "user.name", "Test User")
	runGit(t, dir, "config", "user.email", "test@example.com")
	return dir
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := execGit(dir, args...)
	if err != nil {
		t.Fatalf("git %v failed: %v\nOutput: %s", args, err, out)
	}
	return out
}

func execGit(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_LITERAL_PATHSPECS=1",
		"LC_ALL=C",
	)
	var combined bytes.Buffer
	cmd.Stdout = &combined
	cmd.Stderr = &combined
	err := cmd.Run()
	return combined.String(), err
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write file %s: %v", path, err)
	}
}

func writeFileBytes(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write file %s: %v", path, err)
	}
}

func TestGitInfo(t *testing.T) {
	ctx := context.Background()

	t.Run("no-repo", func(t *testing.T) {
		skipIfNoGit(t)
		dir := t.TempDir()

		st, err := GetStatus(ctx, dir)
		if err != nil {
			t.Fatalf("expected nil error, got: %v", err)
		}
		if st.Repo {
			t.Fatalf("expected Repo=false, got true")
		}
		if len(st.Files) != 0 {
			t.Fatalf("expected 0 files, got %d", len(st.Files))
		}

		diff, err := GetDiff(ctx, dir, "foo.txt")
		if err != nil {
			t.Fatalf("expected nil error, got: %v", err)
		}
		if diff.Letter != "" || len(diff.Hunks) != 0 {
			t.Fatalf("expected empty diff, got letter=%q hunks=%d", diff.Letter, len(diff.Hunks))
		}
	})

	t.Run("repo sin commits", func(t *testing.T) {
		dir := initTestRepo(t)
		writeFile(t, filepath.Join(dir, "hello.txt"), "line1\nline2\nline3\n")

		st, err := GetStatus(ctx, dir)
		if err != nil {
			t.Fatalf("GetStatus: %v", err)
		}
		if !st.Repo {
			t.Fatalf("expected Repo=true")
		}
		if !st.Unborn {
			t.Fatalf("expected Unborn=true")
		}
		if st.Branch == "" {
			t.Fatalf("expected non-empty Branch, got %q", st.Branch)
		}
		if st.Total != 1 || st.Truncated {
			t.Fatalf("expected Total=1 Truncated=false, got Total=%d Truncated=%v", st.Total, st.Truncated)
		}
		if len(st.Files) != 1 {
			t.Fatalf("expected 1 file, got %d", len(st.Files))
		}
		f := st.Files[0]
		if f.Path != "hello.txt" || f.Letter != "A" || f.Add != 3 || f.Del != 0 {
			t.Fatalf("expected hello.txt A +3 -0, got %+v", f)
		}

		diff, err := GetDiff(ctx, dir, "hello.txt")
		if err != nil {
			t.Fatalf("GetDiff: %v", err)
		}
		if diff.Letter != "A" || len(diff.Hunks) != 1 {
			t.Fatalf("expected Letter=A and 1 hunk, got letter=%s hunks=%d", diff.Letter, len(diff.Hunks))
		}
		if len(diff.Hunks[0].Lines) != 3 {
			t.Fatalf("expected 3 lines, got %d", len(diff.Hunks[0].Lines))
		}
		for i, l := range diff.Hunks[0].Lines {
			if l.T != '+' {
				t.Fatalf("line %d: expected '+', got '%c'", i, l.T)
			}
		}
	})

	t.Run("modificado", func(t *testing.T) {
		dir := initTestRepo(t)
		writeFile(t, filepath.Join(dir, "f.txt"), "line1\nline2\nline3\n")
		runGit(t, dir, "add", "f.txt")
		runGit(t, dir, "commit", "-m", "init")

		writeFile(t, filepath.Join(dir, "f.txt"), "line1\nline2 mod\nline3\nline4\n")

		st, err := GetStatus(ctx, dir)
		if err != nil {
			t.Fatalf("GetStatus: %v", err)
		}
		if !st.Repo || st.Unborn || st.Detached {
			t.Fatalf("expected normal repo status, got %+v", st)
		}
		if len(st.Files) != 1 {
			t.Fatalf("expected 1 file, got %d", len(st.Files))
		}
		f := st.Files[0]
		if f.Path != "f.txt" || f.Letter != "M" || f.Add != 2 || f.Del != 1 {
			t.Fatalf("expected f.txt M +2 -1, got %+v", f)
		}

		diff, err := GetDiff(ctx, dir, "f.txt")
		if err != nil {
			t.Fatalf("GetDiff: %v", err)
		}
		if diff.Letter != "M" || len(diff.Hunks) != 1 {
			t.Fatalf("expected Letter=M and 1 hunk, got letter=%s hunks=%d", diff.Letter, len(diff.Hunks))
		}
	})

	t.Run("nuevo dentro de carpeta untracked", func(t *testing.T) {
		dir := initTestRepo(t)
		writeFile(t, filepath.Join(dir, "tracked.txt"), "tracked\n")
		runGit(t, dir, "add", "tracked.txt")
		runGit(t, dir, "commit", "-m", "init")

		writeFile(t, filepath.Join(dir, "untracked_dir", "inner.txt"), "line1\nline2\n")

		st, err := GetStatus(ctx, dir)
		if err != nil {
			t.Fatalf("GetStatus: %v", err)
		}
		if len(st.Files) != 1 {
			t.Fatalf("expected 1 file, got %d: %+v", len(st.Files), st.Files)
		}
		f := st.Files[0]
		if f.Path != "untracked_dir/inner.txt" || f.Letter != "A" || f.Add != 2 || f.Del != 0 {
			t.Fatalf("expected untracked_dir/inner.txt A +2 -0, got %+v", f)
		}
	})

	t.Run("GetDiff untracked hunks +", func(t *testing.T) {
		dir := initTestRepo(t)
		writeFile(t, filepath.Join(dir, "tracked.txt"), "tracked\n")
		runGit(t, dir, "add", "tracked.txt")
		runGit(t, dir, "commit", "-m", "init")

		writeFile(t, filepath.Join(dir, "newfile.txt"), "alpha\nbeta\ngamma\n")

		diff, err := GetDiff(ctx, dir, "newfile.txt")
		if err != nil {
			t.Fatalf("GetDiff: %v", err)
		}
		if diff.Letter != "A" || diff.Binary || diff.TooLarge || len(diff.Hunks) != 1 {
			t.Fatalf("expected A, not binary, not too large, 1 hunk; got %+v", diff)
		}
		h := diff.Hunks[0]
		if h.OldStart != 0 || h.NewStart != 1 || len(h.Lines) != 3 {
			t.Fatalf("expected OldStart=0 NewStart=1 3 lines, got OldStart=%d NewStart=%d lines=%d", h.OldStart, h.NewStart, len(h.Lines))
		}
		expectedLines := []string{"alpha", "beta", "gamma"}
		for i, expected := range expectedLines {
			if h.Lines[i].T != '+' || h.Lines[i].S != expected {
				t.Fatalf("line %d: expected +%s, got %c%s", i, expected, h.Lines[i].T, h.Lines[i].S)
			}
		}
	})

	t.Run("borrado", func(t *testing.T) {
		dir := initTestRepo(t)
		writeFile(t, filepath.Join(dir, "del.txt"), "line1\nline2\n")
		runGit(t, dir, "add", "del.txt")
		runGit(t, dir, "commit", "-m", "init")

		if err := os.Remove(filepath.Join(dir, "del.txt")); err != nil {
			t.Fatalf("remove file: %v", err)
		}

		st, err := GetStatus(ctx, dir)
		if err != nil {
			t.Fatalf("GetStatus: %v", err)
		}
		if len(st.Files) != 1 {
			t.Fatalf("expected 1 file, got %d", len(st.Files))
		}
		f := st.Files[0]
		if f.Path != "del.txt" || f.Letter != "D" || f.Add != 0 || f.Del != 2 {
			t.Fatalf("expected del.txt D +0 -2, got %+v", f)
		}

		diff, err := GetDiff(ctx, dir, "del.txt")
		if err != nil {
			t.Fatalf("GetDiff: %v", err)
		}
		if diff.Letter != "D" || len(diff.Hunks) != 1 {
			t.Fatalf("expected Letter=D and 1 hunk, got %+v", diff)
		}
		for i, l := range diff.Hunks[0].Lines {
			if l.T != '-' {
				t.Fatalf("line %d: expected '-', got '%c'", i, l.T)
			}
		}
	})

	t.Run("rename staged", func(t *testing.T) {
		dir := initTestRepo(t)
		content := "line1\nline2\nline3\n"
		writeFile(t, filepath.Join(dir, "origin.txt"), content)
		runGit(t, dir, "add", "origin.txt")
		runGit(t, dir, "commit", "-m", "add origin")

		runGit(t, dir, "mv", "origin.txt", "dest.txt")

		st, err := GetStatus(ctx, dir)
		if err != nil {
			t.Fatalf("GetStatus: %v", err)
		}
		if len(st.Files) != 2 {
			t.Fatalf("expected 2 files, got %d: %+v", len(st.Files), st.Files)
		}

		destFile := st.Files[0]
		origFile := st.Files[1]

		if destFile.Path != "dest.txt" || destFile.Letter != "A" || destFile.Add != 3 || destFile.Del != 0 {
			t.Fatalf("expected dest.txt A +3 -0, got %+v", destFile)
		}
		if origFile.Path != "origin.txt" || origFile.Letter != "D" || origFile.Add != 0 || origFile.Del != 3 {
			t.Fatalf("expected origin.txt D +0 -3, got %+v", origFile)
		}

		diffDest, err := GetDiff(ctx, dir, "dest.txt")
		if err != nil {
			t.Fatalf("GetDiff dest: %v", err)
		}
		if diffDest.Letter != "A" || len(diffDest.Hunks) != 1 {
			t.Fatalf("expected dest Diff Letter=A with 1 hunk, got %+v", diffDest)
		}

		diffOrig, err := GetDiff(ctx, dir, "origin.txt")
		if err != nil {
			t.Fatalf("GetDiff orig: %v", err)
		}
		if diffOrig.Letter != "D" || len(diffOrig.Hunks) != 1 {
			t.Fatalf("expected orig Diff Letter=D with 1 hunk, got %+v", diffOrig)
		}
	})

	t.Run("conflicto UU -> M", func(t *testing.T) {
		dir := initTestRepo(t)
		writeFile(t, filepath.Join(dir, "conflict.txt"), "base\n")
		runGit(t, dir, "add", "conflict.txt")
		runGit(t, dir, "commit", "-m", "base")

		branch := strings.TrimSpace(runGit(t, dir, "branch", "--show-current"))

		runGit(t, dir, "checkout", "-b", "feature")
		writeFile(t, filepath.Join(dir, "conflict.txt"), "feature branch\n")
		runGit(t, dir, "commit", "-am", "feature commit")

		runGit(t, dir, "checkout", branch)
		writeFile(t, filepath.Join(dir, "conflict.txt"), "base branch change\n")
		runGit(t, dir, "commit", "-am", "base commit")

		_, _ = execGit(dir, "merge", "feature")

		st, err := GetStatus(ctx, dir)
		if err != nil {
			t.Fatalf("GetStatus: %v", err)
		}
		if len(st.Files) != 1 {
			t.Fatalf("expected 1 file, got %d: %+v", len(st.Files), st.Files)
		}
		if st.Files[0].Path != "conflict.txt" || st.Files[0].Letter != "M" {
			t.Fatalf("expected conflict.txt M, got %+v", st.Files[0])
		}
	})

	t.Run("nombre con espacio, ñ y [x].go", func(t *testing.T) {
		dir := initTestRepo(t)
		writeFile(t, filepath.Join(dir, "init.txt"), "init\n")
		runGit(t, dir, "add", "init.txt")
		runGit(t, dir, "commit", "-m", "init")

		writeFile(t, filepath.Join(dir, "archivo con espacio.txt"), "espacio\n")
		writeFile(t, filepath.Join(dir, "archivo_con_ñ.txt"), "con ñ\n")
		writeFile(t, filepath.Join(dir, "[x].go"), "package main\n")

		st, err := GetStatus(ctx, dir)
		if err != nil {
			t.Fatalf("GetStatus: %v", err)
		}
		if len(st.Files) != 3 {
			t.Fatalf("expected 3 files, got %d: %+v", len(st.Files), st.Files)
		}

		expectedPaths := map[string]bool{
			"archivo con espacio.txt": false,
			"archivo_con_ñ.txt":       false,
			"[x].go":                  false,
		}
		for _, f := range st.Files {
			if _, ok := expectedPaths[f.Path]; !ok {
				t.Fatalf("unexpected path: %s", f.Path)
			}
			expectedPaths[f.Path] = true
			if f.Letter != "A" {
				t.Fatalf("expected letter A for %s, got %s", f.Path, f.Letter)
			}
		}
		for p, found := range expectedPaths {
			if !found {
				t.Fatalf("expected path %s was not found in status", p)
			}
		}

		diffBracket, err := GetDiff(ctx, dir, "[x].go")
		if err != nil {
			t.Fatalf("GetDiff [x].go: %v", err)
		}
		if diffBracket.Letter != "A" || len(diffBracket.Hunks) != 1 {
			t.Fatalf("expected [x].go Letter=A with 1 hunk, got %+v", diffBracket)
		}

		diffSpace, err := GetDiff(ctx, dir, "archivo con espacio.txt")
		if err != nil {
			t.Fatalf("GetDiff space: %v", err)
		}
		if diffSpace.Letter != "A" || len(diffSpace.Hunks) != 1 {
			t.Fatalf("expected space Diff Letter=A with 1 hunk, got %+v", diffSpace)
		}

		diffEnye, err := GetDiff(ctx, dir, "archivo_con_ñ.txt")
		if err != nil {
			t.Fatalf("GetDiff ñ: %v", err)
		}
		if diffEnye.Letter != "A" || len(diffEnye.Hunks) != 1 {
			t.Fatalf("expected ñ Diff Letter=A with 1 hunk, got %+v", diffEnye)
		}
	})

	t.Run("instancia en subcarpeta", func(t *testing.T) {
		dir := initTestRepo(t)
		writeFile(t, filepath.Join(dir, "root.txt"), "root\n")
		runGit(t, dir, "add", "root.txt")
		runGit(t, dir, "commit", "-m", "init")

		writeFile(t, filepath.Join(dir, "sub", "inside.txt"), "inside line\n")
		writeFile(t, filepath.Join(dir, "outside.txt"), "outside line\n")

		subDir := filepath.Join(dir, "sub")
		st, err := GetStatus(ctx, subDir)
		if err != nil {
			t.Fatalf("GetStatus subDir: %v", err)
		}
		if !st.Repo {
			t.Fatalf("expected Repo=true")
		}
		if st.Total != 1 || len(st.Files) != 1 {
			t.Fatalf("expected Total=1 and 1 file, got Total=%d files=%d: %+v", st.Total, len(st.Files), st.Files)
		}
		if st.Files[0].Path != "inside.txt" || st.Files[0].Letter != "A" {
			t.Fatalf("expected inside.txt A, got %+v", st.Files[0])
		}

		diff, err := GetDiff(ctx, subDir, "inside.txt")
		if err != nil {
			t.Fatalf("GetDiff inside.txt: %v", err)
		}
		if diff.Letter != "A" || len(diff.Hunks) != 1 {
			t.Fatalf("expected Letter=A and 1 hunk, got %+v", diff)
		}
	})

	t.Run("CRLF", func(t *testing.T) {
		dir := initTestRepo(t)
		writeFile(t, filepath.Join(dir, "crlf.txt"), "line1\r\nline2\r\n")
		runGit(t, dir, "add", "crlf.txt")
		runGit(t, dir, "commit", "-m", "init")

		writeFile(t, filepath.Join(dir, "crlf.txt"), "line1\r\nline2 modified\r\nline3\r\n")

		diff, err := GetDiff(ctx, dir, "crlf.txt")
		if err != nil {
			t.Fatalf("GetDiff crlf: %v", err)
		}
		if len(diff.Hunks) == 0 {
			t.Fatalf("expected hunks, got 0")
		}
		for _, h := range diff.Hunks {
			if strings.Contains(h.Header, "\r") {
				t.Fatalf("header contains \\r: %q", h.Header)
			}
			for _, l := range h.Lines {
				if strings.Contains(l.S, "\r") {
					t.Fatalf("line S contains \\r: %q", l.S)
				}
			}
		}

		b, err := json.Marshal(diff)
		if err != nil {
			t.Fatalf("json.Marshal diff: %v", err)
		}
		if bytes.Contains(b, []byte("\r")) {
			t.Fatalf("marshaled JSON contains \\r: %s", string(b))
		}
	})

	t.Run("binario", func(t *testing.T) {
		dir := initTestRepo(t)
		writeFileBytes(t, filepath.Join(dir, "blob.bin"), []byte{0x00, 0x01, 0x02, 0xFF})

		// untracked binary
		st, err := GetStatus(ctx, dir)
		if err != nil {
			t.Fatalf("GetStatus: %v", err)
		}
		if len(st.Files) != 1 {
			t.Fatalf("expected 1 file, got %d", len(st.Files))
		}
		if st.Files[0].Path != "blob.bin" || st.Files[0].Add != 0 || st.Files[0].Del != 0 {
			t.Fatalf("expected blob.bin 0/0, got %+v", st.Files[0])
		}

		diff, err := GetDiff(ctx, dir, "blob.bin")
		if err != nil {
			t.Fatalf("GetDiff blob.bin: %v", err)
		}
		if !diff.Binary || len(diff.Hunks) != 0 {
			t.Fatalf("expected Binary=true with 0 hunks, got %+v", diff)
		}

		// commit and modify tracked binary
		runGit(t, dir, "add", "blob.bin")
		runGit(t, dir, "commit", "-m", "add binary")

		writeFileBytes(t, filepath.Join(dir, "blob.bin"), []byte{0x00, 0x09, 0x08, 0xFE})

		stTracked, err := GetStatus(ctx, dir)
		if err != nil {
			t.Fatalf("GetStatus tracked: %v", err)
		}
		if len(stTracked.Files) != 1 {
			t.Fatalf("expected 1 file, got %d", len(stTracked.Files))
		}
		if stTracked.Files[0].Path != "blob.bin" || stTracked.Files[0].Add != 0 || stTracked.Files[0].Del != 0 {
			t.Fatalf("expected tracked blob.bin 0/0, got %+v", stTracked.Files[0])
		}

		diffTracked, err := GetDiff(ctx, dir, "blob.bin")
		if err != nil {
			t.Fatalf("GetDiff tracked: %v", err)
		}
		if !diffTracked.Binary || len(diffTracked.Hunks) != 0 {
			t.Fatalf("expected tracked Binary=true with 0 hunks, got %+v", diffTracked)
		}
	})

	t.Run("rama detached", func(t *testing.T) {
		dir := initTestRepo(t)
		writeFile(t, filepath.Join(dir, "f.txt"), "commit 1\n")
		runGit(t, dir, "add", "f.txt")
		runGit(t, dir, "commit", "-m", "c1")

		writeFile(t, filepath.Join(dir, "f.txt"), "commit 2\n")
		runGit(t, dir, "commit", "-am", "c2")

		runGit(t, dir, "checkout", "HEAD~1")

		st, err := GetStatus(ctx, dir)
		if err != nil {
			t.Fatalf("GetStatus detached: %v", err)
		}
		if !st.Repo {
			t.Fatalf("expected Repo=true")
		}
		if !st.Detached {
			t.Fatalf("expected Detached=true")
		}
		if st.Unborn {
			t.Fatalf("expected Unborn=false")
		}
		if len(st.Branch) == 0 {
			t.Fatalf("expected non-empty commit hash in Branch")
		}
	})

	t.Run("mas de 500 archivos Truncated", func(t *testing.T) {
		dir := initTestRepo(t)
		writeFile(t, filepath.Join(dir, "root.txt"), "root\n")
		runGit(t, dir, "add", "root.txt")
		runGit(t, dir, "commit", "-m", "init")

		for i := 0; i < 505; i++ {
			name := fmt.Sprintf("file_%03d.txt", i)
			writeFile(t, filepath.Join(dir, name), "content\n")
		}

		st, err := GetStatus(ctx, dir)
		if err != nil {
			t.Fatalf("GetStatus: %v", err)
		}
		if st.Total != 505 {
			t.Fatalf("expected Total=505, got %d", st.Total)
		}
		if !st.Truncated {
			t.Fatalf("expected Truncated=true")
		}
		if len(st.Files) != 500 {
			t.Fatalf("expected exactly 500 files, got %d", len(st.Files))
		}
	})

	t.Run("GetStatus no crea index.lock ni cambia mtime de index", func(t *testing.T) {
		dir := initTestRepo(t)
		writeFile(t, filepath.Join(dir, "file.txt"), "hello\n")
		runGit(t, dir, "add", "file.txt")
		runGit(t, dir, "commit", "-m", "init")

		writeFile(t, filepath.Join(dir, "file.txt"), "hello modified\n")

		indexPath := filepath.Join(dir, ".git", "index")
		fiBefore, err := os.Stat(indexPath)
		if err != nil {
			t.Fatalf("stat index before: %v", err)
		}
		mtimeBefore := fiBefore.ModTime()

		time.Sleep(100 * time.Millisecond)

		st, err := GetStatus(ctx, dir)
		if err != nil {
			t.Fatalf("GetStatus: %v", err)
		}
		if len(st.Files) != 1 {
			t.Fatalf("expected 1 file, got %d", len(st.Files))
		}

		lockPath := filepath.Join(dir, ".git", "index.lock")
		if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
			t.Fatalf("index.lock should not exist")
		}

		fiAfter, err := os.Stat(indexPath)
		if err != nil {
			t.Fatalf("stat index after: %v", err)
		}
		if !fiAfter.ModTime().Equal(mtimeBefore) {
			t.Fatalf("index mtime changed: before %v, after %v", mtimeBefore, fiAfter.ModTime())
		}
	})

	t.Run("diff TooLarge", func(t *testing.T) {
		dir := initTestRepo(t)
		var sb strings.Builder
		for i := 0; i < 5005; i++ {
			sb.WriteString("line\n")
		}
		writeFile(t, filepath.Join(dir, "large.txt"), sb.String())

		diff, err := GetDiff(ctx, dir, "large.txt")
		if err != nil {
			t.Fatalf("GetDiff large: %v", err)
		}
		if !diff.TooLarge {
			t.Fatalf("expected TooLarge=true, got false")
		}
		if len(diff.Hunks) != 0 {
			t.Fatalf("expected 0 hunks, got %d", len(diff.Hunks))
		}
	})

	t.Run("Line MarshalJSON", func(t *testing.T) {
		hunk := Hunk{
			Header:   "@@ -1,2 +1,2 @@",
			OldStart: 1,
			NewStart: 1,
			Lines: []Line{
				{T: ' ', S: "context line"},
				{T: '+', S: "added line"},
				{T: '-', S: "deleted line"},
			},
		}

		b, err := json.Marshal(hunk)
		if err != nil {
			t.Fatalf("json.Marshal: %v", err)
		}
		expectedJSON := `{"header":"@@ -1,2 +1,2 @@","old_start":1,"new_start":1,"lines":[[" ","context line"],["+","added line"],["-","deleted line"]]}`
		if string(b) != expectedJSON {
			t.Fatalf("unexpected JSON:\nwant: %s\ngot:  %s", expectedJSON, string(b))
		}

		var decoded Hunk
		if err := json.Unmarshal(b, &decoded); err != nil {
			t.Fatalf("json.Unmarshal: %v", err)
		}
		if len(decoded.Lines) != 3 {
			t.Fatalf("expected 3 lines, got %d", len(decoded.Lines))
		}
		if decoded.Lines[0].T != ' ' || decoded.Lines[0].S != "context line" {
			t.Fatalf("unexpected line 0: %+v", decoded.Lines[0])
		}
		if decoded.Lines[1].T != '+' || decoded.Lines[1].S != "added line" {
			t.Fatalf("unexpected line 1: %+v", decoded.Lines[1])
		}
		if decoded.Lines[2].T != '-' || decoded.Lines[2].S != "deleted line" {
			t.Fatalf("unexpected line 2: %+v", decoded.Lines[2])
		}
	})
}

func TestFirstDubiousPerDir(t *testing.T) {
	a := filepath.Join(t.TempDir(), "Repo")
	if !firstDubious(a) {
		t.Fatal("primera vez debe ser true")
	}
	if firstDubious(a) {
		t.Fatal("segunda vez debe ser false")
	}
	if firstDubious(strings.ToUpper(a)) {
		t.Fatal("otra capitalización debe ser false")
	}
	if !firstDubious(filepath.Join(t.TempDir(), "otro")) {
		t.Fatal("otra carpeta debe ser true")
	}
}
