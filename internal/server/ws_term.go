package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"lango/internal/instances"
	"lango/internal/paths"
	"lango/internal/pty"
)

// getOrCreateBridge returns the existing alive bridge or creates and starts a new one atomically.
func (s *WS) getOrCreateBridge(inst *instances.Instance, termID string) *pty.Bridge {
	s.termMu.Lock()
	defer s.termMu.Unlock()

	term, _ := inst.Term(termID)
	if b, ok := term.(*pty.Bridge); ok && b != nil && b.IsAlive() {
		return b
	}

	newBridgeFn := s.NewBridge
	if newBridgeFn == nil {
		newBridgeFn = func(i *instances.Instance) *pty.Bridge {
			return pty.NewBridge(i.Path, pty.WithDllDir(paths.ExeDir()))
		}
	}

	b := newBridgeFn(inst)
	b.EnableAgentPolling(func(_ string) {
		if s.Mgr != nil {
			aggr := aggregateTerms(inst.Terms())
			_ = s.Mgr.SetAgent(inst, aggr.Agent)
		}
	})
	inst.SetTerm(termID, b)

	if err := b.Start(); err != nil {
		if s.Log != nil {
			s.Log.Warn("bridge.Start failed: %v", err)
		}
	}
	return b
}

func (s *WS) handleTerm(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var inst *instances.Instance
	if s.Mgr != nil {
		inst = s.Mgr.Get(id)
	}
	if inst == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"detail":"forbidden"}`))
		return
	}

	termID := r.PathValue("term_id")
	if termID == "" {
		termID = "1"
	}
	if termID < "1" || termID > "5" || len(termID) != 1 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"detail":"terminal not found"}`))
		return
	}

	originHost := s.Origin
	if strings.Contains(originHost, "://") {
		if u, err := url.Parse(originHost); err == nil && u.Host != "" {
			originHost = u.Host
		}
	}

	opts := &websocket.AcceptOptions{
		OriginPatterns: []string{originHost},
	}
	conn, err := websocket.Accept(w, r, opts)
	if err != nil {
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	bridge := s.getOrCreateBridge(inst, termID)

	replay, inAlt, sub := bridge.SnapshotAndSubscribe()
	defer sub.Close()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	var writeMu sync.Mutex
	writeJSON := func(v any) error {
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		if encErr := enc.Encode(v); encErr != nil {
			return encErr
		}
		msg := bytes.TrimRight(buf.Bytes(), "\r\n")
		writeMu.Lock()
		defer writeMu.Unlock()
		return conn.Write(ctx, websocket.MessageText, msg)
	}

	if bridge.IsAlive() {
		exitInterval := s.ExitCheckInterval
		if exitInterval <= 0 {
			exitInterval = 1 * time.Second
		}
		go func() {
			ticker := time.NewTicker(exitInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					if !bridge.IsAlive() {
						_ = writeJSON(map[string]any{"type": "exit"})
						inst.RemoveTerm(termID, bridge)
						_ = conn.Close(websocket.StatusNormalClosure, "shell exited")
						cancel()
						return
					}
				}
			}
		}()
	}

	if replay != "" {
		initSeq := "\x1bc\x1b[?1049l\x1b[2J\x1b[3J\x1b[H"
		if inAlt {
			initSeq += "\x1b[?1049h"
		}
		if err := writeJSON(map[string]any{
			"type": "data",
			"data": initSeq + replay,
		}); err != nil {
			return
		}
	}

	go func() {
		for {
			msg, ok := sub.Next(ctx)
			if !ok {
				return
			}
			if err := writeJSON(msg); err != nil {
				cancel()
				return
			}
		}
	}()

	pingInterval := s.PingInterval
	if pingInterval <= 0 {
		pingInterval = 20 * time.Second
	}
	go func() {
		ticker := time.NewTicker(pingInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := writeJSON(map[string]any{"type": "ping"}); err != nil {
					cancel()
					return
				}
			}
		}
	}()

	for {
		_, payload, readErr := conn.Read(ctx)
		if readErr != nil {
			break
		}

		dec := json.NewDecoder(bytes.NewReader(payload))
		dec.UseNumber()
		var rawVal any
		if err := dec.Decode(&rawVal); err != nil {
			if s.Log != nil {
				s.Log.Warn("term_ws loop error: %v", err)
			}
			continue
		}

		msg, ok := rawVal.(map[string]any)
		if !ok {
			continue
		}

		mtype, _ := msg["type"].(string)
		switch mtype {
		case "data":
			var dataVal any = ""
			if val, exists := msg["data"]; exists {
				dataVal = val
			}
			if err := bridge.Write(pyStr(dataVal)); err != nil {
				if s.Log != nil {
					s.Log.Warn("term_ws loop error: %v", err)
				}
			}
		case "resize":
			cols := 80
			if val, exists := msg["cols"]; exists {
				c, err := parsePyInt(val)
				if err != nil {
					if s.Log != nil {
						s.Log.Warn("term_ws loop error: %v", err)
					}
					continue
				}
				cols = c
			}

			rows := 24
			if val, exists := msg["rows"]; exists {
				rVal, err := parsePyInt(val)
				if err != nil {
					if s.Log != nil {
						s.Log.Warn("term_ws loop error: %v", err)
					}
					continue
				}
				rows = rVal
			}

			if cols < 1 {
				cols = 1
			}
			if rows < 1 {
				rows = 1
			}
			if err := bridge.Resize(cols, rows); err != nil {
				if s.Log != nil {
					s.Log.Warn("term_ws loop error: %v", err)
				}
			}
		default:
			// Other types ignored
		}
	}
}

