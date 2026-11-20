package main

import (
	"context"
	"embed"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/logger"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/windows"

	"lango/internal/applog"
	"lango/internal/instances"
	"lango/internal/paths"
	"lango/internal/server"
	"lango/internal/winutil"
)

const AppName = "Lango"

//go:embed all:ui
var embeddedUI embed.FS

type noCacheWriter struct {
	http.ResponseWriter
	forcedType string
}

func (w *noCacheWriter) WriteHeader(code int) {
	w.ResponseWriter.Header().Set("Cache-Control", "no-cache")
	if w.forcedType != "" {
		w.ResponseWriter.Header().Set("Content-Type", w.forcedType)
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *noCacheWriter) Write(b []byte) (int, error) {
	w.ResponseWriter.Header().Set("Cache-Control", "no-cache")
	if w.forcedType != "" {
		w.ResponseWriter.Header().Set("Content-Type", w.forcedType)
	}
	return w.ResponseWriter.Write(b)
}

func noCacheMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		rw.Header().Set("Cache-Control", "no-cache")
		var forcedType string
		p := strings.ToLower(req.URL.Path)
		if strings.HasSuffix(p, ".js") {
			forcedType = "text/javascript"
		} else if strings.HasSuffix(p, ".woff2") {
			forcedType = "font/woff2"
		}
		if forcedType != "" {
			rw.Header().Set("Content-Type", forcedType)
		}
		next.ServeHTTP(&noCacheWriter{ResponseWriter: rw, forcedType: forcedType}, req)
	})
}

func main() {
	startTime := time.Now()

	exeDir := paths.ExeDir()
	_ = os.Chdir(exeDir)

	if err := applog.Init(exeDir); err != nil {
		// Proceed even if log initialization fails
	}

	log := applog.New("lango")

	exePath, err := os.Executable()
	if err != nil {
		exePath = os.Args[0]
	}
	log.Info("Arrancando AppName=%s pid=%d exe=%s", AppName, os.Getpid(), exePath)

	if err := winutil.SetAUMID(AppName + ".App"); err != nil {
		log.Warn("Failed to set AUMID: %v", err)
	}

	mgr := instances.NewManager(filepath.Join(exeDir, "lango.json"))
	mgr.Load()
	log.Info("Instancias cargadas: %d", len(mgr.List()))

	port := server.Port(AppName)
	ln, port, err := server.Listen(port, nil)
	if err != nil {
		log.Error("Failed to listen: %v", err)
	} else {
		ws := &server.WS{
			Mgr:    mgr,
			Log:    applog.New("lango.server"),
			Port:   port,
			Origin: "http://wails.localhost",
		}
		go func() {
			_ = http.Serve(ln, ws.Handler())
		}()
	}

	rest := &server.Server{
		Mgr:        mgr,
		Log:        applog.New("lango.server"),
		PromptsDir: filepath.Join(exeDir, "prompts"),
	}

	assetMux := http.NewServeMux()
	assetMux.HandleFunc("GET /lango-config.js", server.ConfigJS(port))
	assetMux.Handle("/", rest.Handler())

	var uiFS fs.FS
	if envDir := os.Getenv("LANGO_UI_DIR"); envDir != "" {
		uiFS = os.DirFS(envDir)
	} else {
		sub, err := fs.Sub(embeddedUI, "ui")
		if err != nil {
			log.Error("Failed to sub ui filesystem: %v", err)
		}
		uiFS = sub
	}

	app := NewApp()

	err = wails.Run(&options.App{
		Title:            "Lango",
		Width:            1312,
		Height:           694,
		MinWidth:         200,
		MinHeight:        100,
		Frameless:        true,
		BackgroundColour: &options.RGBA{R: 28, G: 28, B: 28, A: 255},
		AssetServer: &assetserver.Options{
			Assets:     uiFS,
			Handler:    assetMux,
			Middleware: noCacheMiddleware,
		},
		Logger:             applog.NewWailsLogger(),
		LogLevel:           logger.INFO,
		LogLevelProduction: logger.ERROR,
		Windows: &windows.Options{
			WebviewUserDataPath: filepath.Join(exeDir, "WebView2"),
			Theme:               windows.Dark,
		},
		OnStartup: func(ctx context.Context) {
			app.startup(ctx)
		},
		OnDomReady: func(ctx context.Context) {
			elapsed := time.Since(startTime).Milliseconds()
			log.Info("UI lista en %d ms", elapsed)
		},
		OnShutdown: func(ctx context.Context) {
			_ = mgr.Persist()

			insts := mgr.List()
			var ptyList []instances.PTY
			for _, inst := range insts {
				for _, p := range inst.Terms() {
					if p != nil && p.IsAlive() {
						ptyList = append(ptyList, p)
					}
				}
			}

			if len(ptyList) > 0 {
				var wg sync.WaitGroup
				for _, p := range ptyList {
					wg.Add(1)
					go func(p instances.PTY) {
						defer wg.Done()
						defer func() { _ = recover() }()
						p.Terminate()
					}(p)
				}

				done := make(chan struct{})
				go func() {
					wg.Wait()
					close(done)
				}()

				select {
				case <-done:
				case <-time.After(5 * time.Second):
					log.Warn("PTY termination timed out after 5s")
				}
			}

			for _, inst := range insts {
				inst.StopWatchers()
			}

			_ = mgr.Persist()
			log.Info("Shutdown complete — terminated %d PTYs, persisted state", len(ptyList))

			if ln != nil {
				_ = ln.Close()
			}
		},
		Bind: []interface{}{app},
	})
	if err != nil {
		log.Error("Wails error: %v", err)
	}
}
