package instances

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// PTY defines the terminal interface associated with an instance.
type PTY interface {
	IsAlive() bool
	Agent() string
	State() string
	IdleS() int
	Terminate()
}

// Watcher defines the filesystem watcher interface for an instance.
type Watcher interface {
	Stop()
}

// Instance represents a workspace instance.
// Agent solo se escribe con Manager.SetAgent (lo lee persist bajo el lock del manager).
type Instance struct {
	ID    string  `json:"id"`
	Path  string  `json:"path"`
	Agent *string `json:"agent"`

	mu       sync.Mutex
	terms    map[string]PTY
	watchers []Watcher
	openFile string
}

// Term returns the attached terminal for id, and whether it was found.
func (inst *Instance) Term(id string) (PTY, bool) {
	inst.mu.Lock()
	defer inst.mu.Unlock()
	p, ok := inst.terms[id]
	return p, ok
}

// SetTerm attaches a terminal for id.
func (inst *Instance) SetTerm(id string, p PTY) {
	inst.mu.Lock()
	if inst.terms == nil {
		inst.terms = make(map[string]PTY)
	}
	inst.terms[id] = p
	inst.mu.Unlock()
}

// RemoveTerm removes the terminal for id only if it matches p.
func (inst *Instance) RemoveTerm(id string, p PTY) {
	inst.mu.Lock()
	if current, ok := inst.terms[id]; ok && current == p {
		delete(inst.terms, id)
	}
	inst.mu.Unlock()
}

// Terms returns a copy of the attached terminals map.
func (inst *Instance) Terms() map[string]PTY {
	inst.mu.Lock()
	defer inst.mu.Unlock()
	res := make(map[string]PTY, len(inst.terms))
	for k, v := range inst.terms {
		res[k] = v
	}
	return res
}

// AddWatcher registers a watcher to stop on Close.
func (inst *Instance) AddWatcher(w Watcher) {
	inst.mu.Lock()
	inst.watchers = append(inst.watchers, w)
	inst.mu.Unlock()
}

// RemoveWatcher unregisters a watcher.
func (inst *Instance) RemoveWatcher(w Watcher) {
	inst.mu.Lock()
	defer inst.mu.Unlock()
	for i, x := range inst.watchers {
		if x == w {
			inst.watchers = append(inst.watchers[:i], inst.watchers[i+1:]...)
			return
		}
	}
}

// StopWatchers stops and clears all registered watchers on the instance.
func (inst *Instance) StopWatchers() {
	inst.mu.Lock()
	ws := append([]Watcher(nil), inst.watchers...)
	inst.watchers = nil
	inst.mu.Unlock()

	for _, w := range ws {
		if w != nil {
			func(watcher Watcher) {
				defer func() { _ = recover() }()
				watcher.Stop()
			}(w)
		}
	}
}

// SetOpenFile records the file last opened in the editor.
func (inst *Instance) SetOpenFile(p string) {
	inst.mu.Lock()
	inst.openFile = p
	inst.mu.Unlock()
}

// OpenFile returns the file last opened in the editor.
func (inst *Instance) OpenFile() string {
	inst.mu.Lock()
	defer inst.mu.Unlock()
	return inst.openFile
}

func baseName(p string) string {
	if len(p) >= 2 && p[1] == ':' && ((p[0] >= 'a' && p[0] <= 'z') || (p[0] >= 'A' && p[0] <= 'Z')) {
		p = p[2:]
	}
	if idx := strings.LastIndexAny(p, `\/`); idx != -1 {
		return p[idx+1:]
	}
	return p
}

// Name returns the basename truncated to 10 runes with an ellipsis if longer.
func (inst *Instance) Name() string {
	base := baseName(inst.Path)
	runes := []rune(base)
	if len(runes) > 10 {
		return string(runes[:10]) + "…"
	}
	return base
}

type persistInstance struct {
	ID    string  `json:"id"`
	Path  string  `json:"path"`
	Agent *string `json:"agent"`
}

type persistData struct {
	Instances []persistInstance `json:"instances"`
	Active    *string           `json:"active"`
}

// Manager manages workspace instances and their persistence.
type Manager struct {
	mu          sync.Mutex
	persistFile string
	instances   []*Instance
	activeID    *string
}

// NewManager creates a new instance manager backed by persistFile.
func NewManager(persistFile string) *Manager {
	return &Manager{
		persistFile: persistFile,
		instances:   []*Instance{},
	}
}

