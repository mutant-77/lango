package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestConfigJS(t *testing.T) {
	handler := ConfigJS(36076)
	req := httptest.NewRequest("GET", "/lango-config.js", nil)
	rec := httptest.NewRecorder()

	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}

	if ct := rec.Header().Get("Content-Type"); ct != "text/javascript" {
		t.Errorf("expected Content-Type 'text/javascript', got %q", ct)
	}

	if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("expected Cache-Control 'no-cache', got %q", cc)
	}

	expected := "window.LANGO_WS_BASE = \"ws://127.0.0.1:36076\";\n" +
		"window.pywebview = { api: {\n" +
		"  minimize: () => runtime.WindowMinimise(),\n" +
		"  toggle_maximize: () => runtime.WindowToggleMaximise(),\n" +
		"  close: () => runtime.Quit(),\n" +
		"  pick_folder: async () => (await go.main.App.PickFolder()) || null,\n" +
		"  open_url: u => go.main.App.OpenURL(String(u)),\n" +
		"  resize_to: (w, h) => runtime.WindowSetSize(w, h),\n" +
		"  move_to: (x, y) => runtime.WindowSetPosition(x, y) } };\n" +
		"document.addEventListener('dblclick', e => {\n" +
		"  if (e.button !== 0) return;\n" +
		"  if (getComputedStyle(e.target).getPropertyValue('--wails-draggable').trim() === 'drag') runtime.WindowToggleMaximise();\n" +
		"});\n"

	if rec.Body.String() != expected {
		t.Errorf("unexpected body:\ngot:\n%s\nwant:\n%s", rec.Body.String(), expected)
	}
}
