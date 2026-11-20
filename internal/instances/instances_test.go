package instances

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type mockPTY struct {
	isAlive   bool
	agent     string
	state     string
	idleS     int
	panics    bool
	termCalls int
}

func (p *mockPTY) IsAlive() bool { return p.isAlive }
func (p *mockPTY) Agent() string { return p.agent }
func (p *mockPTY) State() string { return p.state }
func (p *mockPTY) IdleS() int    { return p.idleS }
func (p *mockPTY) Terminate() {
	p.termCalls++
	if p.panics {
		panic("pty terminate panic")
	}
}

type mockWatcher struct {
	panics    bool
	stopCalls int
}

func (w *mockWatcher) Stop() {
	w.stopCalls++
	if w.panics {
		panic("watcher stop panic")
	}
}

// ── Instance.Name truncation ──────────────────────────────────────────

func TestNameShort(t *testing.T) {
	tmpDir := t.TempDir()
	inst := &Instance{ID: "x", Path: tmpDir}
	base := baseName(tmpDir)
	runes := []rune(base)
	if len(runes) <= 10 {
		if inst.Name() != base {
			t.Fatalf("inst.Name() = %q, want %q", inst.Name(), base)
		}
	} else {
		if !strings.HasSuffix(inst.Name(), "…") {
			t.Fatalf("inst.Name() %q should end with …", inst.Name())
		}
		if len([]rune(inst.Name())) != 11 {
			t.Fatalf("rune count = %d, want 11", len([]rune(inst.Name())))
		}
	}
}

func TestNameTruncated(t *testing.T) {
	inst := &Instance{ID: "x", Path: "/some/abcdefghijk"}
	if inst.Name() != "abcdefghij…" {
		t.Fatalf("inst.Name() = %q, want abcdefghij…", inst.Name())
	}
	if len([]rune(inst.Name())) != 11 {
		t.Fatalf("rune count = %d, want 11", len([]rune(inst.Name())))
	}
}

func TestNameExact10(t *testing.T) {
	inst := &Instance{ID: "x", Path: "/some/abcdefghij"}
	if inst.Name() != "abcdefghij" {
		t.Fatalf("inst.Name() = %q, want abcdefghij", inst.Name())
	}
}

func TestNameMultibyteRunes(t *testing.T) {
	// 11 runes: exact 10 runes preserved + "…"
	inst11 := &Instance{ID: "x", Path: `C:\data\ñandúñandú12`}
	if inst11.Name() != "ñandúñandú…" {
		t.Fatalf("inst11.Name() = %q, want ñandúñandú…", inst11.Name())
	}
	if len([]rune(inst11.Name())) != 11 {
		t.Fatalf("rune count = %d, want 11", len([]rune(inst11.Name())))
	}

	// Exact 10 runes: unchanged
	inst10 := &Instance{ID: "x", Path: `C:\data\ñandúñandú`}
	if inst10.Name() != "ñandúñandú" {
		t.Fatalf("inst10.Name() = %q, want ñandúñandú", inst10.Name())
	}
}

// ── InstanceManager.Create ────────────────────────────────────────────

func TestCreateValid(t *testing.T) {
	tmpDir := t.TempDir()
	persistFile := filepath.Join(tmpDir, "lango.json")
	mgr := NewManager(persistFile)

	inst, err := mgr.Create(tmpDir)
	if err != nil {
		t.Fatalf("mgr.Create(%q) failed: %v", tmpDir, err)
	}

	absDir, _ := filepath.Abs(tmpDir)
	if inst.Path != absDir {
		t.Fatalf("inst.Path = %q, want %q", inst.Path, absDir)
	}
	if inst.ID == "" {
		t.Fatalf("inst.ID is empty")
	}
	if mgr.ActiveID() == nil || *mgr.ActiveID() != inst.ID {
		t.Fatalf("mgr.ActiveID() = %v, want %q", mgr.ActiveID(), inst.ID)
	}
}