// Load reads and parses instances from the persistence file.
func (m *Manager) Load() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.instances = []*Instance{}
	m.activeID = nil

	content, err := os.ReadFile(m.persistFile)
	if err != nil {
		return
	}

	var raw any
	if err := json.Unmarshal(content, &raw); err != nil {
		return
	}

	root, ok := raw.(map[string]any)
	if !ok {
		return
	}

	var loadedInstances []*Instance
	var loadedActive *string

	func() {
		rawInsts, ok := root["instances"]
		if !ok {
			if rawActive, hasActive := root["active"]; hasActive && rawActive != nil {
				if actStr, okStr := rawActive.(string); okStr {
					loadedActive = &actStr
				}
			}
			return
		}

		list, okList := rawInsts.([]any)
		if !okList {
			return
		}

		for _, item := range list {
			itemMap, okMap := item.(map[string]any)
			if !okMap {
				return
			}

			rawID, hasID := itemMap["id"]
			rawPath, hasPath := itemMap["path"]
			if !hasID || !hasPath {
				return
			}

			idStr, okID := rawID.(string)
			pathStr, okPath := rawPath.(string)
			if !okID || !okPath {
				return
			}

			var agentPtr *string
			if rawAgent, hasAgent := itemMap["agent"]; hasAgent && rawAgent != nil {
				if agentStr, okAgent := rawAgent.(string); okAgent {
					agentPtr = &agentStr
				}
			}

			loadedInstances = append(loadedInstances, &Instance{
				ID:    idStr,
				Path:  pathStr,
				Agent: agentPtr,
			})
		}

		if rawActive, hasActive := root["active"]; hasActive && rawActive != nil {
			if actStr, okStr := rawActive.(string); okStr {
				loadedActive = &actStr
			}
		}
	}()

	var remaining []*Instance
	for _, inst := range loadedInstances {
		fi, err := os.Stat(inst.Path)
		if err == nil && fi.IsDir() {
			remaining = append(remaining, inst)
		}
	}
	m.instances = remaining

	valid := false
	if loadedActive != nil {
		for _, inst := range m.instances {
			if inst.ID == *loadedActive {
				valid = true
				break
			}
		}
	}

	if valid {
		m.activeID = loadedActive
	} else if len(m.instances) > 0 {
		firstID := m.instances[0].ID
		m.activeID = &firstID
	} else {
		m.activeID = nil
	}
}

func (m *Manager) persistLocked() error {
	list := make([]persistInstance, len(m.instances))
	for i, inst := range m.instances {
		list[i] = persistInstance{
			ID:    inst.ID,
			Path:  inst.Path,
			Agent: inst.Agent,
		}
	}

	data := persistData{
		Instances: list,
		Active:    m.activeID,
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(data); err != nil {
		return err
	}

	return os.WriteFile(m.persistFile, buf.Bytes(), 0644)
}

// Persist writes instances to the persistence file.
func (m *Manager) Persist() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.persistLocked()
}

// Create creates a new instance for path and marks it active.
func (m *Manager) Create(path string) (*Instance, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("Not a directory: %s", path)
	}
	fi, err := os.Stat(absPath)
	if err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("Not a directory: %s", absPath)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	inst := &Instance{
		ID:   uuid.NewString(),
		Path: absPath,
	}
	m.instances = append(m.instances, inst)
	m.activeID = &inst.ID

	if err := m.persistLocked(); err != nil {
		return nil, err
	}
	return inst, nil
}

// Close terminates and removes the instance identified by id.
func (m *Manager) Close(id string) error {
	target := m.Get(id)
	if target == nil {
		return nil
	}

	// Fuera del lock: Terminate puede tardar segundos (taskkill).
	terms := target.Terms()
	target.mu.Lock()
	ws := append([]Watcher(nil), target.watchers...)
	target.mu.Unlock()

	if len(terms) > 0 {
		var wg sync.WaitGroup
		for _, p := range terms {
			if p == nil {
				continue
			}
			wg.Add(1)
			go func(p PTY) {
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
		}
	}

	for _, w := range ws {
		if w != nil {
			func(watcher Watcher) {
				defer func() { _ = recover() }()
				watcher.Stop()
			}(w)
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	remaining := make([]*Instance, 0, len(m.instances))
	for _, inst := range m.instances {
		if inst.ID != id {
			remaining = append(remaining, inst)
		}
	}
	m.instances = remaining

	if m.activeID != nil && *m.activeID == id {
		if len(m.instances) > 0 {
			firstID := m.instances[0].ID
			m.activeID = &firstID
		} else {
			m.activeID = nil
		}
	}

	return m.persistLocked()
}

// SetAgent stores the detected agent ("" = none) and persists.
func (m *Manager) SetAgent(inst *Instance, agent string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if agent == "" {
		inst.Agent = nil
	} else {
		inst.Agent = &agent
	}
	return m.persistLocked()
}

// Activate marks the instance with id as active if found.
func (m *Manager) Activate(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	found := false
	for _, inst := range m.instances {
		if inst.ID == id {
			found = true
			break
		}
	}
	if !found {
		return nil
	}

	m.activeID = &id
	return m.persistLocked()
}

// Get returns the instance with id, or nil if not found.
func (m *Manager) Get(id string) *Instance {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, inst := range m.instances {
		if inst.ID == id {
			return inst
		}
	}
	return nil
}

// List returns a shallow copy of all instances.
func (m *Manager) List() []*Instance {
	m.mu.Lock()
	defer m.mu.Unlock()

	res := make([]*Instance, len(m.instances))
	copy(res, m.instances)
	return res
}

// ActiveID returns a pointer to the active instance ID, or nil.
func (m *Manager) ActiveID() *string {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.activeID == nil {
		return nil
	}
	id := *m.activeID
	return &id
}
