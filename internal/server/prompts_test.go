package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func doJSON(t *testing.T, h http.Handler, method, target string, body any) *httptest.ResponseRecorder {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(method, target, bytes.NewReader(data))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func doGet(h http.Handler, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", target, nil))
	return rec
}

func TestPromptsEmpty(t *testing.T) {
	srv, _, _, tmp := setupTestServer(t)
	srv.PromptsDir = filepath.Join(tmp, "prompts")

	rec := doGet(srv.Handler(), "/prompts")
	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	list, ok := body["prompts"].([]any)
	if !ok || len(list) != 0 {
		t.Errorf("expected empty list, got %v", body["prompts"])
	}
}

func TestPromptsListing(t *testing.T) {
	srv, _, _, tmp := setupTestServer(t)
	root := filepath.Join(tmp, "prompts")
	srv.PromptsDir = root
	_ = os.MkdirAll(filepath.Join(root, "A"), 0755)
	_ = os.WriteFile(filepath.Join(root, "b.md"), []byte("uno\r\ndos\r\n"), 0644)
	_ = os.WriteFile(filepath.Join(root, "A", "x.md"), []byte("x"), 0644)
	_ = os.WriteFile(filepath.Join(root, "nota.txt"), []byte("no"), 0644)
	_ = os.WriteFile(filepath.Join(root, "grande.md"), []byte(strings.Repeat("a", 256<<10+1)), 0644)

	rec := doGet(srv.Handler(), "/prompts")
	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var body struct {
		Prompts []struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		} `json:"prompts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Prompts) != 2 {
		t.Fatalf("expected 2 prompts, got %d: %s", len(body.Prompts), rec.Body.String())
	}
	if body.Prompts[0].Path != "A/x.md" || body.Prompts[1].Path != "b.md" {
		t.Errorf("unexpected order/paths: %+v", body.Prompts)
	}
	if body.Prompts[1].Content != "uno\ndos\n" {
		t.Errorf("content not normalized: %q", body.Prompts[1].Content)
	}
}

func TestPromptsCRUD(t *testing.T) {
	srv, _, _, tmp := setupTestServer(t)
	root := filepath.Join(tmp, "prompts")
	srv.PromptsDir = root
	handler := srv.Handler()

	rec := doJSON(t, handler, "POST", "/mkfile", map[string]any{"id": PromptsID, "path": "grupo/nuevo.md"})
	if rec.Code != 200 {
		t.Fatalf("mkfile: expected 200, got %d", rec.Code)
	}
	if _, err := os.Stat(filepath.Join(root, "grupo", "nuevo.md")); err != nil {
		t.Fatalf("mkfile did not create file: %v", err)
	}

	rec = doJSON(t, handler, "PUT", "/file", map[string]any{"id": PromptsID, "path": "grupo/nuevo.md", "content": "hola\n"})
	if rec.Code != 200 {
		t.Fatalf("put: expected 200, got %d", rec.Code)
	}
	disk, err := os.ReadFile(filepath.Join(root, "grupo", "nuevo.md"))
	if err != nil || string(disk) != "hola\r\n" {
		t.Errorf("disk content mismatch: %q (%v)", disk, err)
	}

	rec = doGet(handler, fmt.Sprintf("/file?id=%s&path=grupo/nuevo.md", PromptsID))
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if rec.Code != 200 || got["content"] != "hola\n" {
		t.Errorf("get: code %d, content %v", rec.Code, got["content"])
	}

	rec = doJSON(t, handler, "POST", "/rename", map[string]any{"id": PromptsID, "src": "grupo/nuevo.md", "dst": "grupo/movido.md"})
	if rec.Code != 200 {
		t.Fatalf("rename: expected 200, got %d", rec.Code)
	}
	if _, err := os.Stat(filepath.Join(root, "grupo", "nuevo.md")); !os.IsNotExist(err) {
		t.Errorf("rename left source behind: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "grupo", "movido.md")); err != nil {
		t.Errorf("rename target missing: %v", err)
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("DELETE", fmt.Sprintf("/file?id=%s&path=grupo/movido.md", PromptsID), nil))
	if rec.Code != 200 {
		t.Fatalf("delete: expected 200, got %d", rec.Code)
	}
	if _, err := os.Stat(filepath.Join(root, "grupo", "movido.md")); !os.IsNotExist(err) {
		t.Errorf("delete left file behind: %v", err)
	}
}

func TestPromptsOutsideRoot(t *testing.T) {
	srv, _, _, tmp := setupTestServer(t)
	srv.PromptsDir = filepath.Join(tmp, "prompts")
	handler := srv.Handler()

	if rec := doGet(handler, fmt.Sprintf("/file?id=%s&path=../x", PromptsID)); rec.Code != 403 {
		t.Errorf("get ../x: expected 403, got %d", rec.Code)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("DELETE", fmt.Sprintf("/file?id=%s&path=", PromptsID), nil))
	if rec.Code != 403 {
		t.Errorf("delete root: expected 403, got %d", rec.Code)
	}
}

func TestUnknownSessionNoPanic(t *testing.T) {
	srv, _, _, _ := setupTestServer(t)
	handler := srv.Handler()

	if rec := doGet(handler, "/file?id=nope&path=a.md"); rec.Code != 404 {
		t.Errorf("get unknown: expected 404, got %d", rec.Code)
	}
	if rec := doJSON(t, handler, "PUT", "/file", map[string]any{"id": "nope", "path": "a.md", "content": "x"}); rec.Code != 404 {
		t.Errorf("put unknown: expected 404, got %d", rec.Code)
	}
}
