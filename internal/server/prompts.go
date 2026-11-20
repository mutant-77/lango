package server

import (
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"lango/internal/instances"
)

// PromptsID es el id reservado de la raíz de prompts (fuera de cualquier proyecto).
const PromptsID = "prompts"

const maxPromptBytes = 256 << 10

// rootFor resuelve la carpeta raíz de un id de sesión o de la raíz de prompts.
func (s *Server) rootFor(id string) (root string, inst *instances.Instance, ok bool) {
	if id == PromptsID && s.PromptsDir != "" {
		_ = os.MkdirAll(s.PromptsDir, 0755)
		return s.PromptsDir, nil, true
	}
	inst = s.Mgr.Get(id)
	if inst == nil {
		return "", nil, false
	}
	return inst.Path, inst, true
}

type promptItem struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

func (s *Server) handlePrompts(w http.ResponseWriter, r *http.Request) {
	root, _, ok := s.rootFor(PromptsID)
	if !ok {
		replyDetail(w, http.StatusNotFound, "instance not found")
		return
	}

	prompts := make([]promptItem, 0)
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.EqualFold(filepath.Ext(p), ".md") {
			return nil
		}
		info, err := d.Info()
		if err != nil || info.Size() > maxPromptBytes {
			return nil
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		content, _ := ReadText(raw)
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return nil
		}
		prompts = append(prompts, promptItem{Path: filepath.ToSlash(rel), Content: content})
		return nil
	})

	sort.Slice(prompts, func(i, j int) bool {
		return strings.ToLower(prompts[i].Path) < strings.ToLower(prompts[j].Path)
	})
	replyJSON(w, http.StatusOK, map[string]any{"prompts": prompts})
}
