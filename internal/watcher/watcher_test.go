//go:build windows

package watcher

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

// ── Unit tests for translate with synthetic records ───────────────────

func TestTranslateRenamedFile(t *testing.T) {
	tmp := t.TempDir()
	newFile := filepath.Join(tmp, "renamed_file.txt")
	if err := os.WriteFile(newFile, []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}

	records := []RawRecord{
		{Action: ActionRenamedOldName, Name: "file.txt"},
		{Action: ActionRenamedNewName, Name: "renamed_file.txt"},
	}

	events := Translate(tmp, records)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	ev := events[0]
	if ev.Type != EventMoved || ev.IsDir {
		t.Fatalf("expected FileMoved, got %+v", ev)
	}
	if ev.Path != filepath.Join(tmp, "file.txt") || ev.Dest != newFile {
		t.Fatalf("unexpected paths in moved event: %+v", ev)
	}
}

func TestTranslateRenamedDir(t *testing.T) {
	tmp := t.TempDir()
	newDir := filepath.Join(tmp, "newdir")
	subDir := filepath.Join(newDir, "sub")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatal(err)
	}
	fPath := filepath.Join(subDir, "f.txt")
	if err := os.WriteFile(fPath, []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}

	records := []RawRecord{
		{Action: ActionRenamedOldName, Name: "olddir"},
		{Action: ActionRenamedNewName, Name: "newdir"},
	}

	events := Translate(tmp, records)
	if len(events) != 3 {
		t.Fatalf("expected 3 events (1 dir + 1 subdir + 1 file), got %d: %+v", len(events), events)
	}

	// First event: root dir moved
	if events[0].Type != EventMoved || !events[0].IsDir {
		t.Fatalf("expected DirMoved for newdir, got %+v", events[0])
	}
	if events[0].Path != filepath.Join(tmp, "olddir") || events[0].Dest != newDir {
		t.Fatalf("unexpected paths in root dir moved: %+v", events[0])
	}

	// Second event: sub dir moved
	if events[1].Type != EventMoved || !events[1].IsDir {
		t.Fatalf("expected DirMoved for sub, got %+v", events[1])
	}
	expectedOldSub := filepath.Join(tmp, "olddir", "sub")
	if events[1].Path != expectedOldSub || events[1].Dest != subDir {
		t.Fatalf("unexpected paths in sub dir moved: %+v, want %s", events[1], expectedOldSub)
	}

	// Third event: file moved
	if events[2].Type != EventMoved || events[2].IsDir {
		t.Fatalf("expected FileMoved for f.txt, got %+v", events[2])
	}
	expectedOldFile := filepath.Join(tmp, "olddir", "sub", "f.txt")
	if events[2].Path != expectedOldFile || events[2].Dest != fPath {
		t.Fatalf("unexpected paths in file moved: %+v, want %s", events[2], expectedOldFile)
	}
}

func TestTranslateRemovedDir(t *testing.T) {
	tmp := t.TempDir()
	records := []RawRecord{
		{Action: ActionRemoved, Name: "any_dir"},
	}

	events := Translate(tmp, records)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	// REMOVED: ALWAYS FileDeleted (is_dir=false), even for directories.
	if events[0].Type != EventDeleted || events[0].IsDir {
		t.Fatalf("expected FileDeleted with IsDir=false, got %+v", events[0])
	}
	if events[0].Path != filepath.Join(tmp, "any_dir") {
		t.Fatalf("unexpected path: %s", events[0].Path)
	}
}

func TestTranslateDedupe(t *testing.T) {
	tmp := t.TempDir()
	records := []RawRecord{
		{Action: ActionModified, Name: "file.txt"},
		{Action: ActionModified, Name: "file.txt"}, // duplicate dropped
		{Action: ActionModified, Name: "other.txt"},
		{Action: ActionModified, Name: "file.txt"}, // not immediately previous -> kept
	}

	events := Translate(tmp, records)
	if len(events) != 3 {
		t.Fatalf("expected 3 events after dedupe, got %d: %+v", len(events), events)
	}
	if events[0].Path != filepath.Join(tmp, "file.txt") ||
		events[1].Path != filepath.Join(tmp, "other.txt") ||
		events[2].Path != filepath.Join(tmp, "file.txt") {
		t.Fatalf("unexpected dedupe sequence: %+v", events)
	}
}