// pyStr mimics Python str() conversion for JSON-decoded values.
func pyStr(v any) string {
	if v == nil {
		return "None"
	}
	switch val := v.(type) {
	case string:
		return val
	case bool:
		if val {
			return "True"
		}
		return "False"
	case json.Number:
		s := val.String()
		if !strings.ContainsAny(s, ".eE") {
			return s
		}
		f, err := val.Float64()
		if err != nil {
			return s
		}
		formatted := strconv.FormatFloat(f, 'g', -1, 64)
		if !strings.ContainsAny(formatted, ".eE") && !math.IsNaN(f) && !math.IsInf(f, 0) {
			formatted += ".0"
		}
		return formatted
	case int:
		return strconv.Itoa(val)
	case int64:
		return strconv.FormatInt(val, 10)
	case float64:
		formatted := strconv.FormatFloat(val, 'g', -1, 64)
		if !strings.ContainsAny(formatted, ".eE") && !math.IsNaN(val) && !math.IsInf(val, 0) {
			formatted += ".0"
		}
		return formatted
	case []any, map[string]any:
		// Best-effort Python-like repr serializes slices and maps via json.Marshal.
		b, _ := json.Marshal(val)
		return string(b)
	default:
		return fmt.Sprintf("%v", val)
	}
}

// parsePyInt mimics Python int() conversion for JSON-decoded values.
func parsePyInt(v any) (int, error) {
	switch val := v.(type) {
	case bool:
		if val {
			return 1, nil
		}
		return 0, nil
	case json.Number:
		s := val.String()
		if !strings.ContainsAny(s, ".eE") {
			i, err := strconv.ParseInt(s, 10, 64)
			if err != nil {
				return 0, err
			}
			return int(i), nil
		}
		f, err := val.Float64()
		if err != nil {
			return 0, err
		}
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return 0, errors.New("cannot convert NaN or Inf to int")
		}
		return int(math.Trunc(f)), nil
	case string:
		s := strings.TrimSpace(val)
		if s == "" {
			return 0, errors.New("empty string for int")
		}
		i, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return 0, err
		}
		return int(i), nil
	case int:
		return val, nil
	case int64:
		return int(val), nil
	case float64:
		if math.IsNaN(val) || math.IsInf(val, 0) {
			return 0, errors.New("cannot convert NaN or Inf to int")
		}
		return int(math.Trunc(val)), nil
	default:
		return 0, fmt.Errorf("invalid literal for int(): %v", v)
	}
}
