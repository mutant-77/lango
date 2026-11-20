package server

import (
	"fmt"
	"net/http"
)

// ConfigJS returns an http.HandlerFunc that serves the frontend bootstrap configuration.
func ConfigJS(port int) http.HandlerFunc {
	body := fmt.Sprintf("window.LANGO_WS_BASE = \"ws://127.0.0.1:%d\";\n"+
		"window.pywebview = { api: {\n"+
		"  minimize: () => runtime.WindowMinimise(),\n"+
		"  toggle_maximize: () => runtime.WindowToggleMaximise(),\n"+
		"  close: () => runtime.Quit(),\n"+
		"  pick_folder: async () => (await go.main.App.PickFolder()) || null,\n"+
		"  open_url: u => go.main.App.OpenURL(String(u)),\n"+
		"  resize_to: (w, h) => runtime.WindowSetSize(w, h),\n"+
		"  move_to: (x, y) => runtime.WindowSetPosition(x, y) } };\n"+
		// Wails ignora el doble clic (detail != 1) en zonas de arrastre: maximizar como una barra de título nativa.
		"document.addEventListener('dblclick', e => {\n"+
		"  if (e.button !== 0) return;\n"+
		"  if (getComputedStyle(e.target).getPropertyValue('--wails-draggable').trim() === 'drag') runtime.WindowToggleMaximise();\n"+
		"});\n", port)

	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/javascript")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write([]byte(body))
	}
}
