package server

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"lango/internal/applog"
	"lango/internal/gitinfo"
	"lango/internal/instances"
	"lango/internal/pypath"
)

type fakePTY struct {
	alive      bool
	agent      string
	state      string
	idleS      int
	terminated bool
}

func (f *fakePTY) IsAlive() bool { return f.alive }
func (f *fakePTY) Agent() string { return f.agent }
func (f *fakePTY) State() string { return f.state }
func (f *fakePTY) IdleS() int    { return f.idleS }
func (f *fakePTY) Terminate()    { f.terminated = true; f.alive = false }

type capturedCall struct {
	Target string         `json:"target"`
	Args   []any          `json:"args"`
	Kwargs map[string]any `json:"kwargs,omitempty"`
}

type fakeRunner struct {
	mu       sync.Mutex
	calls    []capturedCall
	wtErr    error
	wtAllErr bool
}

func (f *fakeRunner) ShellExecute(file, dir string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	kw := map[string]any{}
	if dir != "" {
		kw["cwd"] = dir
	}
	f.calls = append(f.calls, capturedCall{
		Target: "os.startfile",
		Args:   []any{file},
		Kwargs: kw,
	})
	return nil
}

func (f *fakeRunner) StartRaw(cmdline string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, capturedCall{
		Target: "subprocess.Popen",
		Args:   []any{cmdline},
	})
	return nil
}

func (f *fakeRunner) Start(argv []string, cwd string, flags uint32) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	argsList := make([]any, len(argv))
	for i, v := range argv {
		argsList[i] = v
	}
	kw := map[string]any{}
	if cwd != "" {
		kw["cwd"] = cwd
	}
	kw["creationflags"] = flags

	f.calls = append(f.calls, capturedCall{
		Target: "subprocess.Popen",
		Args:   []any{argsList},
		Kwargs: kw,
	})

	if f.wtAllErr {
		return fmt.Errorf("simulated system error")
	}
	if f.wtErr != nil && len(argv) > 0 && strings.Contains(argv[0], "wt.exe") {
		return f.wtErr
	}
	return nil
}

func setupTestServer(t *testing.T) (*Server, *instances.Manager, *fakeRunner, string) {
	t.Helper()
	tempDir := t.TempDir()
	persistFile := filepath.Join(tempDir, "lango.json")
	mgr := instances.NewManager(persistFile)
	runner := &fakeRunner{}
	srv := &Server{
		Mgr: mgr,
		Run: runner,
		Log: applog.New("lango.server"),
	}
	return srv, mgr, runner, tempDir
}

// ── 1. PORT OF tests/test_server_file_ops.py ─────────────────────────