func TestCreateInvalidPath(t *testing.T) {
	tmpDir := t.TempDir()
	persistFile := filepath.Join(tmpDir, "lango.json")
	mgr := NewManager(persistFile)

	nonExistent := filepath.Join(tmpDir, "does", "not", "exist")
	_, err := mgr.Create(nonExistent)
	if err == nil {
		t.Fatalf("expected error for non-existent path")
	}
	expected := "Not a directory: " + nonExistent
	if err.Error() != expected {
		t.Fatalf("err = %q, want %q", err.Error(), expected)
	}
}

func TestCreateAlwaysActivates(t *testing.T) {
	tmpDir := t.TempDir()
	d1 := filepath.Join(tmpDir, "p1")
	d2 := filepath.Join(tmpDir, "p2")
	_ = os.Mkdir(d1, 0755)
	_ = os.Mkdir(d2, 0755)

	mgr := NewManager(filepath.Join(tmpDir, "lango.json"))
	i1, err := mgr.Create(d1)
	if err != nil {
		t.Fatalf("Create d1 failed: %v", err)
	}
	if mgr.ActiveID() == nil || *mgr.ActiveID() != i1.ID {
		t.Fatalf("active id = %v, want %s", mgr.ActiveID(), i1.ID)
	}

	i2, err := mgr.Create(d2)
	if err != nil {
		t.Fatalf("Create d2 failed: %v", err)
	}
	if mgr.ActiveID() == nil || *mgr.ActiveID() != i2.ID {
		t.Fatalf("active id = %v, want %s", mgr.ActiveID(), i2.ID)
	}
}

func TestCreateSamePathOpensNewTab(t *testing.T) {
	tmpDir := t.TempDir()
	proj := filepath.Join(tmpDir, "proj")
	_ = os.Mkdir(proj, 0755)

	mgr := NewManager(filepath.Join(tmpDir, "lango.json"))
	i1, err := mgr.Create(proj)
	if err != nil {
		t.Fatalf("Create i1 failed: %v", err)
	}
	i2, err := mgr.Create(proj)
	if err != nil {
		t.Fatalf("Create i2 failed: %v", err)
	}

	if i1.ID == i2.ID {
		t.Fatalf("expected distinct IDs, got %q == %q", i1.ID, i2.ID)
	}
	if len(mgr.List()) != 2 {
		t.Fatalf("len(mgr.List()) = %d, want 2", len(mgr.List()))
	}
	if mgr.ActiveID() == nil || *mgr.ActiveID() != i2.ID {
		t.Fatalf("active id = %v, want %s", mgr.ActiveID(), i2.ID)
	}
}

// ── Persist & Load round-trip ─────────────────────────────────────────

func TestPersistLoad(t *testing.T) {
	tmpDir := t.TempDir()
	proj := filepath.Join(tmpDir, "proj")
	_ = os.Mkdir(proj, 0755)
	persistFile := filepath.Join(tmpDir, "lango.json")

	mgr := NewManager(persistFile)
	inst, err := mgr.Create(proj)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	mgr2 := NewManager(persistFile)
	mgr2.Load()

	list := mgr2.List()
	if len(list) != 1 {
		t.Fatalf("len(list) = %d, want 1", len(list))
	}
	if list[0].ID != inst.ID {
		t.Fatalf("list[0].ID = %q, want %q", list[0].ID, inst.ID)
	}
	if mgr2.ActiveID() == nil || *mgr2.ActiveID() != inst.ID {
		t.Fatalf("mgr2.ActiveID() = %v, want %q", mgr2.ActiveID(), inst.ID)
	}
}

