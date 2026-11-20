package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/windows"

	"lango/internal/applog"
	"lango/internal/gitinfo"
	"lango/internal/instances"
	"lango/internal/pypath"
	"lango/internal/tree"
)

var langMap = map[string]string{
	".py":       "python",
	".js":       "javascript",
	".jsx":      "javascript",
	".ts":       "typescript",
	".tsx":      "typescript",
	".css":      "css",
	".html":     "html",
	".htm":      "html",
	".md":       "markdown",
	".markdown": "markdown",
	".json":     "json",
	".sh":       "shell",
	".bash":     "shell",
	".zsh":      "shell",
	".yaml":     "yaml",
	".yml":      "yaml",
	".toml":     "toml",
	".go":       "go",
	".rs":       "rust",
	".java":     "java",
	".c":        "c",
	".h":        "c",
	".cpp":      "cpp",
	".cc":       "cpp",
	".cxx":      "cpp",
}

func extToLang(ext string) string {
	if l, ok := langMap[strings.ToLower(ext)]; ok {
		return l
	}
	return "text"
}

// Server holds dependencies for REST API endpoints.
type Server struct {
	Mgr        *instances.Manager
	Run        Runner
	Log        *applog.Logger
	PromptsDir string
}

type validationItem struct {
	Loc  []any  `json:"loc"`
	Msg  string `json:"msg"`
	Type string `json:"type"`
}

type gitDiffResponse struct {
	Letter   string         `json:"letter"`
	Binary   bool           `json:"binary"`
	TooLarge bool           `json:"too_large"`
	Lang     string         `json:"lang"`
	Hunks    []gitinfo.Hunk `json:"hunks"`
}

func replyJSON(w http.ResponseWriter, code int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(payload)
}

func replyDetail(w http.ResponseWriter, code int, text string) {
	replyJSON(w, code, map[string]any{"detail": text})
}

func replyValidation(w http.ResponseWriter, items []validationItem) {
	replyJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": items})
}

type termAggregation struct {
	State string
	IdleS any
	Agent string
	Terms []string
}

func termStatePriority(s string) int {
	switch s {
	case "waiting":
		return 4
	case "busy":
		return 3
	case "used":
		return 2
	case "fresh":
		return 1
	default:
		return 0
	}
}

func aggregateTerms(terms map[string]instances.PTY) termAggregation {
	aliveIDs := make([]string, 0, len(terms))
	for id, p := range terms {
		if p != nil && p.IsAlive() {
			aliveIDs = append(aliveIDs, id)
		}
	}
	sort.Strings(aliveIDs)

	if len(aliveIDs) == 0 {
		return termAggregation{
			State: "fresh",
			IdleS: nil,
			Agent: "",
			Terms: []string{},
		}
	}

	bestPriority := -1
	bestState := "fresh"
	var bestIdle any = nil

	agent := ""
	for _, id := range aliveIDs {
		p := terms[id]
		if agent == "" && p.Agent() != "" {
			agent = p.Agent()
		}

		prio := termStatePriority(p.State())
		if prio > bestPriority {
			bestPriority = prio
			bestState = p.State()
			bestIdle = p.IdleS()
		}
	}

	return termAggregation{
		State: bestState,
		IdleS: bestIdle,
		Agent: agent,
		Terms: aliveIDs,
	}
}

func (s *Server) instDict(inst *instances.Instance) map[string]any {
	aggr := aggregateTerms(inst.Terms())
	var agent any = nil
	if aggr.Agent != "" {
		agent = aggr.Agent
	}

	return map[string]any{
		"id":     inst.ID,
		"path":   inst.Path,
		"name":   inst.Name(),
		"agent":  agent,
		"state":  aggr.State,
		"idle_s": aggr.IdleS,
		"terms":  aggr.Terms,
	}
}

