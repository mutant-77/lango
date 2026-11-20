package applog

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	wailslogger "github.com/wailsapp/wails/v2/pkg/logger"
	"gopkg.in/natefinch/lumberjack.v2"
)

var (
	mu     sync.Mutex
	writer io.Writer
)

// Init initializes the application rotating log at <dir>/log/lango.log.
func Init(dir string) error {
	mu.Lock()
	defer mu.Unlock()

	logDir := filepath.Join(dir, "log")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return err
	}

	writer = &lumberjack.Logger{
		Filename:   filepath.Join(logDir, "lango.log"),
		MaxSize:    5, // megabytes
		MaxBackups: 3,
		Compress:   false,
	}
	return nil
}

// Logger represents a named logger.
type Logger struct {
	name string
}

// New creates a new Logger instance with the given name.
func New(name string) *Logger {
	return &Logger{name: name}
}

func (l *Logger) log(level string, format string, args ...interface{}) {
	var msg string
	if len(args) > 0 {
		msg = fmt.Sprintf(format, args...)
	} else {
		msg = format
	}
	msg = strings.TrimRight(msg, "\r\n")

	now := time.Now()
	timestamp := fmt.Sprintf("%04d-%02d-%02d %02d:%02d:%02d,%03d",
		now.Year(), now.Month(), now.Day(),
		now.Hour(), now.Minute(), now.Second(),
		now.Nanosecond()/1_000_000)

	line := fmt.Sprintf("%s %s %s %s\n", timestamp, l.name, level, msg)

	mu.Lock()
	defer mu.Unlock()
	if writer != nil {
		_, _ = writer.Write([]byte(line))
	}
}

// Info logs an informational message.
func (l *Logger) Info(format string, args ...interface{}) {
	l.log("INFO", format, args...)
}

// Warn logs a warning message (formatted as WARNING to match Python logging).
func (l *Logger) Warn(format string, args ...interface{}) {
	l.log("WARNING", format, args...)
}

// Error logs an error message.
func (l *Logger) Error(format string, args ...interface{}) {
	l.log("ERROR", format, args...)
}

// Debug logs a debug message.
func (l *Logger) Debug(format string, args ...interface{}) {
	l.log("DEBUG", format, args...)
}

// WailsLoggerAdapter implements github.com/wailsapp/wails/v2/pkg/logger.Logger
// forwarding messages to applog with the logger name "wails".
type WailsLoggerAdapter struct {
	logger *Logger
}

// NewWailsLogger creates a new Wails logger adapter.
func NewWailsLogger() *WailsLoggerAdapter {
	return &WailsLoggerAdapter{
		logger: New("wails"),
	}
}

func (w *WailsLoggerAdapter) Print(message string) {
	w.logger.Info("%s", message)
}

func (w *WailsLoggerAdapter) Trace(message string) {
	w.logger.Debug("%s", message)
}

func (w *WailsLoggerAdapter) Debug(message string) {
	w.logger.Debug("%s", message)
}

func (w *WailsLoggerAdapter) Info(message string) {
	w.logger.Info("%s", message)
}

func (w *WailsLoggerAdapter) Warning(message string) {
	w.logger.Warn("%s", message)
}

func (w *WailsLoggerAdapter) Error(message string) {
	w.logger.Error("%s", message)
}

func (w *WailsLoggerAdapter) Fatal(message string) {
	w.logger.Error("%s", message)
}

// Verify that WailsLoggerAdapter implements wailslogger.Logger.
var _ wailslogger.Logger = (*WailsLoggerAdapter)(nil)