func TestLoadDiscardsMissingPaths(t *testing.T) {
	tmpDir := t.TempDir()
	persistFile := filepath.Join(tmpDir, "lango.json")

	content := `{
  "instances": [
    {
      "id": "aaa",
      "path": "` + strings.ReplaceAll(filepath.Join(tmpDir, "gone"), `\`, `\\`) + `",
      "agent": null
    }
  ],
  "active": "aaa"
}`
	if err := os.WriteFile(persistFile, []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	mgr := NewManager(persistFile)
	mgr.Load()

	if len(mgr.List()) != 0 {
		t.Fatalf("mgr.List() has %d items, want 0", len(mgr.List()))
	}
	if mgr.ActiveID() != nil {
		t.Fatalf("mgr.ActiveID() = %v, want nil", mgr.ActiveID())
	}
}

// ── Activate & Close ──────────────────────────────────────────────────

func TestActivate(t *testing.T) {
	tmpDir := t.TempDir()
	d1 := filepath.Join(tmpDir, "p1")
	d2 := filepath.Join(tmpDir, "p2")
	_ = os.Mkdir(d1, 0755)
	_ = os.Mkdir(d2, 0755)

	mgr := NewManager(filepath.Join(tmpDir, "lango.json"))
	i1, _ := mgr.Create(d1)
	i2, _ := mgr.Create(d2)

	if err := mgr.Activate(i1.ID); err != nil {
		t.Fatalf("Activate(%s) failed: %v", i1.ID, err)
	}
	if mgr.ActiveID() == nil || *mgr.ActiveID() != i1.ID {
		t.Fatalf("ActiveID = %v, want %s", mgr.ActiveID(), i1.ID)
	}

	// Unknown ID is no-op
	if err := mgr.Activate("non-existent"); err != nil {
		t.Fatalf("Activate non-existent should succeed as no-op: %v", err)
	}
	if mgr.ActiveID() == nil || *mgr.ActiveID() != i1.ID {
		t.Fatalf("ActiveID should still be %s", i1.ID)
	}

	if err := mgr.Activate(i2.ID); err != nil {
		t.Fatalf("Activate(%s) failed: %v", i2.ID, err)
	}
	if mgr.ActiveID() == nil || *mgr.ActiveID() != i2.ID {
		t.Fatalf("ActiveID = %v, want %s", mgr.ActiveID(), i2.ID)
	}
}

func TestCloseRemovesInstance(t *testing.T) {
	tmpDir := t.TempDir()
	p := filepath.Join(tmpDir, "p")
	_ = os.Mkdir(p, 0755)

	mgr := NewManager(filepath.Join(tmpDir, "lango.json"))
	inst, _ := mgr.Create(p)

	if err := mgr.Close(inst.ID); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
	if mgr.Get(inst.ID) != nil {
		t.Fatalf("mgr.Get(%s) should be nil after close", inst.ID)
	}
	if len(mgr.List()) != 0 {
		t.Fatalf("mgr.List() should be empty")
	}
	if mgr.ActiveID() != nil {
		t.Fatalf("mgr.ActiveID() should be nil")
	}
}

func TestCloseUnknownIsNoop(t *testing.T) {
	mgr := NewManager(filepath.Join(t.TempDir(), "lango.json"))
	if err := mgr.Close("nonexistent-id"); err != nil {
		t.Fatalf("Close nonexistent should be no-op: %v", err)
	}
}

func TestCloseMissingFolderDoesNotCrash(t *testing.T) {
	tmpDir := t.TempDir()
	proj := filepath.Join(tmpDir, "project")
	_ = os.Mkdir(proj, 0755)

	mgr := NewManager(filepath.Join(tmpDir, "lango.json"))
	inst, err := mgr.Create(proj)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	if err := os.RemoveAll(proj); err != nil {
		t.Fatalf("RemoveAll failed: %v", err)
	}

	if err := mgr.Close(inst.ID); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
	if mgr.Get(inst.ID) != nil {
		t.Fatalf("mgr.Get(%s) should be nil", inst.ID)
	}
}

func TestCloseActiveSwitchesToFirst(t *testing.T) {
	tmpDir := t.TempDir()
	d1 := filepath.Join(tmpDir, "d1")
	d2 := filepath.Join(tmpDir, "d2")
	d3 := filepath.Join(tmpDir, "d3")
	_ = os.Mkdir(d1, 0755)
	_ = os.Mkdir(d2, 0755)
	_ = os.Mkdir(d3, 0755)

	mgr := NewManager(filepath.Join(tmpDir, "lango.json"))
	i1, _ := mgr.Create(d1)
	i2, _ := mgr.Create(d2)
	i3, _ := mgr.Create(d3)

	// i3 is active. Closing i3 switches active to i1
	_ = mgr.Close(i3.ID)
	if mgr.ActiveID() == nil || *mgr.ActiveID() != i1.ID {
		t.Fatalf("ActiveID = %v, want %s", mgr.ActiveID(), i1.ID)
	}

	// Closing i1 switches active to i2
	_ = mgr.Close(i1.ID)
	if mgr.ActiveID() == nil || *mgr.ActiveID() != i2.ID {
		t.Fatalf("ActiveID = %v, want %s", mgr.ActiveID(), i2.ID)
	}

	// Closing i2 clears active
	_ = mgr.Close(i2.ID)
	if mgr.ActiveID() != nil {
		t.Fatalf("ActiveID = %v, want nil", mgr.ActiveID())
	}
}

func TestClosePanickingPTYAndWatcher(t *testing.T) {
	tmpDir := t.TempDir()
	p := filepath.Join(tmpDir, "p")
	_ = os.Mkdir(p, 0755)

	mgr := NewManager(filepath.Join(tmpDir, "lango.json"))
	inst, _ := mgr.Create(p)

	inst.SetTerm("1", &mockPTY{panics: true})
	inst.AddWatcher(&mockWatcher{panics: true})

	if err := mgr.Close(inst.ID); err != nil {
		t.Fatalf("Close should recover from panics and succeed: %v", err)
	}
	if mgr.Get(inst.ID) != nil {
		t.Fatalf("Instance should be removed")
	}
}

// ── Missing folder parity tests (test_missing_folder.py) ──────────────

func TestMgrCloseRemovesCorrectInstance(t *testing.T) {
	tmpDir := t.TempDir()
	p1 := filepath.Join(tmpDir, "p1")
	p2 := filepath.Join(tmpDir, "p2")
	_ = os.Mkdir(p1, 0755)
	_ = os.Mkdir(p2, 0755)

	mgr := NewManager(filepath.Join(tmpDir, "lango.json"))
	i1, _ := mgr.Create(p1)
	i2, _ := mgr.Create(p2)

	before := len(mgr.List())
	_ = mgr.Close(i1.ID)
	if mgr.Get(i1.ID) != nil {
		t.Fatalf("i1 still in manager")
	}
	if mgr.Get(i2.ID) == nil {
		t.Fatalf("i2 should still be in manager")
	}
	if len(mgr.List()) != before-1 {
		t.Fatalf("count = %d, want %d", len(mgr.List()), before-1)
	}
}

// ── Python lango.json round-trip & CRLF test ──────────────────────────

func TestRoundTripPythonFile(t *testing.T) {
	// Verify Load works on CRLF bytes as-is
	testdataPath := filepath.Join("testdata", "lango_python.json")
	rawBytes, err := os.ReadFile(testdataPath)
	if err != nil {
		t.Fatalf("ReadFile(%s) failed: %v", testdataPath, err)
	}
	if !bytes.Contains(rawBytes, []byte("\r\n")) {
		t.Fatalf("lango_python.json should contain CRLF line endings")
	}

	// Create temp directories to substitute paths for portable round trip
	tmpDir := t.TempDir()
	d1 := filepath.Join(tmpDir, "BooksGen")
	d2 := filepath.Join(tmpDir, "Lango")
	if err := os.Mkdir(d1, 0755); err != nil {
		t.Fatalf("Mkdir d1 failed: %v", err)
	}
	if err := os.Mkdir(d2, 0755); err != nil {
		t.Fatalf("Mkdir d2 failed: %v", err)
	}

	// Replace the real Windows paths with the temp directories, preserving CRLF
	jsonStr := string(rawBytes)
	jsonStr = strings.Replace(jsonStr, `E:\\Codigo\\BooksGen`, strings.ReplaceAll(d1, `\`, `\\`), 1)
	jsonStr = strings.Replace(jsonStr, `E:\\Codigo\\Lango`, strings.ReplaceAll(d2, `\`, `\\`), 1)

	persistPath := filepath.Join(tmpDir, "test_lango.json")
	if err := os.WriteFile(persistPath, []byte(jsonStr), 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	mgr := NewManager(persistPath)
	mgr.Load()

	list := mgr.List()
	if len(list) != 2 {
		t.Fatalf("len(list) = %d, want 2", len(list))
	}
	if list[0].ID != "e784d9ba-e62b-4c98-892c-6d4c0124fcde" || list[0].Path != d1 || list[0].Agent == nil || *list[0].Agent != "agy" {
		t.Fatalf("instance 0 mismatch: %+v", list[0])
	}
	if list[1].ID != "4aeeff2e-bd1d-40c5-84e4-369918c11bde" || list[1].Path != d2 || list[1].Agent == nil || *list[1].Agent != "claude" {
		t.Fatalf("instance 1 mismatch: %+v", list[1])
	}
	if mgr.ActiveID() == nil || *mgr.ActiveID() != "e784d9ba-e62b-4c98-892c-6d4c0124fcde" {
		t.Fatalf("ActiveID mismatch: %v", mgr.ActiveID())
	}

	// Persist -> Load round trip
	if err := mgr.Persist(); err != nil {
		t.Fatalf("mgr.Persist() failed: %v", err)
	}

	mgr2 := NewManager(persistPath)
	mgr2.Load()

	list2 := mgr2.List()
	if len(list2) != 2 {
		t.Fatalf("mgr2 len = %d, want 2", len(list2))
	}
	for i := 0; i < 2; i++ {
		if list2[i].ID != list[i].ID || list2[i].Path != list[i].Path || *list2[i].Agent != *list[i].Agent {
			t.Fatalf("round-trip mismatch at %d: %+v vs %+v", i, list2[i], list[i])
		}
	}
	if mgr2.ActiveID() == nil || *mgr2.ActiveID() != *mgr.ActiveID() {
		t.Fatalf("mgr2 ActiveID mismatch: %v vs %v", mgr2.ActiveID(), mgr.ActiveID())
	}
}

// ── Non-ASCII round-trip ──────────────────────────────────────────────

func TestNonASCIIRoundTrip(t *testing.T) {
	tmpDir := t.TempDir()
	specialDir := filepath.Join(tmpDir, "ñandú_á_proy")
	if err := os.Mkdir(specialDir, 0755); err != nil {
		t.Fatalf("Mkdir specialDir failed: %v", err)
	}

	persistPath := filepath.Join(tmpDir, "lango.json")
	mgr := NewManager(persistPath)
	inst, err := mgr.Create(specialDir)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// Verify the raw file contains "ñandú" and "á", not \u escapes
	rawBytes, err := os.ReadFile(persistPath)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	rawStr := string(rawBytes)
	if !strings.Contains(rawStr, "ñandú") || !strings.Contains(rawStr, "á") {
		t.Fatalf("raw file does not contain unescaped ñandú or á: %s", rawStr)
	}
	if strings.Contains(rawStr, `\u00f1`) || strings.Contains(rawStr, `\u00fa`) || strings.Contains(rawStr, `\u00e1`) {
		t.Fatalf("raw file contains unicode escapes: %s", rawStr)
	}

	// Load back
	mgr2 := NewManager(persistPath)
	mgr2.Load()
	if len(mgr2.List()) != 1 {
		t.Fatalf("len = %d, want 1", len(mgr2.List()))
	}
	if mgr2.List()[0].Path != inst.Path {
		t.Fatalf("path = %q, want %q", mgr2.List()[0].Path, inst.Path)
	}
}

// ── Corrupted JSON & Malformed items ──────────────────────────────────

func TestCorruptedJSON(t *testing.T) {
	tmpDir := t.TempDir()
	persistPath := filepath.Join(tmpDir, "lango.json")
	if err := os.WriteFile(persistPath, []byte(`{invalid json`), 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	mgr := NewManager(persistPath)
	mgr.Load()

	if len(mgr.List()) != 0 {
		t.Fatalf("List should be empty on corrupted json, got %d", len(mgr.List()))
	}
	if mgr.ActiveID() != nil {
		t.Fatalf("ActiveID should be nil on corrupted json")
	}
}

func TestItemMissingPathInMiddle(t *testing.T) {
	tmpDir := t.TempDir()
	d1 := filepath.Join(tmpDir, "d1")
	d3 := filepath.Join(tmpDir, "d3")
	_ = os.Mkdir(d1, 0755)
	_ = os.Mkdir(d3, 0755)

	content := `{
  "instances": [
    {"id": "id-1", "path": "` + strings.ReplaceAll(d1, `\`, `\\`) + `"},
    {"id": "id-2"},
    {"id": "id-3", "path": "` + strings.ReplaceAll(d3, `\`, `\\`) + `"}
  ],
  "active": "id-3"
}`

	persistPath := filepath.Join(tmpDir, "lango.json")
	if err := os.WriteFile(persistPath, []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	mgr := NewManager(persistPath)
	mgr.Load()

	// Earlier items kept (id-1), later dropped, active falls back to first remaining (id-1)
	list := mgr.List()
	if len(list) != 1 {
		t.Fatalf("len(list) = %d, want 1", len(list))
	}
	if list[0].ID != "id-1" {
		t.Fatalf("list[0].ID = %q, want id-1", list[0].ID)
	}
	if mgr.ActiveID() == nil || *mgr.ActiveID() != "id-1" {
		t.Fatalf("ActiveID = %v, want id-1", mgr.ActiveID())
	}
}

func TestLoadMissingFile(t *testing.T) {
	mgr := NewManager(filepath.Join(t.TempDir(), "nonexistent.json"))
	mgr.Load()
	if len(mgr.List()) != 0 {
		t.Fatalf("List should be empty")
	}
	if mgr.ActiveID() != nil {
		t.Fatalf("ActiveID should be nil")
	}
}

func TestEmptySerializationFormat(t *testing.T) {
	tmpDir := t.TempDir()
	persistPath := filepath.Join(tmpDir, "lango.json")
	mgr := NewManager(persistPath)
	if err := mgr.Persist(); err != nil {
		t.Fatalf("Persist failed: %v", err)
	}

	rawBytes, _ := os.ReadFile(persistPath)
	var raw map[string]any
	if err := json.Unmarshal(rawBytes, &raw); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	insts, ok := raw["instances"].([]any)
	if !ok || insts == nil {
		t.Fatalf(`"instances" should serialize as [] and not null, got %v`, raw["instances"])
	}
	if len(insts) != 0 {
		t.Fatalf(`"instances" should be empty []`)
	}
	if raw["active"] != nil {
		t.Fatalf(`"active" should be null, got %v`, raw["active"])
	}
}

func TestConcurrentAgentAndClose(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(filepath.Join(dir, "lango.json"))
	proj := filepath.Join(dir, "p")
	if err := os.Mkdir(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	insts := make([]*Instance, 8)
	for i := range insts {
		inst, err := m.Create(proj)
		if err != nil {
			t.Fatal(err)
		}
		insts[i] = inst
	}
	done := make(chan struct{})
	for i, inst := range insts {
		go func(i int, inst *Instance) {
			defer func() { done <- struct{}{} }()
			inst.SetTerm("1", &mockPTY{})
			inst.SetOpenFile("a.txt")
			_ = m.SetAgent(inst, "claude")
			_ = m.List()
			if i%2 == 0 {
				_ = m.Close(inst.ID)
				_ = m.Close(inst.ID)
			}
		}(i, inst)
	}
	for range insts {
		<-done
	}
	if got := len(m.List()); got != 4 {
		t.Fatalf("want 4 instances, got %d", got)
	}
}

// ── Multi-terminal tests ──────────────────────────────────────────────

func TestInstanceMultipleTerms(t *testing.T) {
	inst := &Instance{ID: "i1", Path: "p"}
	p1 := &mockPTY{}
	p2 := &mockPTY{}
	p3 := &mockPTY{}

	inst.SetTerm("1", p1)
	inst.SetTerm("2", p2)
	inst.SetTerm("3", p3)

	got1, ok1 := inst.Term("1")
	if !ok1 || got1 != p1 {
		t.Fatalf("Term(\"1\") = %v, %v; want %v, true", got1, ok1, p1)
	}
	got2, ok2 := inst.Term("2")
	if !ok2 || got2 != p2 {
		t.Fatalf("Term(\"2\") = %v, %v; want %v, true", got2, ok2, p2)
	}
	got3, ok3 := inst.Term("3")
	if !ok3 || got3 != p3 {
		t.Fatalf("Term(\"3\") = %v, %v; want %v, true", got3, ok3, p3)
	}
	if _, ok := inst.Term("4"); ok {
		t.Fatalf("Term(\"4\") should not exist")
	}

	terms := inst.Terms()
	if len(terms) != 3 {
		t.Fatalf("len(Terms()) = %d; want 3", len(terms))
	}
	if terms["1"] != p1 || terms["2"] != p2 || terms["3"] != p3 {
		t.Fatalf("Terms() copy mismatch")
	}

	delete(terms, "1")
	if _, ok := inst.Term("1"); !ok {
		t.Fatalf("mutating returned Terms() affected internal state")
	}
}

func TestInstanceRemoveTerm(t *testing.T) {
	inst := &Instance{ID: "i1", Path: "p"}
	p1 := &mockPTY{}
	pOther := &mockPTY{}

	inst.SetTerm("1", p1)

	inst.RemoveTerm("1", pOther)
	got, ok := inst.Term("1")
	if !ok || got != p1 {
		t.Fatalf("RemoveTerm with different PTY should not remove term")
	}

	inst.RemoveTerm("2", p1)
	got, ok = inst.Term("1")
	if !ok || got != p1 {
		t.Fatalf("RemoveTerm with wrong id should not remove term")
	}

	inst.RemoveTerm("1", p1)
	if _, ok := inst.Term("1"); ok {
		t.Fatalf("RemoveTerm with matching PTY should remove term")
	}
}

func TestManagerCloseTerminatesAllTerms(t *testing.T) {
	tmpDir := t.TempDir()
	p := filepath.Join(tmpDir, "p")
	_ = os.Mkdir(p, 0755)

	mgr := NewManager(filepath.Join(tmpDir, "lango.json"))
	inst, err := mgr.Create(p)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	t1 := &mockPTY{}
	t2 := &mockPTY{}
	t3 := &mockPTY{panics: true}
	t4 := &mockPTY{}
	t5 := &mockPTY{}

	inst.SetTerm("1", t1)
	inst.SetTerm("2", t2)
	inst.SetTerm("3", t3)
	inst.SetTerm("4", t4)
	inst.SetTerm("5", t5)

	if err := mgr.Close(inst.ID); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	for id, term := range map[string]*mockPTY{"1": t1, "2": t2, "3": t3, "4": t4, "5": t5} {
		if term.termCalls != 1 {
			t.Fatalf("term %s termCalls = %d, want 1", id, term.termCalls)
		}
	}
	if mgr.Get(inst.ID) != nil {
		t.Fatalf("instance should be removed after Close")
	}
}

func TestLoadDoesNotRestoreTerms(t *testing.T) {
	tmpDir := t.TempDir()
	persistFile := filepath.Join(tmpDir, "lango.json")
	p := filepath.Join(tmpDir, "p")
	_ = os.Mkdir(p, 0755)

	mgr := NewManager(persistFile)
	inst, err := mgr.Create(p)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	inst.SetTerm("1", &mockPTY{})
	inst.SetTerm("2", &mockPTY{})
	if err := mgr.Persist(); err != nil {
		t.Fatalf("Persist failed: %v", err)
	}

	mgr2 := NewManager(persistFile)
	mgr2.Load()

	loaded := mgr2.Get(inst.ID)
	if loaded == nil {
		t.Fatalf("expected instance to be loaded")
	}
	if len(loaded.Terms()) != 0 {
		t.Fatalf("loaded instance should have 0 terms, got %d", len(loaded.Terms()))
	}
	if _, ok := loaded.Term("1"); ok {
		t.Fatalf("loaded instance should not have term 1")
	}
}