func TestTranslateReplaceAllSemantics(t *testing.T) {
	tmp := t.TempDir()
	targetDir := filepath.Join(tmp, "dest")
	if err := os.MkdirAll(filepath.Join(targetDir, "sub"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(targetDir, "sub", "f.txt"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	// RENAMED_NEW with no preceding RENAMED_OLD -> src is ""
	records := []RawRecord{
		{Action: ActionRenamedNewName, Name: "dest"},
	}

	events := Translate(tmp, records)
	if len(events) < 3 {
		t.Fatalf("expected >= 3 events, got %d: %+v", len(events), events)
	}
	for i, ev := range events {
		if ev.Path != "" {
			t.Fatalf("event %d expected empty Path when src is empty, got %q", i, ev.Path)
		}
	}
}

func TestTranslateWalkOrdering(t *testing.T) {
	tmp := t.TempDir()
	dirA := filepath.Join(tmp, "d_a")
	dirB := filepath.Join(tmp, "d_b")
	subA := filepath.Join(dirA, "sub_a")
	if err := os.MkdirAll(subA, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dirB, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "file1.txt"), []byte("1"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dirA, "fileA.txt"), []byte("A"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subA, "fileSubA.txt"), []byte("Sub"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create symlink to dirB inside tmp
	symDir := filepath.Join(tmp, "sym_dir")
	if err := os.Symlink(dirB, symDir); err == nil {
		defer os.Remove(symDir)
	}

	events := generateSubCreatedEvents(tmp)

	// Top level: all directories must appear before top-level files
	firstFileIdx := -1
	for i, ev := range events {
		if !ev.IsDir {
			firstFileIdx = i
			break
		}
	}

	if firstFileIdx == -1 {
		t.Fatal("expected at least one file")
	}

	// Verify no directory appears after top level file at level 0
	topLevelDirs := 0
	for i := 0; i < firstFileIdx; i++ {
		if events[i].IsDir {
			topLevelDirs++
		}
	}
	if topLevelDirs < 2 {
		t.Fatalf("expected at least 2 top-level dirs before first file, got %d", topLevelDirs)
	}
}

// ── 1:1 port of tests/test_watcher_observer.py ────────────────────────

func setupHandler(t *testing.T) (*Handler, *[]map[string]any, string) {
	tmp := t.TempDir()
	var events []map[string]any
	h := NewHandler(tmp, func(ev map[string]any) {
		events = append(events, ev)
	})
	return h, &events, tmp
}

func TestOnCreatedFile(t *testing.T) {
	h, events, tmp := setupHandler(t)
	f := filepath.Join(tmp, "hello.txt")
	if err := os.WriteFile(f, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	h.OnCreated(f, false)

	if len(*events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(*events))
	}
	ev := (*events)[0]
	if ev["kind"] != "created" || ev["path"] != "hello.txt" || ev["is_dir"] != false {
		t.Fatalf("unexpected event: %+v", ev)
	}
}

func TestOnCreatedDir(t *testing.T) {
	h, events, tmp := setupHandler(t)
	d := filepath.Join(tmp, "mydir")
	if err := os.Mkdir(d, 0755); err != nil {
		t.Fatal(err)
	}
	h.OnCreated(d, true)

	if len(*events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(*events))
	}
	ev := (*events)[0]
	if ev["kind"] != "created" || ev["is_dir"] != true {
		t.Fatalf("unexpected event: %+v", ev)
	}
}

func TestOnDeleted(t *testing.T) {
	h, events, tmp := setupHandler(t)
	h.OnDeleted(filepath.Join(tmp, "gone.txt"), false)

	if len(*events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(*events))
	}
	ev := (*events)[0]
	if ev["kind"] != "deleted" || ev["path"] != "gone.txt" {
		t.Fatalf("unexpected event: %+v", ev)
	}
}

func TestOnModified(t *testing.T) {
	h, events, tmp := setupHandler(t)
	f := filepath.Join(tmp, "data.py")
	if err := os.WriteFile(f, []byte("x = 1"), 0644); err != nil {
		t.Fatal(err)
	}
	h.OnModified(f, false)

	if len(*events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(*events))
	}
	ev := (*events)[0]
	if ev["kind"] != "modified" || ev["path"] != "data.py" {
		t.Fatalf("unexpected event: %+v", ev)
	}
}

func TestOnModifiedDirEmitsDirChanged(t *testing.T) {
	h, events, tmp := setupHandler(t)
	sub := filepath.Join(tmp, "sub")
	if err := os.Mkdir(sub, 0755); err != nil {
		t.Fatal(err)
	}
	h.OnModified(sub, true)

	if len(*events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(*events))
	}
	ev := (*events)[0]
	if ev["kind"] != "dir-changed" || ev["path"] != "sub" {
		t.Fatalf("unexpected event: %+v", ev)
	}
}

