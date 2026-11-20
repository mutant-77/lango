//go:build windows

package pty

import (
	"errors"
	"strings"
	"sync"
	"time"
	"unsafe"

	"lango/internal/agents"

	"github.com/shirou/gopsutil/v4/process"
	"golang.org/x/sys/windows"
)

type snapshotFunc func() (map[int]int, error)
type cmdlineFunc func(pid int) (string, error)

var (
	pollMu           sync.RWMutex
	snapshotProvider snapshotFunc = defaultSnapshot
	cmdlineProvider  cmdlineFunc  = defaultCmdline
	pollInterval                  = time.Second
)

func defaultSnapshot() (map[int]int, error) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(snap)

	var pe windows.ProcessEntry32
	pe.Size = uint32(unsafe.Sizeof(pe))
	ppidMap := make(map[int]int)
	for err = windows.Process32First(snap, &pe); err == nil; err = windows.Process32Next(snap, &pe) {
		ppidMap[int(pe.ProcessID)] = int(pe.ParentProcessID)
	}
	if err != nil && !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return nil, err
	}
	return ppidMap, nil
}

func defaultCmdline(pid int) (string, error) {
	p, err := process.NewProcess(int32(pid))
	if err != nil {
		return "", err
	}
	return p.Cmdline()
}

func getSnapshot() (map[int]int, error) {
	pollMu.RLock()
	fn := snapshotProvider
	pollMu.RUnlock()
	return fn()
}

func getCmdline(pid int) (string, error) {
	pollMu.RLock()
	fn := cmdlineProvider
	pollMu.RUnlock()
	return fn(pid)
}

func getPollInterval() time.Duration {
	pollMu.RLock()
	d := pollInterval
	pollMu.RUnlock()
	return d
}

func setTestProviders(snap snapshotFunc, cmd cmdlineFunc, interval time.Duration) func() {
	pollMu.Lock()
	oldSnap := snapshotProvider
	oldCmd := cmdlineProvider
	oldInterval := pollInterval
	if snap != nil {
		snapshotProvider = snap
	}
	if cmd != nil {
		cmdlineProvider = cmd
	}
	if interval > 0 {
		pollInterval = interval
	}
	pollMu.Unlock()

	return func() {
		pollMu.Lock()
		snapshotProvider = oldSnap
		cmdlineProvider = oldCmd
		pollInterval = oldInterval
		pollMu.Unlock()
	}
}

// collectDescendants builds the descendant list of rootPID from the PPID map.
func collectDescendants(ppidMap map[int]int, rootPID int) []int {
	childrenByParent := make(map[int][]int)
	for pid, ppid := range ppidMap {
		childrenByParent[ppid] = append(childrenByParent[ppid], pid)
	}

	var descendants []int
	queue := []int{rootPID}
	visited := map[int]bool{rootPID: true}

	for len(queue) > 0 {
		curr := queue[0]
		queue = queue[1:]
		for _, child := range childrenByParent[curr] {
			if !visited[child] {
				visited[child] = true
				descendants = append(descendants, child)
				queue = append(queue, child)
			}
		}
	}
	return descendants
}

// EnableAgentPolling registers lazy agent polling triggered on the first subscription.
func (b *Bridge) EnableAgentPolling(onAgent func(agent string)) {
	b.SetOnFirstSubscribe(func() {
		b.pollAgents(onAgent)
	})
}

func (b *Bridge) pollAgents(onAgent func(agent string)) {
	ticker := time.NewTicker(getPollInterval())
	defer ticker.Stop()

	for {
		b.mu.Lock()
		alive := b.alive && !b.terminated
		b.mu.Unlock()
		if !alive {
			return
		}

		select {
		case <-b.stopPoll:
			return
		case <-ticker.C:
		}

		b.mu.Lock()
		alive = b.alive && !b.terminated
		b.mu.Unlock()
		if !alive {
			return
		}

		if !b.IsAlive() || b.Pid() == 0 {
			continue
		}

		shellPID := b.Pid()

		ppidMap, err := getSnapshot()
		if err != nil {
			continue
		}

		if _, ok := ppidMap[shellPID]; !ok {
			return
		}

		descendants := collectDescendants(ppidMap, shellPID)

		var found string
		var passErr error

		for _, pid := range descendants {
			rawCmd, err := getCmdline(pid)
			if err != nil {
				passErr = err
				break
			}
			rawCmd = strings.TrimSpace(rawCmd)
			if rawCmd == "" {
				continue
			}
			tokens, err := windows.DecomposeCommandLine(rawCmd)
			if err != nil {
				passErr = err
				break
			}
			if len(tokens) == 0 {
				continue
			}
			if a := agents.Detect(tokens); a != "" {
				found = a
				break
			}
		}

		if passErr != nil {
			continue
		}

		if found != b.Agent() {
			b.SetAgent(found)
			var agentVal any = found
			if found == "" {
				agentVal = nil
			}
			b.Broadcast(Message{
				"type":  "agent",
				"agent": agentVal,
			})
			if onAgent != nil {
				onAgent(found)
			}
		}
	}
}