func decodeJSONBody(r *http.Request) (map[string]any, []validationItem) {
	data, err := io.ReadAll(r.Body)
	if err != nil || len(strings.TrimSpace(string(data))) == 0 {
		return nil, []validationItem{{
			Loc:  []any{"body"},
			Msg:  "Field required",
			Type: "missing",
		}}
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, []validationItem{{
			Loc:  []any{"body"},
			Msg:  "Invalid JSON",
			Type: "json_invalid",
		}}
	}
	return m, nil
}

// Handler returns the configured http.Handler using method patterns.
func (s *Server) Handler() http.Handler {
	if s.Log == nil {
		s.Log = applog.New("lango.server")
	}
	if s.Run == nil {
		s.Run = &RealRunner{}
	}

	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		replyJSON(w, http.StatusOK, map[string]any{"ok": true})
	})

	mux.HandleFunc("GET /instances", func(w http.ResponseWriter, r *http.Request) {
		insts := s.Mgr.List()
		list := make([]map[string]any, len(insts))
		for i, inst := range insts {
			list[i] = s.instDict(inst)
		}
		var active any = nil
		if actID := s.Mgr.ActiveID(); actID != nil {
			active = *actID
		}
		replyJSON(w, http.StatusOK, map[string]any{
			"instances": list,
			"active":    active,
		})
	})

	mux.HandleFunc("POST /instances", func(w http.ResponseWriter, r *http.Request) {
		body, errs := decodeJSONBody(r)
		if len(errs) > 0 {
			replyValidation(w, errs)
			return
		}
		rawPath, ok := body["path"]
		if !ok {
			replyValidation(w, []validationItem{{Loc: []any{"body", "path"}, Msg: "Field required", Type: "missing"}})
			return
		}
		pathStr, okStr := rawPath.(string)
		if !okStr {
			replyValidation(w, []validationItem{{Loc: []any{"body", "path"}, Msg: "Input should be a valid string", Type: "string_type"}})
			return
		}

		inst, err := s.Mgr.Create(pathStr)
		if err != nil {
			s.Log.Warn("create_instance failed: %s", err)
			replyDetail(w, http.StatusBadRequest, err.Error())
			return
		}
		s.Log.Info("Created instance %s (%s)", inst.ID, inst.Path)
		replyJSON(w, http.StatusOK, s.instDict(inst))
	})

	mux.HandleFunc("DELETE /instances/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		_ = s.Mgr.Close(id)
		s.Log.Info("Closed instance %s", id)
		replyJSON(w, http.StatusOK, map[string]any{"ok": true})
	})

	mux.HandleFunc("DELETE /instances/{id}/terms/{term_id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		inst := s.Mgr.Get(id)
		if inst == nil {
			replyDetail(w, http.StatusNotFound, "instance not found")
			return
		}
		termID := r.PathValue("term_id")
		if termID < "1" || termID > "5" || len(termID) != 1 {
			replyDetail(w, http.StatusNotFound, "terminal not found")
			return
		}
		if term, ok := inst.Term(termID); ok && term != nil {
			term.Terminate()
			inst.RemoveTerm(termID, term)
		}
		replyJSON(w, http.StatusOK, map[string]any{"ok": true})
	})

	mux.HandleFunc("POST /instances/{id}/activate", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		inst := s.Mgr.Get(id)
		if inst == nil {
			replyDetail(w, http.StatusNotFound, "instance not found")
			return
		}
		_ = s.Mgr.Activate(id)
		replyJSON(w, http.StatusOK, map[string]any{"ok": true})
	})

	mux.HandleFunc("GET /prompts", s.handlePrompts)

	mux.HandleFunc("GET /tree", func(w http.ResponseWriter, r *http.Request) {
		if !r.URL.Query().Has("id") {
			replyValidation(w, []validationItem{{Loc: []any{"query", "id"}, Msg: "Field required", Type: "missing"}})
			return
		}
		id := r.URL.Query().Get("id")
		inst := s.Mgr.Get(id)
		if inst == nil {
			replyDetail(w, http.StatusNotFound, "instance not found")
			return
		}

		path := r.URL.Query().Get("path")
		target := pypath.Join(inst.Path, path)
		if !pypath.Contained(inst.Path, target) {
			replyDetail(w, http.StatusForbidden, "path outside instance")
			return
		}

		entries, err := tree.Build(inst.Path, path)
		if err != nil {
			replyDetail(w, http.StatusInternalServerError, err.Error())
			return
		}
		replyJSON(w, http.StatusOK, entries)
	})

	mux.HandleFunc("GET /file", func(w http.ResponseWriter, r *http.Request) {
		var errs []validationItem
		if !r.URL.Query().Has("id") {
			errs = append(errs, validationItem{Loc: []any{"query", "id"}, Msg: "Field required", Type: "missing"})
		}
		if !r.URL.Query().Has("path") {
			errs = append(errs, validationItem{Loc: []any{"query", "path"}, Msg: "Field required", Type: "missing"})
		}
		if len(errs) > 0 {
			replyValidation(w, errs)
			return
		}

		id := r.URL.Query().Get("id")
		path := r.URL.Query().Get("path")
		root, inst, ok := s.rootFor(id)
		if !ok {
			replyDetail(w, http.StatusNotFound, "instance not found")
			return
		}

		target := pypath.Join(root, path)
		if !pypath.Contained(root, target) {
			replyDetail(w, http.StatusForbidden, "path outside instance")
			return
		}

		absPath := pypath.Resolve(target)
		st, err := os.Stat(absPath)
		if err != nil || st.IsDir() {
			replyDetail(w, http.StatusNotFound, "file not found")
			return
		}

		rawBytes, err := os.ReadFile(absPath)
		if err != nil {
			replyDetail(w, http.StatusInternalServerError, err.Error())
			return
		}

		content, lines := ReadText(rawBytes)
		mtime := st.ModTime().Unix()
		lang := extToLang(filepath.Ext(absPath))
		if inst != nil {
			inst.SetOpenFile(path)
		}

		replyJSON(w, http.StatusOK, map[string]any{
			"content": content,
			"lines":   lines,
			"mtime":   mtime,
			"lang":    lang,
		})
	})

	mux.HandleFunc("GET /git/status", func(w http.ResponseWriter, r *http.Request) {
		if !r.URL.Query().Has("id") {
			replyValidation(w, []validationItem{{Loc: []any{"query", "id"}, Msg: "Field required", Type: "missing"}})
			return
		}

		id := r.URL.Query().Get("id")
		inst := s.Mgr.Get(id)
		if inst == nil {
			replyDetail(w, http.StatusNotFound, "instance not found")
			return
		}

		st, err := gitinfo.GetStatus(r.Context(), inst.Path)
		if err != nil {
			s.Log.Warn("git status failed for %s: %s", inst.Path, err)
			replyJSON(w, http.StatusOK, gitinfo.Status{
				Repo:  false,
				Files: []gitinfo.File{},
			})
			return
		}

		if st.Files == nil {
			st.Files = []gitinfo.File{}
		}

		replyJSON(w, http.StatusOK, st)
	})

	mux.HandleFunc("GET /git/diff", func(w http.ResponseWriter, r *http.Request) {
		var errs []validationItem
		if !r.URL.Query().Has("id") {
			errs = append(errs, validationItem{Loc: []any{"query", "id"}, Msg: "Field required", Type: "missing"})
		}
		if !r.URL.Query().Has("path") {
			errs = append(errs, validationItem{Loc: []any{"query", "path"}, Msg: "Field required", Type: "missing"})
		}
		if len(errs) > 0 {
			replyValidation(w, errs)
			return
		}

		id := r.URL.Query().Get("id")
		path := r.URL.Query().Get("path")
		inst := s.Mgr.Get(id)
		if inst == nil {
			replyDetail(w, http.StatusNotFound, "instance not found")
			return
		}

		target := pypath.Join(inst.Path, path)
		if !pypath.Contained(inst.Path, target) {
			replyDetail(w, http.StatusForbidden, "path outside instance")
			return
		}

		rel, err := filepath.Rel(inst.Path, target)
		if err != nil {
			replyDetail(w, http.StatusInternalServerError, err.Error())
			return
		}
		rel = filepath.ToSlash(rel)

		diff, err := gitinfo.GetDiff(r.Context(), inst.Path, rel)
		if err != nil {
			replyDetail(w, http.StatusInternalServerError, err.Error())
			return
		}

		hunks := diff.Hunks
		if hunks == nil {
			hunks = []gitinfo.Hunk{}
		}

		replyJSON(w, http.StatusOK, gitDiffResponse{
			Letter:   diff.Letter,
			Binary:   diff.Binary,
			TooLarge: diff.TooLarge,
			Lang:     extToLang(filepath.Ext(path)),
			Hunks:    hunks,
		})
	})

	mux.HandleFunc("PUT /file", func(w http.ResponseWriter, r *http.Request) {
		body, errs := decodeJSONBody(r)
		if len(errs) > 0 {
			replyValidation(w, errs)
			return
		}

		var vErrs []validationItem
		rawID, hasID := body["id"]
		if !hasID {
			vErrs = append(vErrs, validationItem{Loc: []any{"body", "id"}, Msg: "Field required", Type: "missing"})
		} else if _, ok := rawID.(string); !ok {
			vErrs = append(vErrs, validationItem{Loc: []any{"body", "id"}, Msg: "Input should be a valid string", Type: "string_type"})
		}

		rawPath, hasPath := body["path"]
		if !hasPath {
			vErrs = append(vErrs, validationItem{Loc: []any{"body", "path"}, Msg: "Field required", Type: "missing"})
		} else if _, ok := rawPath.(string); !ok {
			vErrs = append(vErrs, validationItem{Loc: []any{"body", "path"}, Msg: "Input should be a valid string", Type: "string_type"})
		}

		rawContent, hasContent := body["content"]
		if !hasContent {
			vErrs = append(vErrs, validationItem{Loc: []any{"body", "content"}, Msg: "Field required", Type: "missing"})
		} else if _, ok := rawContent.(string); !ok {
			vErrs = append(vErrs, validationItem{Loc: []any{"body", "content"}, Msg: "Input should be a valid string", Type: "string_type"})
		}

		var mtime *int64
		if rawMtime, hasMtime := body["mtime"]; hasMtime && rawMtime != nil {
			if num, ok := rawMtime.(float64); ok {
				val := int64(num)
				mtime = &val
			} else {
				vErrs = append(vErrs, validationItem{Loc: []any{"body", "mtime"}, Msg: "Input should be a valid integer", Type: "int_type"})
			}
		}

		force := false
		if rawForce, hasForce := body["force"]; hasForce && rawForce != nil {
			if b, ok := rawForce.(bool); ok {
				force = b
			} else {
				vErrs = append(vErrs, validationItem{Loc: []any{"body", "force"}, Msg: "Input should be a valid boolean", Type: "bool_type"})
			}
		}

		if len(vErrs) > 0 {
			replyValidation(w, vErrs)
			return
		}

		id := rawID.(string)
		path := rawPath.(string)
		content := rawContent.(string)

		root, _, ok := s.rootFor(id)
		if !ok {
			replyDetail(w, http.StatusNotFound, "instance not found")
			return
		}

		target := pypath.Join(root, path)
		if !pypath.Contained(root, target) {
			replyDetail(w, http.StatusForbidden, "path outside instance")
			return
		}

		absPath := pypath.Resolve(target)
		if !force && mtime != nil {
			if st, err := os.Stat(absPath); err == nil {
				diskMtime := st.ModTime().Unix()
				if diskMtime > *mtime {
					replyJSON(w, http.StatusConflict, map[string]any{
						"detail": map[string]any{
							"conflict": true,
							"mtime":    diskMtime,
						},
					})
					return
				}
			}
		}

		toWrite := WriteText(content)
		if err := os.WriteFile(absPath, toWrite, 0666); err != nil {
			replyDetail(w, http.StatusInternalServerError, err.Error())
			return
		}

		st, err := os.Stat(absPath)
		if err != nil {
			replyDetail(w, http.StatusInternalServerError, err.Error())
			return
		}
		newMtime := st.ModTime().Unix()

		replyJSON(w, http.StatusOK, map[string]any{
			"ok":    true,
			"bytes": len([]byte(content)),
			"mtime": newMtime,
		})
	})

	mux.HandleFunc("DELETE /file", func(w http.ResponseWriter, r *http.Request) {
		var errs []validationItem
		if !r.URL.Query().Has("id") {
			errs = append(errs, validationItem{Loc: []any{"query", "id"}, Msg: "Field required", Type: "missing"})
		}
		if !r.URL.Query().Has("path") {
			errs = append(errs, validationItem{Loc: []any{"query", "path"}, Msg: "Field required", Type: "missing"})
		}
		if len(errs) > 0 {
			replyValidation(w, errs)
			return
		}

		id := r.URL.Query().Get("id")
		path := r.URL.Query().Get("path")
		root, _, ok := s.rootFor(id)
		if !ok {
			replyDetail(w, http.StatusNotFound, "instance not found")
			return
		}

		target := pypath.Join(root, path)
		if !pypath.Contained(root, target) {
			replyDetail(w, http.StatusForbidden, "path outside instance")
			return
		}

		absPath := pypath.Resolve(target)
		rootResolved := pypath.Resolve(root)
		if strings.EqualFold(absPath, rootResolved) {
			replyDetail(w, http.StatusForbidden, "path outside instance")
			return
		}

		st, err := os.Stat(absPath)
		if err != nil {
			replyDetail(w, http.StatusInternalServerError, err.Error())
			return
		}

		if st.IsDir() {
			err = os.RemoveAll(absPath)
		} else {
			err = os.Remove(absPath)
		}
		if err != nil {
			replyDetail(w, http.StatusInternalServerError, err.Error())
			return
		}

		replyJSON(w, http.StatusOK, map[string]any{"ok": true})
	})

	mux.HandleFunc("POST /rename", func(w http.ResponseWriter, r *http.Request) {
		body, errs := decodeJSONBody(r)
		if len(errs) > 0 {
			replyValidation(w, errs)
			return
		}

		var vErrs []validationItem
		rawID, hasID := body["id"]
		if !hasID {
			vErrs = append(vErrs, validationItem{Loc: []any{"body", "id"}, Msg: "Field required", Type: "missing"})
		} else if _, ok := rawID.(string); !ok {
			vErrs = append(vErrs, validationItem{Loc: []any{"body", "id"}, Msg: "Input should be a valid string", Type: "string_type"})
		}

		rawSrc, hasSrc := body["src"]
		if !hasSrc {
			vErrs = append(vErrs, validationItem{Loc: []any{"body", "src"}, Msg: "Field required", Type: "missing"})
		} else if _, ok := rawSrc.(string); !ok {
			vErrs = append(vErrs, validationItem{Loc: []any{"body", "src"}, Msg: "Input should be a valid string", Type: "string_type"})
		}

		rawDst, hasDst := body["dst"]
		if !hasDst {
			vErrs = append(vErrs, validationItem{Loc: []any{"body", "dst"}, Msg: "Field required", Type: "missing"})
		} else if _, ok := rawDst.(string); !ok {
			vErrs = append(vErrs, validationItem{Loc: []any{"body", "dst"}, Msg: "Input should be a valid string", Type: "string_type"})
		}

		if len(vErrs) > 0 {
			replyValidation(w, vErrs)
			return
		}

		id := rawID.(string)
		src := rawSrc.(string)
		dst := rawDst.(string)

		root, _, ok := s.rootFor(id)
		if !ok {
			replyDetail(w, http.StatusNotFound, "instance not found")
			return
		}

		srcTarget := pypath.Join(root, src)
		dstTarget := pypath.Join(root, dst)
		if !pypath.Contained(root, srcTarget) || !pypath.Contained(root, dstTarget) {
			replyDetail(w, http.StatusForbidden, "path outside instance")
			return
		}

		srcResolved := pypath.Resolve(srcTarget)
		dstResolved := pypath.Resolve(dstTarget)

		srcFi, srcErr := os.Stat(srcResolved)
		if srcErr != nil {
			replyDetail(w, http.StatusInternalServerError, srcErr.Error())
			return
		}

		dstFi, dstErr := os.Stat(dstResolved)
		if dstErr == nil && !os.SameFile(srcFi, dstFi) {
			replyDetail(w, http.StatusInternalServerError, fmt.Sprintf("destination already exists: %s", dstResolved))
			return
		}

		if err := os.Rename(srcResolved, dstResolved); err != nil {
			replyDetail(w, http.StatusInternalServerError, err.Error())
			return
		}

		replyJSON(w, http.StatusOK, map[string]any{"ok": true})
	})

	mux.HandleFunc("POST /mkfile", func(w http.ResponseWriter, r *http.Request) {
		body, errs := decodeJSONBody(r)
		if len(errs) > 0 {
			replyValidation(w, errs)
			return
		}

		var vErrs []validationItem
		rawID, hasID := body["id"]
		if !hasID {
			vErrs = append(vErrs, validationItem{Loc: []any{"body", "id"}, Msg: "Field required", Type: "missing"})
		} else if _, ok := rawID.(string); !ok {
			vErrs = append(vErrs, validationItem{Loc: []any{"body", "id"}, Msg: "Input should be a valid string", Type: "string_type"})
		}

		rawPath, hasPath := body["path"]
		if !hasPath {
			vErrs = append(vErrs, validationItem{Loc: []any{"body", "path"}, Msg: "Field required", Type: "missing"})
		} else if _, ok := rawPath.(string); !ok {
			vErrs = append(vErrs, validationItem{Loc: []any{"body", "path"}, Msg: "Input should be a valid string", Type: "string_type"})
		}

		if len(vErrs) > 0 {
			replyValidation(w, vErrs)
			return
		}

		id := rawID.(string)
		path := rawPath.(string)

		root, _, ok := s.rootFor(id)
		if !ok {
			replyDetail(w, http.StatusNotFound, "instance not found")
			return
		}

		target := pypath.Join(root, path)
		if !pypath.Contained(root, target) {
			replyDetail(w, http.StatusForbidden, "path outside instance")
			return
		}

		absPath := pypath.Resolve(target)
		if err := os.MkdirAll(filepath.Dir(absPath), 0755); err != nil {
			replyDetail(w, http.StatusInternalServerError, err.Error())
			return
		}

		if _, err := os.Stat(absPath); err == nil {
			now := time.Now()
			if err := os.Chtimes(absPath, now, now); err != nil {
				replyDetail(w, http.StatusInternalServerError, err.Error())
				return
			}
		} else {
			if err := os.WriteFile(absPath, []byte{}, 0666); err != nil {
				replyDetail(w, http.StatusInternalServerError, err.Error())
				return
			}
		}

		replyJSON(w, http.StatusOK, map[string]any{"ok": true})
	})

	mux.HandleFunc("POST /mkdir", func(w http.ResponseWriter, r *http.Request) {
		body, errs := decodeJSONBody(r)
		if len(errs) > 0 {
			replyValidation(w, errs)
			return
		}

		var vErrs []validationItem
		rawID, hasID := body["id"]
		if !hasID {
			vErrs = append(vErrs, validationItem{Loc: []any{"body", "id"}, Msg: "Field required", Type: "missing"})
		} else if _, ok := rawID.(string); !ok {
			vErrs = append(vErrs, validationItem{Loc: []any{"body", "id"}, Msg: "Input should be a valid string", Type: "string_type"})
		}

		rawPath, hasPath := body["path"]
		if !hasPath {
			vErrs = append(vErrs, validationItem{Loc: []any{"body", "path"}, Msg: "Field required", Type: "missing"})
		} else if _, ok := rawPath.(string); !ok {
			vErrs = append(vErrs, validationItem{Loc: []any{"body", "path"}, Msg: "Input should be a valid string", Type: "string_type"})
		}

		if len(vErrs) > 0 {
			replyValidation(w, vErrs)
			return
		}

		id := rawID.(string)
		path := rawPath.(string)

		inst := s.Mgr.Get(id)
		if inst == nil {
			replyDetail(w, http.StatusNotFound, "instance not found")
			return
		}

		target := pypath.Join(inst.Path, path)
		if !pypath.Contained(inst.Path, target) {
			replyDetail(w, http.StatusForbidden, "path outside instance")
			return
		}

		absPath := pypath.Resolve(target)
		if err := os.MkdirAll(absPath, 0755); err != nil {
			replyDetail(w, http.StatusInternalServerError, err.Error())
			return
		}

		replyJSON(w, http.StatusOK, map[string]any{"ok": true})
	})

	mux.HandleFunc("POST /reveal", func(w http.ResponseWriter, r *http.Request) {
		body, errs := decodeJSONBody(r)
		if len(errs) > 0 {
			replyValidation(w, errs)
			return
		}

		var vErrs []validationItem
		rawID, hasID := body["id"]
		if !hasID {
			vErrs = append(vErrs, validationItem{Loc: []any{"body", "id"}, Msg: "Field required", Type: "missing"})
		} else if _, ok := rawID.(string); !ok {
			vErrs = append(vErrs, validationItem{Loc: []any{"body", "id"}, Msg: "Input should be a valid string", Type: "string_type"})
		}

		path := ""
		if rawPath, hasPath := body["path"]; hasPath && rawPath != nil {
			if s, ok := rawPath.(string); ok {
				path = s
			} else {
				vErrs = append(vErrs, validationItem{Loc: []any{"body", "path"}, Msg: "Input should be a valid string", Type: "string_type"})
			}
		}

		if len(vErrs) > 0 {
			replyValidation(w, vErrs)
			return
		}

		id := rawID.(string)
		root, _, ok := s.rootFor(id)
		if !ok {
			replyDetail(w, http.StatusNotFound, "instance not found")
			return
		}

		target := pypath.Join(root, path)
		if !pypath.Contained(root, target) {
			replyDetail(w, http.StatusForbidden, "path outside instance")
			return
		}

		absPath := pypath.Resolve(target)
		st, err := os.Stat(absPath)
		if err != nil {
			replyDetail(w, http.StatusNotFound, "path not found")
			return
		}

		var cmdline string
		if st.IsDir() {
			cmdline = fmt.Sprintf(`explorer.exe "%s"`, absPath)
		} else {
			cmdline = fmt.Sprintf(`explorer.exe /select,"%s"`, absPath)
		}

		if err := s.Run.StartRaw(cmdline); err != nil {
			s.Log.Warn("reveal failed for %s: %s", absPath, err)
			replyDetail(w, http.StatusInternalServerError, err.Error())
			return
		}

		replyJSON(w, http.StatusOK, map[string]any{"ok": true})
	})

	mux.HandleFunc("POST /launch", func(w http.ResponseWriter, r *http.Request) {
		body, errs := decodeJSONBody(r)
		if len(errs) > 0 {
			replyValidation(w, errs)
			return
		}

		var vErrs []validationItem
		rawID, hasID := body["id"]
		if !hasID {
			vErrs = append(vErrs, validationItem{Loc: []any{"body", "id"}, Msg: "Field required", Type: "missing"})
		} else if _, ok := rawID.(string); !ok {
			vErrs = append(vErrs, validationItem{Loc: []any{"body", "id"}, Msg: "Input should be a valid string", Type: "string_type"})
		}

		rawPath, hasPath := body["path"]
		if !hasPath {
			vErrs = append(vErrs, validationItem{Loc: []any{"body", "path"}, Msg: "Field required", Type: "missing"})
		} else if _, ok := rawPath.(string); !ok {
			vErrs = append(vErrs, validationItem{Loc: []any{"body", "path"}, Msg: "Input should be a valid string", Type: "string_type"})
		}

		if len(vErrs) > 0 {
			replyValidation(w, vErrs)
			return
		}

		id := rawID.(string)
		path := rawPath.(string)

		inst := s.Mgr.Get(id)
		if inst == nil {
			replyDetail(w, http.StatusNotFound, "instance not found")
			return
		}

		target := pypath.Join(inst.Path, path)
		if !pypath.Contained(inst.Path, target) {
			replyDetail(w, http.StatusForbidden, "path outside instance")
			return
		}

		absPath := pypath.Resolve(target)
		st, err := os.Stat(absPath)
		if err != nil || st.IsDir() {
			replyDetail(w, http.StatusNotFound, "file not found")
			return
		}

		parentDir := filepath.Dir(absPath)
		if err := s.Run.ShellExecute(absPath, parentDir); err != nil {
			s.Log.Warn("launch failed for %s: %s", absPath, err)
			replyDetail(w, http.StatusInternalServerError, err.Error())
			return
		}

		s.Log.Info("Launched %s", absPath)
		replyJSON(w, http.StatusOK, map[string]any{"ok": true})
	})

	mux.HandleFunc("POST /wt", func(w http.ResponseWriter, r *http.Request) {
		body, errs := decodeJSONBody(r)
		if len(errs) > 0 {
			replyValidation(w, errs)
			return
		}

		var vErrs []validationItem
		rawID, hasID := body["id"]
		if !hasID {
			vErrs = append(vErrs, validationItem{Loc: []any{"body", "id"}, Msg: "Field required", Type: "missing"})
		} else if _, ok := rawID.(string); !ok {
			vErrs = append(vErrs, validationItem{Loc: []any{"body", "id"}, Msg: "Input should be a valid string", Type: "string_type"})
		}

		if len(vErrs) > 0 {
			replyValidation(w, vErrs)
			return
		}

		id := rawID.(string)
		inst := s.Mgr.Get(id)
		if inst == nil {
			replyDetail(w, http.StatusNotFound, "instance not found")
			return
		}

		st, err := os.Stat(inst.Path)
		if err != nil || !st.IsDir() {
			replyDetail(w, http.StatusNotFound, "folder not found")
			return
		}

		cwd := inst.Path
		safe := strings.ReplaceAll(cwd, "'", "''")
		candidates := []struct {
			argv  []string
			flags uint32
		}{
			{[]string{"wt.exe", "-d", cwd}, 0},
			{[]string{"powershell.exe", "-NoExit", "-Command", fmt.Sprintf("Set-Location -LiteralPath '%s'", safe)}, windows.CREATE_NEW_CONSOLE},
			{[]string{"cmd.exe", "/k", fmt.Sprintf(`cd /d "%s"`, cwd)}, windows.CREATE_NEW_CONSOLE},
		}

		var lastErr error
		for _, c := range candidates {
			err := s.Run.Start(c.argv, cwd, c.flags)
			if err == nil {
				s.Log.Info("Opened terminal via %s in %s", c.argv[0], cwd)
				replyJSON(w, http.StatusOK, map[string]any{"ok": true, "via": c.argv[0]})
				return
			}
			lastErr = err
		}

		s.Log.Warn("could not open terminal in %s: %s", cwd, lastErr)
		replyDetail(w, http.StatusInternalServerError, fmt.Sprintf("could not open terminal: %s", lastErr))
	})

	return mux
}