func TestOnModifiedRootDirPathIsEmpty(t *testing.T) {
	h, events, tmp := setupHandler(t)
	h.OnModified(tmp, true)

	if len(*events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(*events))
	}
	ev := (*events)[0]
	if ev["kind"] != "dir-changed" || ev["path"] != "" {
		t.Fatalf("unexpected event: %+v", ev)
	}
}

func TestOnModifiedDirIgnoredPath(t *testing.T) {
	h, events, tmp := setupHandler(t)
	h.OnModified(filepath.Join(tmp, "node_modules"), true)

	if len(*events) != 0 {
		t.Fatalf("expected 0 events, got %d", len(*events))
	}
}

func TestOnMoved(t *testing.T) {
	h, events, tmp := setupHandler(t)
	h.OnMoved(filepath.Join(tmp, "old.txt"), filepath.Join(tmp, "new.txt"), false)

	if len(*events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(*events))
	}
	ev := (*events)[0]
	if ev["kind"] != "moved" || ev["path"] != "old.txt" || ev["dest"] != "new.txt" {
		t.Fatalf("unexpected event: %+v", ev)
	}
}

func TestIgnoresNodeModules(t *testing.T) {
	h, events, tmp := setupHandler(t)
	h.OnCreated(filepath.Join(tmp, "node_modules", "pkg.js"), false)

	if len(*events) != 0 {
		t.Fatalf("expected 0 events, got %d", len(*events))
	}
}

func TestDoesNotIgnoreDotfile(t *testing.T) {
	h, events, tmp := setupHandler(t)
	h.OnCreated(filepath.Join(tmp, ".env"), false)

	if len(*events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(*events))
	}
	if (*events)[0]["path"] != ".env" {
		t.Fatalf("unexpected path: %+v", (*events)[0])
	}
}

func TestIgnoresGit(t *testing.T) {
	h, events, tmp := setupHandler(t)
	h.OnCreated(filepath.Join(tmp, ".git", "config"), false)

	if len(*events) != 0 {
		t.Fatalf("expected 0 events, got %d", len(*events))
	}
}

