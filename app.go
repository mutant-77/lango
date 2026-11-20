package main

import (
	"context"
	"strings"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"lango/internal/applog"
	"lango/internal/winutil"
)

// App struct
type App struct {
	ctx context.Context
}

// NewApp creates a new App application struct.
func NewApp() *App {
	return &App{}
}

// startup is called when the app starts. The context is saved
// so we can call the runtime methods.
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
}

// PickFolder opens a directory picker dialog. Returns empty string on cancel or error.
func (a *App) PickFolder() string {
	res, err := runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{})
	if err != nil {
		return ""
	}
	return res
}

// OpenURL opens http or https links in the default browser.
func (a *App) OpenURL(u string) {
	lower := strings.ToLower(u)
	if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
		return
	}
	applog.New("lango").Info("Abriendo URL: %s", u)
	_ = winutil.ShellExecute(u, "")
}
