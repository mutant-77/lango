package server

import (
	"fmt"
	"net/http"
	"sync"

	"lango/internal/applog"
)

// Guard validates Host and Origin headers for incoming TCP requests.
func Guard(next http.Handler, port int, origin string, log *applog.Logger) http.Handler {
	expectedHost := fmt.Sprintf("127.0.0.1:%d", port)
	var once sync.Once
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqOrigin := r.Header.Get("Origin")
		if r.Host != expectedHost || reqOrigin != origin {
			if log != nil {
				log.Warn("Rechazado Host=%s Origin=%s Path=%s", r.Host, reqOrigin, r.URL.Path)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"detail":"forbidden"}`))
			return
		}
		if log != nil {
			once.Do(func() {
				log.Info("WS Origin=%s", reqOrigin)
			})
		}
		next.ServeHTTP(w, r)
	})
}