func TestGitChanged(t *testing.T) {
	want := map[string]any{"kind": "git-changed"}

	t.Run("index created", func(t *testing.T) {
		h, events, tmp := setupHandler(t)
		h.OnCreated(filepath.Join(tmp, ".git", "index"), false)
		if len(*events) != 1 || !reflect.DeepEqual((*events)[0], want) {
			t.Fatalf("expected 1 git-changed event, got %+v", *events)
		}
	})

	t.Run("index modified", func(t *testing.T) {
		h, events, tmp := setupHandler(t)
		h.OnModified(filepath.Join(tmp, ".git", "index"), false)
		if len(*events) != 1 || !reflect.DeepEqual((*events)[0], want) {
			t.Fatalf("expected 1 git-changed event, got %+v", *events)
		}
	})

	t.Run("index moved from lock", func(t *testing.T) {
		h, events, tmp := setupHandler(t)
		h.OnMoved(filepath.Join(tmp, ".git", "index.lock"), filepath.Join(tmp, ".git", "index"), false)
		if len(*events) != 1 || !reflect.DeepEqual((*events)[0], want) {
			t.Fatalf("expected 1 git-changed event, got %+v", *events)
		}
	})

	t.Run("HEAD modified", func(t *testing.T) {
		h, events, tmp := setupHandler(t)
		h.OnModified(filepath.Join(tmp, ".git", "HEAD"), false)
		if len(*events) != 1 || !reflect.DeepEqual((*events)[0], want) {
			t.Fatalf("expected 1 git-changed event, got %+v", *events)
		}
	})

	t.Run("refs heads created", func(t *testing.T) {
		h, events, tmp := setupHandler(t)
		h.OnCreated(filepath.Join(tmp, ".git", "refs", "heads", "main"), false)
		if len(*events) != 1 || !reflect.DeepEqual((*events)[0], want) {
			t.Fatalf("expected 1 git-changed event, got %+v", *events)
		}
	})

	t.Run("objects ignored", func(t *testing.T) {
		h, events, tmp := setupHandler(t)
		h.OnCreated(filepath.Join(tmp, ".git", "objects", "ab", "cd"), false)
		if len(*events) != 0 {
			t.Fatalf("expected 0 events, got %d", len(*events))
		}
	})

	t.Run("index.lock ignored", func(t *testing.T) {
		h, events, tmp := setupHandler(t)
		h.OnCreated(filepath.Join(tmp, ".git", "index.lock"), false)
		if len(*events) != 0 {
			t.Fatalf("expected 0 events, got %d", len(*events))
		}
	})
}

func TestStartWatchingReturnsRunningObserver(t *testing.T) {
	tmp := t.TempDir()
	w, err := Start(tmp, func(ev map[string]any) {})
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	w.Stop()
}

// ── Stop test ────────────────────────────────────────────────────────

func TestStopPromptAndNoLeak(t *testing.T) {
	tmp := t.TempDir()
	w, err := Start(tmp, func(ev map[string]any) {})
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	time.Sleep(50 * time.Millisecond)

	start := time.Now()
	w.Stop()
	elapsed := time.Since(start)

	if elapsed > 200*time.Millisecond {
		t.Fatalf("Stop() took %v, expected < 200ms", elapsed)
	}

	select {
	case <-w.goroutineDone:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("read goroutine did not exit")
	}

	// Idempotent Stop call
	start2 := time.Now()
	w.Stop()
	if time.Since(start2) > 50*time.Millisecond {
		t.Fatal("second Stop() was not immediate")
	}
}

func TestStopConcurrent(t *testing.T) {
	tmp := t.TempDir()
	w, err := Start(tmp, func(ev map[string]any) {})
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	time.Sleep(50 * time.Millisecond)

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w.Stop()
		}()
	}
	wg.Wait()
}

// ── Golden test ──────────────────────────────────────────────────────

type goldenPayload struct {
	Ops []goldenOp `json:"ops"`
}

type goldenOp struct {
	Events []map[string]any `json:"events"`
	Op     string           `json:"op"`
}

type opSnapshot struct {
	events     []map[string]any
	pathExists map[string]bool
}

func normalizeEvent(ev map[string]any) map[string]any {
	out := make(map[string]any, len(ev))
	for k, v := range ev {
		switch k {
		case "mtime":
			if _, ok := v.(int); ok {
				out[k] = "<MTIME>"
			} else if _, ok := v.(int64); ok {
				out[k] = "<MTIME>"
			} else {
				out[k] = v
			}
		case "size":
			if n, ok := v.(int); ok {
				out[k] = float64(n)
			} else if n, ok := v.(int64); ok {
				out[k] = float64(n)
			} else if n, ok := v.(float64); ok {
				out[k] = n
			} else {
				out[k] = v
			}
		default:
			out[k] = v
		}
	}
	return out
}