func TestGetFile(t *testing.T) {
	srv, mgr, _, tmp := setupTestServer(t)
	proj := filepath.Join(tmp, "proj")
	_ = os.MkdirAll(filepath.Join(proj, "src"), 0755)
	_ = os.WriteFile(filepath.Join(proj, "src", "main.py"), []byte("x = 1\ny = 2\n"), 0644)
	_ = os.WriteFile(filepath.Join(proj, "README.md"), []byte("# readme\n"), 0644)

	inst, err := mgr.Create(proj)
	if err != nil {
		t.Fatal(err)
	}

	handler := srv.Handler()

	// 1. Returns content
	req := httptest.NewRequest("GET", fmt.Sprintf("/file?id=%s&path=src/main.py", inst.ID), nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body["content"] != "x = 1\ny = 2\n" {
		t.Errorf("content mismatch: %v", body["content"])
	}
	if body["lines"] != float64(2) {
		t.Errorf("lines mismatch: %v", body["lines"])
	}
	if body["lang"] != "python" {
		t.Errorf("lang mismatch: %v", body["lang"])
	}
	if inst.OpenFile() != "src/main.py" {
		t.Errorf("open_file not updated: %v", inst.OpenFile())
	}

	// 2. Lang markdown
	req = httptest.NewRequest("GET", fmt.Sprintf("/file?id=%s&path=README.md", inst.ID), nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body["lang"] != "markdown" {
		t.Errorf("lang mismatch: %v", body["lang"])
	}

	// 3. Unknown instance
	req = httptest.NewRequest("GET", "/file?id=does-not-exist&path=foo.py", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 404 {
		t.Errorf("expected 404, got %d", rec.Code)
	}

	// 4. Missing file
	req = httptest.NewRequest("GET", fmt.Sprintf("/file?id=%s&path=no_such_file.py", inst.ID), nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 404 {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

func TestPutFile(t *testing.T) {
	srv, mgr, _, tmp := setupTestServer(t)
	proj := filepath.Join(tmp, "proj")
	_ = os.MkdirAll(filepath.Join(proj, "src"), 0755)
	mainPy := filepath.Join(proj, "src", "main.py")
	_ = os.WriteFile(mainPy, []byte("x = 1\n"), 0644)

	inst, err := mgr.Create(proj)
	if err != nil {
		t.Fatal(err)
	}

	handler := srv.Handler()

	// 1. Writes content
	putBody := map[string]any{
		"id":      inst.ID,
		"path":    "src/main.py",
		"content": "new content\n",
	}
	bBytes, _ := json.Marshal(putBody)
	req := httptest.NewRequest("PUT", "/file", bytes.NewReader(bBytes))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	contentOnDisk, _ := os.ReadFile(mainPy)
	// WriteText converts \n to \r\n
	if string(contentOnDisk) != "new content\r\n" {
		t.Errorf("unexpected disk content: %q", string(contentOnDisk))
	}

	// 2. Unknown instance
	putBody["id"] = "nope"
	bBytes, _ = json.Marshal(putBody)
	req = httptest.NewRequest("PUT", "/file", bytes.NewReader(bBytes))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 404 {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

func TestDeleteFileAndDir(t *testing.T) {
	srv, mgr, _, tmp := setupTestServer(t)
	proj := filepath.Join(tmp, "proj")
	_ = os.MkdirAll(filepath.Join(proj, "src"), 0755)
	readme := filepath.Join(proj, "README.md")
	_ = os.WriteFile(readme, []byte("# readme\n"), 0644)

	inst, err := mgr.Create(proj)
	if err != nil {
		t.Fatal(err)
	}

	handler := srv.Handler()

	// 1. Delete file
	req := httptest.NewRequest("DELETE", fmt.Sprintf("/file?id=%s&path=README.md", inst.ID), nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if _, err := os.Stat(readme); !os.IsNotExist(err) {
		t.Errorf("expected file to be deleted")
	}

	// 2. Delete dir
	srcDir := filepath.Join(proj, "src")
	req = httptest.NewRequest("DELETE", fmt.Sprintf("/file?id=%s&path=src", inst.ID), nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if _, err := os.Stat(srcDir); !os.IsNotExist(err) {
		t.Errorf("expected dir to be deleted")
	}
}

func TestFileOpsRemaining(t *testing.T) {
	srv, mgr, runner, tmp := setupTestServer(t)
	proj := filepath.Join(tmp, "proj")
	_ = os.MkdirAll(proj, 0755)
	_ = os.WriteFile(filepath.Join(proj, "README.md"), []byte("readme"), 0644)

	inst, err := mgr.Create(proj)
	if err != nil {
		t.Fatal(err)
	}

	handler := srv.Handler()

	// Rename
	renBody := map[string]any{"id": inst.ID, "src": "README.md", "dst": "NOTES.md"}
	bb, _ := json.Marshal(renBody)
	req := httptest.NewRequest("POST", "/rename", bytes.NewReader(bb))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if _, err := os.Stat(filepath.Join(proj, "NOTES.md")); err != nil {
		t.Errorf("NOTES.md missing")
	}

	// Mkfile
	mkfBody := map[string]any{"id": inst.ID, "path": "src/utils.py"}
	bb, _ = json.Marshal(mkfBody)
	req = httptest.NewRequest("POST", "/mkfile", bytes.NewReader(bb))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if _, err := os.Stat(filepath.Join(proj, "src", "utils.py")); err != nil {
		t.Errorf("src/utils.py missing")
	}

	// Mkdir
	mkdBody := map[string]any{"id": inst.ID, "path": "lib/helpers"}
	bb, _ = json.Marshal(mkdBody)
	req = httptest.NewRequest("POST", "/mkdir", bytes.NewReader(bb))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if fi, err := os.Stat(filepath.Join(proj, "lib", "helpers")); err != nil || !fi.IsDir() {
		t.Errorf("lib/helpers missing or not dir")
	}

	// Reveal file
	_ = os.WriteFile(filepath.Join(proj, "test file.txt"), []byte("txt"), 0644)
	runner.calls = nil
	revBody := map[string]any{"id": inst.ID, "path": "test file.txt"}
	bb, _ = json.Marshal(revBody)
	req = httptest.NewRequest("POST", "/reveal", bytes.NewReader(bb))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 200 || len(runner.calls) != 1 || !strings.Contains(runner.calls[0].Args[0].(string), "explorer.exe /select,") {
		t.Errorf("reveal file failed: %v", runner.calls)
	}

	// Reveal folder
	runner.calls = nil
	revBody = map[string]any{"id": inst.ID, "path": "lib"}
	bb, _ = json.Marshal(revBody)
	req = httptest.NewRequest("POST", "/reveal", bytes.NewReader(bb))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 200 || len(runner.calls) != 1 || strings.Contains(runner.calls[0].Args[0].(string), "/select") {
		t.Errorf("reveal folder failed: %v", runner.calls)
	}

	// Reveal root
	runner.calls = nil
	revBody = map[string]any{"id": inst.ID, "path": ""}
	bb, _ = json.Marshal(revBody)
	req = httptest.NewRequest("POST", "/reveal", bytes.NewReader(bb))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 200 || len(runner.calls) != 1 {
		t.Errorf("reveal root failed: %v", runner.calls)
	}

	// Reveal missing
	runner.calls = nil
	revBody = map[string]any{"id": inst.ID, "path": "missing.txt"}
	bb, _ = json.Marshal(revBody)
	req = httptest.NewRequest("POST", "/reveal", bytes.NewReader(bb))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 404 || len(runner.calls) != 0 {
		t.Errorf("expected 404 and no calls")
	}

	// Launch file
	_ = os.WriteFile(filepath.Join(proj, "run.bat"), []byte("@echo off\n"), 0644)
	runner.calls = nil
	launchBody := map[string]any{"id": inst.ID, "path": "run.bat"}
	bb, _ = json.Marshal(launchBody)
	req = httptest.NewRequest("POST", "/launch", bytes.NewReader(bb))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 200 || len(runner.calls) != 1 || !strings.HasSuffix(runner.calls[0].Args[0].(string), "run.bat") {
		t.Errorf("launch failed: %v", runner.calls)
	}

	// Launch missing file
	launchBody = map[string]any{"id": inst.ID, "path": "no_such.exe"}
	bb, _ = json.Marshal(launchBody)
	req = httptest.NewRequest("POST", "/launch", bytes.NewReader(bb))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 404 {
		t.Errorf("expected 404")
	}

	// WT open terminal
	runner.calls = nil
	wtBody := map[string]any{"id": inst.ID}
	bb, _ = json.Marshal(wtBody)
	req = httptest.NewRequest("POST", "/wt", bytes.NewReader(bb))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 200 || len(runner.calls) != 1 {
		t.Fatalf("wt failed: %d, calls=%v", rec.Code, runner.calls)
	}

	// WT fallback
	runner.calls = nil
	runner.wtErr = os.ErrNotExist
	req = httptest.NewRequest("POST", "/wt", bytes.NewReader(bb))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var wtResp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &wtResp)
	if wtResp["via"] != "powershell.exe" {
		t.Errorf("expected powershell.exe fallback, got %v", wtResp["via"])
	}
}

// ── 2. PORT OF tests/test_instances_api.py ───────────────────────────

func TestInstancesAPI(t *testing.T) {
	srv, mgr, _, tmp := setupTestServer(t)
	proj := filepath.Join(tmp, "proj")
	_ = os.MkdirAll(proj, 0755)

	inst, err := mgr.Create(proj)
	if err != nil {
		t.Fatal(err)
	}

	handler := srv.Handler()

	// Fresh instance state
	req := httptest.NewRequest("GET", "/instances", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var data map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &data)
	if data["active"] != inst.ID {
		t.Errorf("expected active %s, got %v", inst.ID, data["active"])
	}
	items := data["instances"].([]any)
	item := items[0].(map[string]any)
	if item["state"] != "fresh" || item["agent"] != nil || item["idle_s"] != nil {
		t.Errorf("expected fresh/nil/nil, got %v", item)
	}

	// Live PTY values reflected
	inst.SetTerm("1", &fakePTY{alive: true, agent: "claude", state: "waiting", idleS: 5})
	req = httptest.NewRequest("GET", "/instances", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	_ = json.Unmarshal(rec.Body.Bytes(), &data)
	item = data["instances"].([]any)[0].(map[string]any)
	if item["agent"] != "claude" || item["state"] != "waiting" || item["idle_s"] != float64(5) {
		t.Errorf("expected claude/waiting/5, got %v", item)
	}

	// Dead PTY returns fresh/nil/nil
	inst.SetTerm("1", &fakePTY{alive: false, agent: "claude", state: "waiting", idleS: 5})
	req = httptest.NewRequest("GET", "/instances", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	_ = json.Unmarshal(rec.Body.Bytes(), &data)
	item = data["instances"].([]any)[0].(map[string]any)
	if item["agent"] != nil || item["state"] != "fresh" || item["idle_s"] != nil {
		t.Errorf("expected fresh/nil/nil on dead pty, got %v", item)
	}
}

// ── 3. D5 TESTS & RENAME EDGE CASES ──────────────────────────────────

func TestD5Cases(t *testing.T) {
	srv, mgr, _, tmp := setupTestServer(t)
	proj := filepath.Join(tmp, "proj")
	_ = os.MkdirAll(proj, 0755)
	_ = os.WriteFile(filepath.Join(proj, "..config"), []byte("cfg"), 0644)
	_ = os.WriteFile(filepath.Join(proj, "a.txt"), []byte("content a"), 0644)
	_ = os.WriteFile(filepath.Join(proj, "b.txt"), []byte("content b"), 0644)

	siblingDir := filepath.Join(tmp, "proj2")
	_ = os.MkdirAll(siblingDir, 0755)
	_ = os.WriteFile(filepath.Join(siblingDir, "x.txt"), []byte("x"), 0644)

	inst, err := mgr.Create(proj)
	if err != nil {
		t.Fatal(err)
	}

	handler := srv.Handler()

	// 1. Sibling prefix dir -> 403
	req := httptest.NewRequest("GET", fmt.Sprintf("/file?id=%s&path=../proj2/x.txt", inst.ID), nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Errorf("sibling prefix should return 403, got %d", rec.Code)
	}

	// 2. /tree?path=.. -> 403
	req = httptest.NewRequest("GET", fmt.Sprintf("/tree?id=%s&path=..", inst.ID), nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Errorf("tree path=.. should return 403, got %d", rec.Code)
	}

	// 3. DELETE root -> 403
	req = httptest.NewRequest("DELETE", fmt.Sprintf("/file?id=%s&path=", inst.ID), nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Errorf("delete root should return 403, got %d", rec.Code)
	}
	req = httptest.NewRequest("DELETE", fmt.Sprintf("/file?id=%s&path=.", inst.ID), nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Errorf("delete root with . should return 403, got %d", rec.Code)
	}

	// 4. Absolute path in file route -> 403
	req = httptest.NewRequest("GET", fmt.Sprintf("/file?id=%s&path=C:\\Windows\\win.ini", inst.ID), nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Errorf("absolute path outside should return 403, got %d", rec.Code)
	}

	// 5. ..config file -> allowed
	req = httptest.NewRequest("GET", fmt.Sprintf("/file?id=%s&path=..config", inst.ID), nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Errorf("..config file should be allowed, got %d", rec.Code)
	}

	// 6. Case-only rename succeeds: a.txt -> A.txt
	renBody := map[string]any{"id": inst.ID, "src": "a.txt", "dst": "A.txt"}
	bb, _ := json.Marshal(renBody)
	req = httptest.NewRequest("POST", "/rename", bytes.NewReader(bb))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Errorf("case-only rename should succeed, got %d", rec.Code)
	}

	// 7. Rename onto existing different file -> 500 and both files unchanged
	renBody = map[string]any{"id": inst.ID, "src": "A.txt", "dst": "b.txt"}
	bb, _ = json.Marshal(renBody)
	req = httptest.NewRequest("POST", "/rename", bytes.NewReader(bb))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 500 {
		t.Errorf("rename onto existing file should return 500, got %d", rec.Code)
	}
	aContent, _ := os.ReadFile(filepath.Join(proj, "A.txt"))
	bContent, _ := os.ReadFile(filepath.Join(proj, "b.txt"))
	if string(aContent) != "content a" || string(bContent) != "content b" {
		t.Errorf("both files should remain unchanged")
	}
}

// ── 4. GOLDEN SUITE COMPARISON ───────────────────────────────────────

var uuidRegex = regexp.MustCompile(`\b[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\b`)

func makeNormalizer(projRoot string) func(any, string) any {
	variantsMap := make(map[string]bool)
	raws := []string{
		projRoot,
		pypath.Resolve(projRoot),
	}
	for _, raw := range raws {
		variantsMap[raw] = true
		variantsMap[strings.ReplaceAll(raw, `\`, `/`)] = true
		variantsMap[strings.ReplaceAll(raw, `/`, `\`)] = true
		variantsMap[strings.ReplaceAll(raw, `\`, `\\`)] = true
		variantsMap[strings.ReplaceAll(raw, `/`, `\\`)] = true
	}
	for v := range variantsMap {
		if len(v) > 1 && v[1] == ':' {
			lowerD := strings.ToLower(string(v[0])) + v[1:]
			upperD := strings.ToUpper(string(v[0])) + v[1:]
			variantsMap[lowerD] = true
			variantsMap[upperD] = true
			variantsMap[strings.ReplaceAll(lowerD, `\`, `/`)] = true
			variantsMap[strings.ReplaceAll(upperD, `\`, `/`)] = true
			variantsMap[strings.ReplaceAll(lowerD, `\`, `\\`)] = true
			variantsMap[strings.ReplaceAll(upperD, `\`, `\\`)] = true
		}
	}
	variants := make([]string, 0, len(variantsMap))
	for v := range variantsMap {
		variants = append(variants, v)
	}
	sort.Slice(variants, func(i, j int) bool {
		return len(variants[i]) > len(variants[j])
	})

	normalizeStr := func(s string) string {
		for _, v := range variants {
			s = strings.ReplaceAll(s, v, "<ROOT>")
		}
		s = uuidRegex.ReplaceAllString(s, "<UUID>")
		return s
	}

	var normVal func(any, string) any
	normVal = func(val any, key string) any {
		if key == "mtime" {
			if _, ok := val.(float64); ok {
				return "<MTIME>"
			}
			if _, ok := val.(int64); ok {
				return "<MTIME>"
			}
			if _, ok := val.(int); ok {
				return "<MTIME>"
			}
		}
		if key == "idle_s" {
			if _, ok := val.(float64); ok {
				return "<IDLE>"
			}
			if _, ok := val.(int64); ok {
				return "<IDLE>"
			}
			if _, ok := val.(int); ok {
				return "<IDLE>"
			}
		}
		switch v := val.(type) {
		case string:
			return normalizeStr(v)
		case map[string]any:
			res := make(map[string]any, len(v))
			for k, valItem := range v {
				res[k] = normVal(valItem, k)
			}
			return res
		case []any:
			res := make([]any, len(v))
			for i, elem := range v {
				res[i] = normVal(elem, "")
			}
			return res
		default:
			return val
		}
	}

	return normVal
}

func recordDisk(projRoot string, relpaths []string, mtimeChanged map[string]bool) map[string]any {
	res := make(map[string]any)
	for _, rel := range relpaths {
		normRel := strings.TrimLeft(strings.ReplaceAll(rel, `\`, `/`), "/")
		p := filepath.Join(projRoot, rel)
		lfi, err := os.Lstat(p)
		exists := (err == nil)
		isDir := false
		var bytesHex any = nil
		if exists {
			if lfi.Mode()&os.ModeSymlink != 0 {
				if sfi, serr := os.Stat(p); serr == nil {
					isDir = sfi.IsDir()
				}
			} else {
				isDir = lfi.IsDir()
			}
			if !isDir {
				b, rerr := os.ReadFile(p)
				if rerr == nil {
					bytesHex = hex.EncodeToString(b)
				}
			}
		}
		entry := map[string]any{
			"bytes_hex": bytesHex,
			"exists":    exists,
			"is_dir":    isDir,
		}
		if mtimeChanged != nil {
			if ch, ok := mtimeChanged[normRel]; ok {
				entry["mtime_changed"] = ch
			}
		}
		res[normRel] = entry
	}
	return res
}

func populateProject(projRoot string) {
	_ = os.MkdirAll(filepath.Join(projRoot, "src"), 0755)
	_ = os.WriteFile(filepath.Join(projRoot, "src", "main.py"), []byte("def main():\n    print('hello')\n"), 0644)

	_ = os.MkdirAll(filepath.Join(projRoot, "Docs"), 0755)
	_ = os.WriteFile(filepath.Join(projRoot, "Docs", "guide.md"), []byte("# Guide\nWelcome to docs.\n"), 0644)

	_ = os.MkdirAll(filepath.Join(projRoot, "a"), 0755)
	_ = os.WriteFile(filepath.Join(projRoot, "a", "sample.ts"), []byte("export const a: number = 1;\n"), 0644)

	_ = os.MkdirAll(filepath.Join(projRoot, "sub-dir"), 0755)
	_ = os.WriteFile(filepath.Join(projRoot, "sub-dir", "data.json"), []byte("{\"nested\": true}\n"), 0644)

	for _, ign := range []string{".git", "node_modules", "__pycache__", "dist", "build", ".vscode"} {
		d := filepath.Join(projRoot, ign)
		_ = os.MkdirAll(d, 0755)
		_ = os.WriteFile(filepath.Join(d, "dummy.txt"), []byte("ignored\n"), 0644)
	}

	_ = os.WriteFile(filepath.Join(projRoot, ".env"), []byte("SECRET_KEY=12345\n"), 0644)
	_ = os.MkdirAll(filepath.Join(projRoot, ".hidden_dir"), 0755)
	_ = os.WriteFile(filepath.Join(projRoot, ".hidden_dir", "secret.txt"), []byte("hidden file\n"), 0644)

	_ = os.WriteFile(filepath.Join(projRoot, "b.txt"), []byte("content b\n"), 0644)
	_ = os.WriteFile(filepath.Join(projRoot, "B2.txt"), []byte("content B2\n"), 0644)
	_ = os.WriteFile(filepath.Join(projRoot, "Zeta.md"), []byte("# Zeta\n"), 0644)
	_ = os.WriteFile(filepath.Join(projRoot, "alpha.py"), []byte("alpha = 1\n"), 0644)
	_ = os.MkdirAll(filepath.Join(projRoot, "Alpha_dir"), 0755)
	_ = os.WriteFile(filepath.Join(projRoot, "Alpha_dir", "inner.txt"), []byte("alpha inner\n"), 0644)

	brokenLink := filepath.Join(projRoot, "broken_link")
	_ = os.Remove(brokenLink)
	_ = os.Symlink("non_existent_target", brokenLink)

	_ = os.WriteFile(filepath.Join(projRoot, "..config"), []byte("setting=on\n"), 0644)

	_ = os.WriteFile(filepath.Join(projRoot, "lf.txt"), []byte("a\nb\n"), 0644)
	_ = os.WriteFile(filepath.Join(projRoot, "crlf.txt"), []byte("a\r\nb\r\n"), 0644)
	_ = os.WriteFile(filepath.Join(projRoot, "cr.txt"), []byte("a\rb\r"), 0644)
	_ = os.WriteFile(filepath.Join(projRoot, "invalid_utf8.txt"), []byte("ok\xff\xfeend\xc3(x\n"), 0644)
	_ = os.WriteFile(filepath.Join(projRoot, "bom.txt"), []byte("\xef\xbb\xbfhola\n"), 0644)
	_ = os.WriteFile(filepath.Join(projRoot, "seps.txt"), []byte("a\fb\x1cc\xe2\x80\xa8d\n"), 0644)
	_ = os.WriteFile(filepath.Join(projRoot, "empty.txt"), []byte(""), 0644)

	_ = os.WriteFile(filepath.Join(projRoot, "sample.py"), []byte("print('hello')\n"), 0644)
	_ = os.WriteFile(filepath.Join(projRoot, "sample.md"), []byte("# Title\n"), 0644)
	_ = os.WriteFile(filepath.Join(projRoot, "sample.json"), []byte("{\"key\": \"val\"}\n"), 0644)
	_ = os.WriteFile(filepath.Join(projRoot, "sample.ts"), []byte("const x: number = 42;\n"), 0644)
	_ = os.WriteFile(filepath.Join(projRoot, "sample.go"), []byte("package main\n\nfunc main() {}\n"), 0644)
	_ = os.WriteFile(filepath.Join(projRoot, "sample.xyz"), []byte("custom format content\n"), 0644)

	_ = os.WriteFile(filepath.Join(projRoot, "put_overwrite.txt"), []byte("initial put overwrite\n"), 0644)
	_ = os.WriteFile(filepath.Join(projRoot, "put_nl.txt"), []byte("initial put nl\n"), 0644)
	_ = os.WriteFile(filepath.Join(projRoot, "put_conflict.txt"), []byte("initial put conflict\n"), 0644)
	_ = os.WriteFile(filepath.Join(projRoot, "put_conflict_force.txt"), []byte("initial put conflict force\n"), 0644)
	_ = os.WriteFile(filepath.Join(projRoot, "put_non_ascii.txt"), []byte("initial non ascii\n"), 0644)
	_ = os.WriteFile(filepath.Join(projRoot, "to_delete.txt"), []byte("delete me\n"), 0644)

	_ = os.MkdirAll(filepath.Join(projRoot, "dir_to_delete"), 0755)
	_ = os.WriteFile(filepath.Join(projRoot, "dir_to_delete", "child.txt"), []byte("child content\n"), 0644)

	_ = os.WriteFile(filepath.Join(projRoot, "rename_src.txt"), []byte("source content\n"), 0644)
	_ = os.WriteFile(filepath.Join(projRoot, "rename_src2.txt"), []byte("src2 content\n"), 0644)
	_ = os.WriteFile(filepath.Join(projRoot, "rename_dst2.txt"), []byte("dst2 already exists\n"), 0644)

	_ = os.WriteFile(filepath.Join(projRoot, "mkfile_existing.txt"), []byte("existing mkfile content\n"), 0644)
	_ = os.MkdirAll(filepath.Join(projRoot, "existing_dir"), 0755)

	_ = os.WriteFile(filepath.Join(projRoot, "run.bat"), []byte("@echo off\r\necho bat\r\n"), 0644)
	_ = os.WriteFile(filepath.Join(projRoot, "test file.txt"), []byte("reveal spaced name\n"), 0644)
}

func TestGoldenSuite(t *testing.T) {
	tempDir := t.TempDir()
	projRoot := filepath.Join(tempDir, "project")
	if err := os.MkdirAll(projRoot, 0755); err != nil {
		t.Fatal(err)
	}
	populateProject(projRoot)

	persistFile := filepath.Join(tempDir, "lango.json")
	mgr := instances.NewManager(persistFile)
	runner := &fakeRunner{}
	srv := &Server{
		Mgr: mgr,
		Run: runner,
		Log: applog.New("lango.server"),
	}

	inst, err := mgr.Create(projRoot)
	if err != nil {
		t.Fatal(err)
	}
	instID := inst.ID

	handler := srv.Handler()
	normalizer := makeNormalizer(projRoot)

	goldenDir, _ := filepath.Abs("../../testdata/golden")

	var comparedCount int
	var count500 int
	var count422 int
	var countD5 int

	runGoldenCase := func(
		caseName string,
		method string,
		url string,
		jsonBody any,
		diskPaths []string,
		checkMtimePaths []string,
		capturedCheck bool,
		isD5Case bool,
	) {
		t.Helper()
		comparedCount++

		mtimesBefore := make(map[string]int64)
		for _, cp := range checkMtimePaths {
			p := filepath.Join(projRoot, cp)
			if st, err := os.Stat(p); err == nil {
				mtimesBefore[cp] = st.ModTime().Unix()
			}
		}

		var reqBody io.Reader
		if jsonBody != nil {
			b, _ := json.Marshal(jsonBody)
			reqBody = bytes.NewReader(b)
		}

		req := httptest.NewRequest(method, url, reqBody)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		mtimeChanged := make(map[string]bool)
		for _, cp := range checkMtimePaths {
			p := filepath.Join(projRoot, cp)
			if st, err := os.Stat(p); err == nil {
				if before, ok := mtimesBefore[cp]; ok {
					mtimeChanged[cp] = (st.ModTime().Unix() != before)
				}
			}
		}

		var rawResp any
		if err := json.Unmarshal(rec.Body.Bytes(), &rawResp); err != nil {
			rawResp = rec.Body.String()
		}

		normBody := normalizer(rawResp, "")

		// Read golden file
		goldenFile := filepath.Join(goldenDir, fmt.Sprintf("rest_%s.json", caseName))
		if isD5Case {
			goldenFile = filepath.Join(goldenDir, fmt.Sprintf("d5_%s.json", caseName))
		}
		goldenBytes, err := os.ReadFile(goldenFile)
		if err != nil {
			t.Fatalf("failed to read golden file %s: %v", goldenFile, err)
		}
		var golden map[string]any
		if err := json.Unmarshal(goldenBytes, &golden); err != nil {
			t.Fatalf("failed to parse golden JSON %s: %v", goldenFile, err)
		}

		goldenStatus := int(golden["status"].(float64))

		jsonEqual := func(a, b any) bool {
			ba, _ := json.Marshal(a)
			bb, _ := json.Marshal(b)
			return bytes.Equal(ba, bb)
		}

		// Check status & body with allowed differences
		if isD5Case || caseName == "tree_dotdot" {
			countD5++
			if caseName == "tree_dotdot" {
				d5Bytes, _ := os.ReadFile(filepath.Join(goldenDir, "d5_tree_dotdot.json"))
				var d5Golden map[string]any
				_ = json.Unmarshal(d5Bytes, &d5Golden)
				if rec.Code != 403 {
					t.Fatalf("[%s] D5 expected 403, got %d", caseName, rec.Code)
				}
				if !jsonEqual(normBody, d5Golden["body"]) {
					t.Fatalf("[%s] D5 body mismatch: got %v, want %v", caseName, normBody, d5Golden["body"])
				}
			} else {
				if rec.Code != goldenStatus {
					t.Fatalf("[%s] status mismatch: got %d, want %d", caseName, rec.Code, goldenStatus)
				}
				if !jsonEqual(normBody, golden["body"]) {
					t.Fatalf("[%s] body mismatch: got %v, want %v", caseName, normBody, golden["body"])
				}
			}
		} else if goldenStatus == 500 {
			count500++
			if rec.Code != 500 {
				t.Fatalf("[%s] expected 500, got %d", caseName, rec.Code)
			}
			bm, ok := normBody.(map[string]any)
			if !ok {
				t.Fatalf("[%s] expected 500 body to be object, got %T", caseName, normBody)
			}
			if _, isStr := bm["detail"].(string); !isStr {
				t.Fatalf("[%s] expected 500 detail to be string, got %v", caseName, bm["detail"])
			}
		} else if goldenStatus == 422 {
			count422++
			if rec.Code != 422 {
				t.Fatalf("[%s] expected 422, got %d", caseName, rec.Code)
			}
			bm, ok := normBody.(map[string]any)
			if !ok {
				t.Fatalf("[%s] expected 422 body to be object, got %T", caseName, normBody)
			}
			if _, isList := bm["detail"].([]any); !isList {
				t.Fatalf("[%s] expected 422 detail to be list, got %v", caseName, bm["detail"])
			}
		} else {
			if rec.Code != goldenStatus {
				t.Fatalf("[%s] status mismatch: got %d, want %d. Body: %s", caseName, rec.Code, goldenStatus, rec.Body.String())
			}
			if !jsonEqual(normBody, golden["body"]) {
				t.Fatalf("[%s] body mismatch:\ngot:  %+v\nwant: %+v", caseName, normBody, golden["body"])
			}
		}

		// Check disk if present
		if diskPaths != nil {
			rawDisk := recordDisk(projRoot, diskPaths, mtimeChanged)
			normDisk := normalizer(rawDisk, "")
			if !jsonEqual(normDisk, golden["disk"]) {
				t.Fatalf("[%s] disk mismatch:\ngot:  %+v\nwant: %+v", caseName, normDisk, golden["disk"])
			}
		}

		// Check captured if present
		if capturedCheck {
			var rawCalls []any
			for _, call := range runner.calls {
				callMap := map[string]any{
					"target": call.Target,
					"args":   call.Args,
				}
				if len(call.Kwargs) > 0 {
					callMap["kwargs"] = call.Kwargs
				}
				rawCalls = append(rawCalls, callMap)
			}
			normCaptured := normalizer(rawCalls, "")
			if !jsonEqual(normCaptured, golden["captured"]) {
				t.Fatalf("[%s] captured mismatch:\ngot:  %+v\nwant: %+v", caseName, normCaptured, golden["captured"])
			}
		}
	}

	// ── 1. TREE CASES ────────────────────────────────────────────────
	runGoldenCase("tree_root", "GET", fmt.Sprintf("/tree?id=%s&path=", instID), nil, nil, nil, false, false)
	runGoldenCase("tree_subdir", "GET", fmt.Sprintf("/tree?id=%s&path=src", instID), nil, nil, nil, false, false)
	runGoldenCase("tree_mixed_case", "GET", fmt.Sprintf("/tree?id=%s&path=Alpha_dir", instID), nil, nil, nil, false, false)
	runGoldenCase("tree_dotdot", "GET", fmt.Sprintf("/tree?id=%s&path=..", instID), nil, nil, nil, false, false)
	runGoldenCase("tree_file", "GET", fmt.Sprintf("/tree?id=%s&path=b.txt", instID), nil, nil, nil, false, false)
	runGoldenCase("tree_nonexistent", "GET", fmt.Sprintf("/tree?id=%s&path=nonexistent", instID), nil, nil, nil, false, false)

	// ── 2. FILE GET CASES ────────────────────────────────────────────
	runGoldenCase("file_get_lf", "GET", fmt.Sprintf("/file?id=%s&path=lf.txt", instID), nil, nil, nil, false, false)
	runGoldenCase("file_get_crlf", "GET", fmt.Sprintf("/file?id=%s&path=crlf.txt", instID), nil, nil, nil, false, false)
	runGoldenCase("file_get_cr", "GET", fmt.Sprintf("/file?id=%s&path=cr.txt", instID), nil, nil, nil, false, false)
	runGoldenCase("file_get_invalid_utf8", "GET", fmt.Sprintf("/file?id=%s&path=invalid_utf8.txt", instID), nil, nil, nil, false, false)
	runGoldenCase("file_get_bom", "GET", fmt.Sprintf("/file?id=%s&path=bom.txt", instID), nil, nil, nil, false, false)
	runGoldenCase("file_get_seps", "GET", fmt.Sprintf("/file?id=%s&path=seps.txt", instID), nil, nil, nil, false, false)
	runGoldenCase("file_get_empty", "GET", fmt.Sprintf("/file?id=%s&path=empty.txt", instID), nil, nil, nil, false, false)
	runGoldenCase("file_get_ext_py", "GET", fmt.Sprintf("/file?id=%s&path=sample.py", instID), nil, nil, nil, false, false)
	runGoldenCase("file_get_ext_md", "GET", fmt.Sprintf("/file?id=%s&path=sample.md", instID), nil, nil, nil, false, false)
	runGoldenCase("file_get_ext_json", "GET", fmt.Sprintf("/file?id=%s&path=sample.json", instID), nil, nil, nil, false, false)
	runGoldenCase("file_get_ext_ts", "GET", fmt.Sprintf("/file?id=%s&path=sample.ts", instID), nil, nil, nil, false, false)
	runGoldenCase("file_get_ext_go", "GET", fmt.Sprintf("/file?id=%s&path=sample.go", instID), nil, nil, nil, false, false)
	runGoldenCase("file_get_ext_xyz", "GET", fmt.Sprintf("/file?id=%s&path=sample.xyz", instID), nil, nil, nil, false, false)
	runGoldenCase("file_get_missing", "GET", fmt.Sprintf("/file?id=%s&path=missing.txt", instID), nil, nil, nil, false, false)
	runGoldenCase("file_get_unknown_instance", "GET", "/file?id=00000000-0000-0000-0000-000000000000&path=sample.py", nil, nil, nil, false, false)
	runGoldenCase("file_get_outside", "GET", fmt.Sprintf("/file?id=%s&path=../outside.txt", instID), nil, nil, nil, false, false)

	// ── 3. PUT /file CASES ───────────────────────────────────────────
	runGoldenCase("put_file_normal_overwrite", "PUT", "/file", map[string]any{
		"id":      instID,
		"path":    "put_overwrite.txt",
		"content": "normal overwrite content\n",
	}, []string{"put_overwrite.txt"}, nil, false, false)

	runGoldenCase("put_file_newline", "PUT", "/file", map[string]any{
		"id":      instID,
		"path":    "put_nl.txt",
		"content": "line1\nline2\n",
	}, []string{"put_nl.txt"}, nil, false, false)

	runGoldenCase("put_file_new_file", "PUT", "/file", map[string]any{
		"id":      instID,
		"path":    "put_new.txt",
		"content": "brand new file content\n",
	}, []string{"put_new.txt"}, nil, false, false)

	conflictP := filepath.Join(projRoot, "put_conflict.txt")
	fixedTime := time.Unix(2000000000, 0)
	_ = os.Chtimes(conflictP, fixedTime, fixedTime)
	runGoldenCase("put_file_conflict", "PUT", "/file", map[string]any{
		"id":      instID,
		"path":    "put_conflict.txt",
		"content": "should fail with conflict\n",
		"mtime":   1000000000,
	}, []string{"put_conflict.txt"}, nil, false, false)

	forceP := filepath.Join(projRoot, "put_conflict_force.txt")
	_ = os.Chtimes(forceP, fixedTime, fixedTime)
	runGoldenCase("put_file_conflict_force", "PUT", "/file", map[string]any{
		"id":      instID,
		"path":    "put_conflict_force.txt",
		"content": "force written content\n",
		"mtime":   1000000000,
		"force":   true,
	}, []string{"put_conflict_force.txt"}, nil, false, false)

	runGoldenCase("put_file_non_ascii", "PUT", "/file", map[string]any{
		"id":      instID,
		"path":    "put_non_ascii.txt",
		"content": "ñandú €\n",
	}, []string{"put_non_ascii.txt"}, nil, false, false)

	runGoldenCase("put_file_missing_field", "PUT", "/file", map[string]any{
		"id":   instID,
		"path": "put_overwrite.txt",
	}, nil, nil, false, false)

	runGoldenCase("put_file_wrong_type", "PUT", "/file", map[string]any{
		"id":      instID,
		"path":    "put_overwrite.txt",
		"content": 12345,
	}, nil, nil, false, false)

	runGoldenCase("put_file_unknown_instance", "PUT", "/file", map[string]any{
		"id":      "00000000-0000-0000-0000-000000000000",
		"path":    "put_overwrite.txt",
		"content": "hello",
	}, nil, nil, false, false)

	// ── 4. DELETE /file CASES ────────────────────────────────────────
	runGoldenCase("delete_file_file", "DELETE", fmt.Sprintf("/file?id=%s&path=to_delete.txt", instID), nil, []string{"to_delete.txt"}, nil, false, false)
	runGoldenCase("delete_file_dir_with_content", "DELETE", fmt.Sprintf("/file?id=%s&path=dir_to_delete", instID), nil, []string{"dir_to_delete"}, nil, false, false)

	// ── 5. POST /rename CASES ────────────────────────────────────────
	runGoldenCase("rename_normal", "POST", "/rename", map[string]any{
		"id":  instID,
		"src": "rename_src.txt",
		"dst": "rename_dst.txt",
	}, []string{"rename_src.txt", "rename_dst.txt"}, nil, false, false)

	runGoldenCase("rename_dest_exists", "POST", "/rename", map[string]any{
		"id":  instID,
		"src": "rename_src2.txt",
		"dst": "rename_dst2.txt",
	}, []string{"rename_src2.txt", "rename_dst2.txt"}, nil, false, false)

	// ── 6. POST /mkfile CASES ────────────────────────────────────────
	runGoldenCase("mkfile_new_nested", "POST", "/mkfile", map[string]any{
		"id":   instID,
		"path": "nested/deep/file.txt",
	}, []string{"nested/deep/file.txt"}, nil, false, false)

	mkP := filepath.Join(projRoot, "mkfile_existing.txt")
	fixedOld := time.Unix(1000000000, 0)
	_ = os.Chtimes(mkP, fixedOld, fixedOld)
	runGoldenCase("mkfile_existing", "POST", "/mkfile", map[string]any{
		"id":   instID,
		"path": "mkfile_existing.txt",
	}, []string{"mkfile_existing.txt"}, []string{"mkfile_existing.txt"}, false, false)

	// ── 7. POST /mkdir CASES ─────────────────────────────────────────
	runGoldenCase("mkdir_new_nested", "POST", "/mkdir", map[string]any{
		"id":   instID,
		"path": "nested/deep/newdir",
	}, []string{"nested/deep/newdir"}, nil, false, false)

	runGoldenCase("mkdir_existing", "POST", "/mkdir", map[string]any{
		"id":   instID,
		"path": "existing_dir",
	}, []string{"existing_dir"}, nil, false, false)

	// ── 8. POST /launch, /reveal, /wt CASES ──────────────────────────
	runner.calls = nil
	runGoldenCase("launch_bat", "POST", "/launch", map[string]any{
		"id":   instID,
		"path": "run.bat",
	}, nil, nil, true, false)

	runner.calls = nil
	runGoldenCase("launch_missing", "POST", "/launch", map[string]any{
		"id":   instID,
		"path": "missing.bat",
	}, nil, nil, true, false)

	runner.calls = nil
	runGoldenCase("reveal_file_with_space", "POST", "/reveal", map[string]any{
		"id":   instID,
		"path": "test file.txt",
	}, nil, nil, true, false)

	runner.calls = nil
	runGoldenCase("reveal_folder", "POST", "/reveal", map[string]any{
		"id":   instID,
		"path": "Docs",
	}, nil, nil, true, false)

	runner.calls = nil
	runGoldenCase("reveal_root", "POST", "/reveal", map[string]any{
		"id":   instID,
		"path": "",
	}, nil, nil, true, false)

	runner.calls = nil
	runGoldenCase("reveal_missing", "POST", "/reveal", map[string]any{
		"id":   instID,
		"path": "missing_reveal.txt",
	}, nil, nil, true, false)

	runner.calls = nil
	runGoldenCase("wt_success", "POST", "/wt", map[string]any{
		"id": instID,
	}, nil, nil, true, false)

	runner.calls = nil
	runner.wtErr = os.ErrNotExist
	runGoldenCase("wt_fallback_powershell", "POST", "/wt", map[string]any{
		"id": instID,
	}, nil, nil, true, false)
	runner.wtErr = nil

	runner.calls = nil
	runner.wtAllErr = true
	runGoldenCase("wt_error_500", "POST", "/wt", map[string]any{
		"id": instID,
	}, nil, nil, true, false)
	runner.wtAllErr = false

	// ── 9. INSTANCES CASES ───────────────────────────────────────────
	runGoldenCase("instances_get", "GET", "/instances", nil, nil, nil, false, false)

	subPath := filepath.Join(projRoot, "Docs")
	recPost := httptest.NewRecorder()
	bPost, _ := json.Marshal(map[string]any{"path": subPath})
	handler.ServeHTTP(recPost, httptest.NewRequest("POST", "/instances", bytes.NewReader(bPost)))
	var rawInstPost any
	_ = json.Unmarshal(recPost.Body.Bytes(), &rawInstPost)
	normInstPost := normalizer(rawInstPost, "")

	goldenPostBytes, _ := os.ReadFile(filepath.Join(goldenDir, "rest_instances_post_valid.json"))
	var goldenPost map[string]any
	_ = json.Unmarshal(goldenPostBytes, &goldenPost)
	if recPost.Code != 200 || !reflect.DeepEqual(normInstPost, goldenPost["body"]) {
		t.Fatalf("instances_post_valid mismatch: got %v, want %v", normInstPost, goldenPost["body"])
	}
	comparedCount++

	secondaryID := rawInstPost.(map[string]any)["id"].(string)

	filePath := filepath.Join(projRoot, "b.txt")
	runGoldenCase("instances_post_file_path", "POST", "/instances", map[string]any{
		"path": filePath,
	}, nil, nil, false, false)

	nonexistentPath := filepath.Join(projRoot, "nonexistent_subfolder")
	runGoldenCase("instances_post_nonexistent", "POST", "/instances", map[string]any{
		"path": nonexistentPath,
	}, nil, nil, false, false)

	runGoldenCase("instances_delete", "DELETE", fmt.Sprintf("/instances/%s", secondaryID), nil, nil, nil, false, false)

	runGoldenCase("instances_activate_unknown", "POST", "/instances/00000000-0000-0000-0000-000000000000/activate", nil, nil, nil, false, false)

	// ── 10. NEW D5 CASES ─────────────────────────────────────────────
	runGoldenCase("sibling_prefix", "GET", fmt.Sprintf("/file?id=%s&path=../sibling/x.txt", instID), nil, nil, nil, false, true)
	runGoldenCase("delete_root", "DELETE", fmt.Sprintf("/file?id=%s&path=", instID), nil, nil, nil, false, true)

	t.Logf("Golden suite summary: compared %d cases (allowed diffs: %d 500s, %d 422s, %d D5s)",
		comparedCount, count500, count422, countD5)
}

// ── 11. GIT REST ROUTES (P2) ─────────────────────────────────────

func skipIfNoGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
}

func execGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_LITERAL_PATHSPECS=1",
		"LC_ALL=C",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s failed: %v\nOutput: %s", args, dir, err, string(out))
	}
	return string(out)
}

func initTestRepo(t *testing.T, dir string) {
	t.Helper()
	skipIfNoGit(t)
	execGit(t, dir, "init")
	execGit(t, dir, "config", "user.name", "Test User")
	execGit(t, dir, "config", "user.email", "test@example.com")
}

func TestGitStatus(t *testing.T) {
	srv, mgr, _, tmp := setupTestServer(t)
	handler := srv.Handler()

	t.Run("422 missing id", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/git/status", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != 422 {
			t.Fatalf("expected 422, got %d", rec.Code)
		}
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("unmarshal error: %v", err)
		}
		details, ok := body["detail"].([]any)
		if !ok || len(details) == 0 {
			t.Fatalf("expected detail array, got %v", body["detail"])
		}
	})

	t.Run("404 unknown instance", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/git/status?id=unknown-id", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != 404 {
			t.Fatalf("expected 404, got %d", rec.Code)
		}
		var body map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		if body["detail"] != "instance not found" {
			t.Errorf("expected 'instance not found', got %v", body["detail"])
		}
	})

	t.Run("non repo instance returns 200 repo false", func(t *testing.T) {
		nonRepo := filepath.Join(tmp, "non-repo-status")
		if err := os.MkdirAll(nonRepo, 0755); err != nil {
			t.Fatal(err)
		}
		inst, err := mgr.Create(nonRepo)
		if err != nil {
			t.Fatal(err)
		}

		req := httptest.NewRequest("GET", fmt.Sprintf("/git/status?id=%s", inst.ID), nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("expected 200, got %d", rec.Code)
		}

		var st gitinfo.Status
		if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
			t.Fatalf("unmarshal error: %v", err)
		}
		if st.Repo {
			t.Errorf("expected repo false, got %v", st.Repo)
		}
		if st.Files == nil {
			t.Errorf("expected files to be non-nil []File, got nil")
		}
		if len(st.Files) != 0 {
			t.Errorf("expected 0 files, got %d", len(st.Files))
		}
		if !strings.Contains(rec.Body.String(), `"files":[]`) {
			t.Errorf("expected '\"files\":[]' in json body, got: %s", rec.Body.String())
		}
	})

	t.Run("gitinfo error returns 200 repo false", func(t *testing.T) {
		errDir := filepath.Join(tmp, "err-status")
		_ = os.MkdirAll(errDir, 0755)
		inst, err := mgr.Create(errDir)
		if err != nil {
			t.Fatal(err)
		}

		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		req := httptest.NewRequest("GET", fmt.Sprintf("/git/status?id=%s", inst.ID), nil).WithContext(ctx)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
		var st gitinfo.Status
		_ = json.Unmarshal(rec.Body.Bytes(), &st)
		if st.Repo {
			t.Errorf("expected repo false, got %v", st.Repo)
		}
		if !strings.Contains(rec.Body.String(), `"files":[]`) {
			t.Errorf("expected '\"files\":[]' in json body, got: %s", rec.Body.String())
		}
	})

	t.Run("real git repo with modified file", func(t *testing.T) {
		skipIfNoGit(t)
		repoDir := filepath.Join(tmp, "repo-status")
		if err := os.MkdirAll(repoDir, 0755); err != nil {
			t.Fatal(err)
		}
		initTestRepo(t, repoDir)

		mainFile := filepath.Join(repoDir, "main.go")
		if err := os.WriteFile(mainFile, []byte("package main\n\nfunc main() {\n\tprintln(\"old\")\n}\n"), 0644); err != nil {
			t.Fatal(err)
		}
		execGit(t, repoDir, "add", ".")
		execGit(t, repoDir, "commit", "-m", "initial commit")

		// Modify main.go to replace "old" with "new" so add > 0 and del > 0
		if err := os.WriteFile(mainFile, []byte("package main\n\nfunc main() {\n\tprintln(\"new\")\n}\n"), 0644); err != nil {
			t.Fatal(err)
		}

		inst, err := mgr.Create(repoDir)
		if err != nil {
			t.Fatal(err)
		}

		req := httptest.NewRequest("GET", fmt.Sprintf("/git/status?id=%s", inst.ID), nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("expected 200, got %d", rec.Code)
		}

		var st gitinfo.Status
		if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
			t.Fatalf("unmarshal error: %v", err)
		}
		if !st.Repo {
			t.Fatalf("expected repo true, got %v", st.Repo)
		}
		if len(st.Files) != 1 {
			t.Fatalf("expected 1 file, got %d", len(st.Files))
		}
		f := st.Files[0]
		if f.Path != "main.go" {
			t.Errorf("expected path 'main.go', got %s", f.Path)
		}
		if f.Letter != "M" {
			t.Errorf("expected letter 'M', got %s", f.Letter)
		}
		if f.Add <= 0 || f.Del <= 0 {
			t.Errorf("expected add/del > 0, got add=%d, del=%d", f.Add, f.Del)
		}
	})
}

func TestGitDiff(t *testing.T) {
	srv, mgr, _, tmp := setupTestServer(t)
	handler := srv.Handler()

	t.Run("422 missing params", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/git/diff", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != 422 {
			t.Fatalf("expected 422, got %d", rec.Code)
		}

		req = httptest.NewRequest("GET", "/git/diff?id=some-id", nil)
		rec = httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != 422 {
			t.Fatalf("expected 422, got %d", rec.Code)
		}

		req = httptest.NewRequest("GET", "/git/diff?path=file.txt", nil)
		rec = httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != 422 {
			t.Fatalf("expected 422, got %d", rec.Code)
		}
	})

	t.Run("404 unknown instance", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/git/diff?id=unknown-id&path=main.go", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != 404 {
			t.Fatalf("expected 404, got %d", rec.Code)
		}
		var body map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		if body["detail"] != "instance not found" {
			t.Errorf("expected 'instance not found', got %v", body["detail"])
		}
	})

	t.Run("403 path outside instance", func(t *testing.T) {
		proj := filepath.Join(tmp, "proj-outside")
		_ = os.MkdirAll(proj, 0755)
		inst, err := mgr.Create(proj)
		if err != nil {
			t.Fatal(err)
		}

		req := httptest.NewRequest("GET", fmt.Sprintf("/git/diff?id=%s&path=../outside.txt", inst.ID), nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != 403 {
			t.Fatalf("expected 403, got %d", rec.Code)
		}
		var body map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		if body["detail"] != "path outside instance" {
			t.Errorf("expected 'path outside instance', got %v", body["detail"])
		}
	})

	t.Run("non repo instance returns 200 empty hunks", func(t *testing.T) {
		nonRepo := filepath.Join(tmp, "non-repo-diff")
		_ = os.MkdirAll(nonRepo, 0755)
		inst, err := mgr.Create(nonRepo)
		if err != nil {
			t.Fatal(err)
		}

		req := httptest.NewRequest("GET", fmt.Sprintf("/git/diff?id=%s&path=foo.txt", inst.ID), nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), `"hunks":[]`) {
			t.Errorf("expected '\"hunks\":[]' in json body, got: %s", rec.Body.String())
		}
	})

	t.Run("gitinfo error returns 500", func(t *testing.T) {
		proj := filepath.Join(tmp, "proj-diff-err")
		_ = os.MkdirAll(proj, 0755)
		inst, err := mgr.Create(proj)
		if err != nil {
			t.Fatal(err)
		}

		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		req := httptest.NewRequest("GET", fmt.Sprintf("/git/diff?id=%s&path=foo.txt", inst.ID), nil).WithContext(ctx)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != 500 {
			t.Fatalf("expected 500, got %d", rec.Code)
		}
		var body map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		if body["detail"] == nil || body["detail"] == "" {
			t.Errorf("expected detail in 500 response, got %v", body)
		}
	})

	t.Run("real git repo with modified file", func(t *testing.T) {
		skipIfNoGit(t)
		repoDir := filepath.Join(tmp, "repo-diff")
		if err := os.MkdirAll(repoDir, 0755); err != nil {
			t.Fatal(err)
		}
		initTestRepo(t, repoDir)

		mainFile := filepath.Join(repoDir, "main.go")
		if err := os.WriteFile(mainFile, []byte("package main\n\nfunc main() {\n\tprintln(\"old\")\n}\n"), 0644); err != nil {
			t.Fatal(err)
		}
		execGit(t, repoDir, "add", ".")
		execGit(t, repoDir, "commit", "-m", "initial commit")

		// Modify main.go
		if err := os.WriteFile(mainFile, []byte("package main\n\nfunc main() {\n\tprintln(\"new\")\n}\n"), 0644); err != nil {
			t.Fatal(err)
		}

		inst, err := mgr.Create(repoDir)
		if err != nil {
			t.Fatal(err)
		}

		// Ensure SetOpenFile is not called
		if inst.OpenFile() != "" {
			t.Fatalf("open_file should be empty before request")
		}

		req := httptest.NewRequest("GET", fmt.Sprintf("/git/diff?id=%s&path=main.go", inst.ID), nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("expected 200, got %d", rec.Code)
		}

		if inst.OpenFile() != "" {
			t.Errorf("open_file should remain untouched, got %q", inst.OpenFile())
		}

		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("unmarshal error: %v", err)
		}

		if body["letter"] != "M" {
			t.Errorf("expected letter 'M', got %v", body["letter"])
		}
		if body["binary"] != false {
			t.Errorf("expected binary false, got %v", body["binary"])
		}
		if body["too_large"] != false {
			t.Errorf("expected too_large false, got %v", body["too_large"])
		}
		if body["lang"] != "go" {
			t.Errorf("expected lang 'go', got %v", body["lang"])
		}

		hunks, ok := body["hunks"].([]any)
		if !ok || len(hunks) == 0 {
			t.Fatalf("expected hunks, got %v", body["hunks"])
		}
		hunk := hunks[0].(map[string]any)
		lines, ok := hunk["lines"].([]any)
		if !ok || len(lines) == 0 {
			t.Fatalf("expected lines in hunk, got %v", hunk["lines"])
		}
		for i, line := range lines {
			lineArr, ok := line.([]any)
			if !ok || len(lineArr) != 2 {
				t.Fatalf("line %d: expected 2-element array, got %v", i, line)
			}
			marker, ok1 := lineArr[0].(string)
			text, ok2 := lineArr[1].(string)
			if !ok1 || !ok2 {
				t.Fatalf("line %d: expected string elements, got [%T, %T]", i, lineArr[0], lineArr[1])
			}
			if marker != " " && marker != "+" && marker != "-" {
				t.Errorf("line %d: unexpected marker %q", i, marker)
			}
			_ = text
		}
	})

	t.Run("committed-then-deleted file returns 200 with letter D and minus lines", func(t *testing.T) {
		skipIfNoGit(t)
		repoDir := filepath.Join(tmp, "repo-diff-del")
		if err := os.MkdirAll(repoDir, 0755); err != nil {
			t.Fatal(err)
		}
		initTestRepo(t, repoDir)

		delFile := filepath.Join(repoDir, "delete_me.txt")
		if err := os.WriteFile(delFile, []byte("line 1\nline 2\n"), 0644); err != nil {
			t.Fatal(err)
		}
		execGit(t, repoDir, "add", "delete_me.txt")
		execGit(t, repoDir, "commit", "-m", "commit delete_me")

		// Delete the file from working tree
		if err := os.Remove(delFile); err != nil {
			t.Fatal(err)
		}

		inst, err := mgr.Create(repoDir)
		if err != nil {
			t.Fatal(err)
		}

		req := httptest.NewRequest("GET", fmt.Sprintf("/git/diff?id=%s&path=delete_me.txt", inst.ID), nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("expected 200, got %d", rec.Code)
		}

		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("unmarshal error: %v", err)
		}

		if body["letter"] != "D" {
			t.Errorf("expected letter 'D', got %v", body["letter"])
		}
		if body["lang"] != "text" {
			t.Errorf("expected lang 'text', got %v", body["lang"])
		}

		hunks, ok := body["hunks"].([]any)
		if !ok || len(hunks) == 0 {
			t.Fatalf("expected at least 1 hunk for deleted file, got %v", body["hunks"])
		}
		hunk := hunks[0].(map[string]any)
		lines, ok := hunk["lines"].([]any)
		if !ok || len(lines) == 0 {
			t.Fatalf("expected lines in hunk, got %v", hunk["lines"])
		}

		hasMinus := false
		for i, line := range lines {
			lineArr, ok := line.([]any)
			if !ok || len(lineArr) != 2 {
				t.Fatalf("line %d: expected 2-element array, got %v", i, line)
			}
			marker, _ := lineArr[0].(string)
			if marker == "-" {
				hasMinus = true
			}
		}
		if !hasMinus {
			t.Errorf("expected at least one '-' line in diff of deleted file")
		}
	})
}

func TestD19_Aggregation(t *testing.T) {
	srv, mgr, _, tmp := setupTestServer(t)
	proj := filepath.Join(tmp, "proj")
	_ = os.MkdirAll(proj, 0755)

	inst, err := mgr.Create(proj)
	if err != nil {
		t.Fatal(err)
	}

	handler := srv.Handler()

	// 1. Without any alive terminals: terms is [], state fresh, agent nil, idle_s nil
	req := httptest.NewRequest("GET", "/instances", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var data map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &data)
	item := data["instances"].([]any)[0].(map[string]any)
	if item["state"] != "fresh" || item["agent"] != nil || item["idle_s"] != nil {
		t.Fatalf("expected fresh/nil/nil, got %v", item)
	}
	termsList, ok := item["terms"].([]any)
	if !ok || len(termsList) != 0 {
		t.Fatalf("expected empty terms [], got %v", item["terms"])
	}

	// 2. Terminal 1 busy, agent "agent1", idle_s 10
	//    Terminal 2 waiting, agent "agent2", idle_s 2
	p1 := &fakePTY{alive: true, agent: "agent1", state: "busy", idleS: 10}
	p2 := &fakePTY{alive: true, agent: "agent2", state: "waiting", idleS: 2}
	inst.SetTerm("1", p1)
	inst.SetTerm("2", p2)

	req = httptest.NewRequest("GET", "/instances", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &data)
	item = data["instances"].([]any)[0].(map[string]any)

	// Priority: waiting > busy -> state waiting, idle_s 2
	if item["state"] != "waiting" {
		t.Errorf("expected state 'waiting', got %v", item["state"])
	}
	if item["idle_s"] != float64(2) {
		t.Errorf("expected idle_s 2, got %v", item["idle_s"])
	}
	// Agent: lowest ID terminal with agent -> "agent1"
	if item["agent"] != "agent1" {
		t.Errorf("expected agent 'agent1', got %v", item["agent"])
	}
	termsList = item["terms"].([]any)
	if len(termsList) != 2 || termsList[0] != "1" || termsList[1] != "2" {
		t.Errorf("expected terms ['1', '2'], got %v", termsList)
	}

	// 3. If terminal 1 dies, terminal 2 remains alive
	p1.alive = false
	req = httptest.NewRequest("GET", "/instances", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	_ = json.Unmarshal(rec.Body.Bytes(), &data)
	item = data["instances"].([]any)[0].(map[string]any)

	if item["state"] != "waiting" || item["idle_s"] != float64(2) {
		t.Errorf("expected waiting/2, got state=%v, idle_s=%v", item["state"], item["idle_s"])
	}
	if item["agent"] != "agent2" {
		t.Errorf("expected agent 'agent2' after term 1 died, got %v", item["agent"])
	}
	termsList = item["terms"].([]any)
	if len(termsList) != 1 || termsList[0] != "2" {
		t.Errorf("expected terms ['2'], got %v", termsList)
	}

	// 4. If terminal 2 also dies, back to fresh/nil/nil and empty terms []
	p2.alive = false
	req = httptest.NewRequest("GET", "/instances", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	_ = json.Unmarshal(rec.Body.Bytes(), &data)
	item = data["instances"].([]any)[0].(map[string]any)

	if item["state"] != "fresh" || item["agent"] != nil || item["idle_s"] != nil {
		t.Errorf("expected fresh/nil/nil after all died, got %v", item)
	}
	termsList = item["terms"].([]any)
	if len(termsList) != 0 {
		t.Errorf("expected empty terms [], got %v", termsList)
	}
}

func TestDeleteTerminalEndpoint(t *testing.T) {
	srv, mgr, _, tmp := setupTestServer(t)
	proj := filepath.Join(tmp, "proj")
	_ = os.MkdirAll(proj, 0755)

	inst, err := mgr.Create(proj)
	if err != nil {
		t.Fatal(err)
	}

	handler := srv.Handler()

	// 1. Inexistent session -> 404 with detail
	req := httptest.NewRequest("DELETE", "/instances/nonexistent-id/terms/1", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 404 {
		t.Fatalf("expected 404 for nonexistent instance, got %d", rec.Code)
	}
	var errBody map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &errBody)
	if errBody["detail"] != "instance not found" {
		t.Errorf("expected 'instance not found' detail, got %v", errBody["detail"])
	}

	// 2. Invalid term_id -> 404 with detail
	invalidIDs := []string{"0", "6", "x", "10", "abc"}
	for _, tid := range invalidIDs {
		req = httptest.NewRequest("DELETE", fmt.Sprintf("/instances/%s/terms/%s", inst.ID, tid), nil)
		rec = httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != 404 {
			t.Fatalf("expected 404 for invalid term_id %q, got %d", tid, rec.Code)
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &errBody)
		if errBody["detail"] == nil || errBody["detail"] == "" {
			t.Errorf("expected detail in error body for term_id %q", tid)
		}
	}

	// 3. Existing terminal -> terminates, removes, returns 200 {"ok": true}
	pty2 := &fakePTY{alive: true}
	inst.SetTerm("2", pty2)

	req = httptest.NewRequest("DELETE", fmt.Sprintf("/instances/%s/terms/2", inst.ID), nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var okBody map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &okBody)
	if okBody["ok"] != true {
		t.Errorf("expected ok: true, got %v", okBody)
	}
	if !pty2.terminated {
		t.Errorf("expected pty2 to be terminated")
	}
	if _, exists := inst.Term("2"); exists {
		t.Errorf("expected term 2 to be removed from instance")
	}

	// 4. Repeated DELETE of nonexistent/already-deleted terminal -> idempotent 200 {"ok": true}
	req = httptest.NewRequest("DELETE", fmt.Sprintf("/instances/%s/terms/2", inst.ID), nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("expected 200 for idempotent delete, got %d", rec.Code)
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &okBody)
	if okBody["ok"] != true {
		t.Errorf("expected ok: true on idempotent delete, got %v", okBody)
	}
}