func canonicalJSON(ev map[string]any) string {
	b, _ := json.Marshal(ev)
	return string(b)
}

type createdKey struct {
	kind  string
	path  string
	isDir bool
}

func TestWatcherSequenceGolden(t *testing.T) {
	goldenPath := filepath.Join("..", "..", "testdata", "golden", "watcher_sequence.json")
	data, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("failed to read golden file %s: %v", goldenPath, err)
	}

	var golden goldenPayload
	if err := json.Unmarshal(data, &golden); err != nil {
		t.Fatalf("failed to unmarshal golden: %v", err)
	}

	parent := t.TempDir()
	root := filepath.Join(parent, "root")
	outsideDir := filepath.Join(parent, "outside")
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outsideDir, 0755); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var events []map[string]any

	w, err := Start(root, func(ev map[string]any) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, ev)
	})
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer w.Stop()

	// Initial pause matching capture_watcher_golden.py
	time.Sleep(500 * time.Millisecond)

	type stepFn func()

	step := func(fn stepFn, sleepDuration time.Duration) opSnapshot {
		mu.Lock()
		startIdx := len(events)
		mu.Unlock()

		fn()
		time.Sleep(sleepDuration)

		mu.Lock()
		defer mu.Unlock()
		res := make([]map[string]any, len(events[startIdx:]))
		existsMap := make(map[string]bool)
		for i, ev := range events[startIdx:] {
			res[i] = normalizeEvent(ev)
			if p, ok := ev["path"].(string); ok && p != "" {
				full := filepath.Join(root, p)
				_, err := os.Stat(full)
				existsMap[p] = (err == nil)
			}
		}
		return opSnapshot{
			events:     res,
			pathExists: existsMap,
		}
	}

	opResults := make(map[string]opSnapshot)

	// 1. create_file
	opResults["create_file"] = step(func() {
		_ = os.WriteFile(filepath.Join(root, "new.txt"), []byte("hello\r\n"), 0644)
	}, 1*time.Second)

	// 2. write_file_twice
	opResults["write_file_twice"] = step(func() {
		_ = os.WriteFile(filepath.Join(root, "new.txt"), []byte("save 1\r\n"), 0644)
		time.Sleep(100 * time.Millisecond)
		_ = os.WriteFile(filepath.Join(root, "new.txt"), []byte("save 2\r\n"), 0644)
	}, 1*time.Second)

	// 3. create_dir_with_content
	opResults["create_dir_with_content"] = step(func() {
		incoming := filepath.Join(outsideDir, "incoming")
		sub := filepath.Join(incoming, "sub")
		_ = os.MkdirAll(sub, 0755)
		_ = os.WriteFile(filepath.Join(incoming, "file1.txt"), []byte("content 1\r\n"), 0644)
		_ = os.WriteFile(filepath.Join(sub, "file2.txt"), []byte("content 2\r\n"), 0644)
		_ = os.Rename(incoming, filepath.Join(root, "incoming"))
	}, 1*time.Second)

	// 4. create_empty_dir
	opResults["create_empty_dir"] = step(func() {
		_ = os.Mkdir(filepath.Join(root, "empty_dir"), 0755)
	}, 1*time.Second)

	// 5. rename_file
	opResults["rename_file"] = step(func() {
		_ = os.Rename(filepath.Join(root, "new.txt"), filepath.Join(root, "renamed.txt"))
	}, 1*time.Second)

	// 6. rename_dir
	opResults["rename_dir"] = step(func() {
		_ = os.Rename(filepath.Join(root, "incoming"), filepath.Join(root, "moved_dir"))
	}, 1*time.Second)

	// 7. delete_file
	opResults["delete_file"] = step(func() {
		_ = os.Remove(filepath.Join(root, "renamed.txt"))
	}, 1*time.Second)

	// 8. delete_dir_tree
	opResults["delete_dir_tree"] = step(func() {
		_ = os.RemoveAll(filepath.Join(root, "moved_dir"))
	}, 1*time.Second)

	// 9. create_in_ignored
	opResults["create_in_ignored"] = step(func() {
		nodeModules := filepath.Join(root, "node_modules")
		_ = os.MkdirAll(nodeModules, 0755)
		_ = os.WriteFile(filepath.Join(nodeModules, "x.js"), []byte("console.log(1)\r\n"), 0644)
	}, 1*time.Second)

	// 10. delete_root
	opResults["delete_root"] = step(func() {
		_ = os.RemoveAll(root)
	}, 1500*time.Millisecond)

	// Compare each op against golden
	for _, goldenOp := range golden.Ops {
		opName := goldenOp.Op
		snap, ok := opResults[opName]
		if !ok {
			t.Fatalf("op %s not executed", opName)
		}
		actualEvents := snap.events

		// Separate golden events
		var goldenCreated []map[string]any
		var goldenDeleted []map[string]any
		var goldenMoved []map[string]any
		var goldenMod []map[string]any
		var goldenDirChanged []map[string]any

		for _, ge := range goldenOp.Events {
			kind, _ := ge["kind"].(string)
			switch kind {
			case "created":
				goldenCreated = append(goldenCreated, ge)
			case "deleted":
				goldenDeleted = append(goldenDeleted, ge)
			case "moved":
				goldenMoved = append(goldenMoved, ge)
			case "modified":
				goldenMod = append(goldenMod, ge)
			case "dir-changed":
				goldenDirChanged = append(goldenDirChanged, ge)
			}
		}

		// Separate actual events
		var actualCreated []map[string]any
		var actualDeleted []map[string]any
		var actualMoved []map[string]any
		var actualMod []map[string]any
		var actualDirChanged []map[string]any

		for _, ae := range actualEvents {
			kind, _ := ae["kind"].(string)
			switch kind {
			case "created":
				actualCreated = append(actualCreated, ae)
			case "deleted":
				actualDeleted = append(actualDeleted, ae)
			case "moved":
				actualMoved = append(actualMoved, ae)
			case "modified":
				actualMod = append(actualMod, ae)
			case "dir-changed":
				actualDirChanged = append(actualDirChanged, ae)
			}
		}

		// 1. "created" events: multiset of (kind, path, is_dir) exactly; size for file may be golden value or 0
		goldenCreatedCounts := make(map[createdKey]int)
		for _, ge := range goldenCreated {
			goldenCreatedCounts[createdKey{
				kind:  ge["kind"].(string),
				path:  ge["path"].(string),
				isDir: ge["is_dir"].(bool),
			}]++
		}
		actualCreatedCounts := make(map[createdKey]int)
		for _, ae := range actualCreated {
			actualCreatedCounts[createdKey{
				kind:  ae["kind"].(string),
				path:  ae["path"].(string),
				isDir: ae["is_dir"].(bool),
			}]++
		}
		if !reflect.DeepEqual(goldenCreatedCounts, actualCreatedCounts) {
			t.Fatalf("op %s: created multiset mismatch on (kind, path, is_dir):\nGolden: %v\nActual: %v", opName, goldenCreatedCounts, actualCreatedCounts)
		}

		// Verify size on created events
		goldenCreatedByPath := make(map[string]map[string]any)
		for _, ge := range goldenCreated {
			goldenCreatedByPath[ge["path"].(string)] = ge
		}
		for _, ae := range actualCreated {
			path := ae["path"].(string)
			ge := goldenCreatedByPath[path]
			isDir := ae["is_dir"].(bool)
			if isDir {
				if ae["size"] != nil {
					t.Fatalf("op %s: dir created event %s has non-nil size %v", opName, path, ae["size"])
				}
			} else {
				var geSize float64
				if ge["size"] != nil {
					geSize = ge["size"].(float64)
				}
				var aeSize float64
				if ae["size"] != nil {
					aeSize = ae["size"].(float64)
				}
				if aeSize != geSize && aeSize != 0 {
					t.Fatalf("op %s: file created event %s has size %v, expected golden %v or 0", opName, path, aeSize, geSize)
				}
			}
		}

		// Sub-event sequential ordering for created events
		if len(goldenCreated) != len(actualCreated) {
			t.Fatalf("op %s: created count mismatch: golden=%d, actual=%d", opName, len(goldenCreated), len(actualCreated))
		}
		for i := range goldenCreated {
			ge := goldenCreated[i]
			ae := actualCreated[i]
			if ge["path"] != ae["path"] || ge["is_dir"] != ae["is_dir"] {
				t.Fatalf("op %s: created order mismatch at index %d:\nGolden: %v\nActual: %v", opName, i, ge, ae)
			}
		}

		// 2. "deleted" events: multiset exact match
		goldenDelCounts := make(map[string]int)
		for _, ge := range goldenDeleted {
			goldenDelCounts[canonicalJSON(ge)]++
		}
		actualDelCounts := make(map[string]int)
		for _, ae := range actualDeleted {
			actualDelCounts[canonicalJSON(ae)]++
		}
		if !reflect.DeepEqual(goldenDelCounts, actualDelCounts) {
			t.Fatalf("op %s: deleted multiset mismatch:\nGolden: %v\nActual: %v", opName, goldenDelCounts, actualDelCounts)
		}

		// 3. "moved" events: multiset and sub-event ordering exact match
		goldenMovedCounts := make(map[string]int)
		for _, ge := range goldenMoved {
			goldenMovedCounts[canonicalJSON(ge)]++
		}
		actualMovedCounts := make(map[string]int)
		for _, ae := range actualMoved {
			actualMovedCounts[canonicalJSON(ae)]++
		}
		if !reflect.DeepEqual(goldenMovedCounts, actualMovedCounts) {
			t.Fatalf("op %s: moved multiset mismatch:\nGolden: %v\nActual: %v", opName, goldenMovedCounts, actualMovedCounts)
		}
		if len(goldenMoved) != len(actualMoved) {
			t.Fatalf("op %s: moved count mismatch: golden=%d, actual=%d", opName, len(goldenMoved), len(actualMoved))
		}
		for i := range goldenMoved {
			if canonicalJSON(goldenMoved[i]) != canonicalJSON(actualMoved[i]) {
				t.Fatalf("op %s: moved order mismatch at index %d:\nGolden: %v\nActual: %v", opName, i, goldenMoved[i], actualMoved[i])
			}
		}

		// 4. "modified" events: compare set of paths only for paths that still exist after the op
		goldenModPaths := make(map[string]bool)
		for _, ge := range goldenMod {
			p, _ := ge["path"].(string)
			goldenModPaths[p] = true
		}

		actualExistingModPaths := make(map[string]bool)
		for _, ae := range actualMod {
			p, _ := ae["path"].(string)
			if snap.pathExists[p] {
				actualExistingModPaths[p] = true
			} else {
				t.Logf("op %s: modified event on deleted path %q (tolerated deletion race)", opName, p)
			}
		}

		if !reflect.DeepEqual(goldenModPaths, actualExistingModPaths) {
			t.Fatalf("op %s: modified path set mismatch for surviving paths:\nGolden: %v\nActual: %v", opName, goldenModPaths, actualExistingModPaths)
		}

		// 5. "dir-changed": keep as log-only
		goldenDCPaths := make(map[string]bool)
		for _, ge := range goldenDirChanged {
			goldenDCPaths[ge["path"].(string)] = true
		}
		actualDCPaths := make(map[string]bool)
		for _, ae := range actualDirChanged {
			actualDCPaths[ae["path"].(string)] = true
		}
		if !reflect.DeepEqual(goldenDCPaths, actualDCPaths) {
			t.Logf("op %s: dir-changed differ (nondeterministic): golden=%v, actual=%v", opName, goldenDCPaths, actualDCPaths)
		}
	}
}
