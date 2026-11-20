package watcher

import (
	"os"
	"path/filepath"
	"strings"
)

type Action uint32

const (
	ActionAdded          Action = 1
	ActionRemoved        Action = 2
	ActionModified       Action = 3
	ActionRenamedOldName Action = 4
	ActionRenamedNewName Action = 5
	ActionRemovedSelf    Action = 0xFFFE
)

type RawRecord struct {
	Action Action
	Name   string
}

type EventType int

const (
	EventCreated EventType = iota
	EventDeleted
	EventModified
	EventMoved
)

type Event struct {
	Type  EventType
	Path  string
	Dest  string
	IsDir bool
}

type walkEntry struct {
	path      string
	isSymlink bool
}

func isDirectory(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

func walkDirNTFS(dir string, visit func(root string, dirs []walkEntry, files []string)) {
	f, err := os.Open(dir)
	if err != nil {
		return
	}
	entries, err := f.ReadDir(-1)
	_ = f.Close()
	if err != nil {
		return
	}

	var dirs []walkEntry
	var files []string

	for _, entry := range entries {
		name := entry.Name()
		full := filepath.Join(dir, name)

		isDir := false
		isSym := false

		if entry.IsDir() {
			isDir = true
		} else if entry.Type()&os.ModeSymlink != 0 {
			isSym = true
			if fi, err := os.Stat(full); err == nil && fi.IsDir() {
				isDir = true
			}
		}

		if isDir {
			dirs = append(dirs, walkEntry{path: full, isSymlink: isSym})
		} else {
			files = append(files, full)
		}
	}

	visit(dir, dirs, files)

	for _, d := range dirs {
		if !d.isSymlink {
			walkDirNTFS(d.path, visit)
		}
	}
}

func generateSubCreatedEvents(srcDirPath string) []Event {
	var events []Event
	walkDirNTFS(srcDirPath, func(root string, dirs []walkEntry, files []string) {
		for _, d := range dirs {
			events = append(events, Event{
				Type:  EventCreated,
				Path:  d.path,
				IsDir: true,
			})
		}
		for _, f := range files {
			events = append(events, Event{
				Type:  EventCreated,
				Path:  f,
				IsDir: false,
			})
		}
	})
	return events
}

func generateSubMovedEvents(srcDirPath, destDirPath string) []Event {
	var events []Event
	walkDirNTFS(destDirPath, func(root string, dirs []walkEntry, files []string) {
		for _, d := range dirs {
			renamedPath := ""
			if srcDirPath != "" {
				renamedPath = strings.ReplaceAll(d.path, destDirPath, srcDirPath)
			}
			events = append(events, Event{
				Type:  EventMoved,
				Path:  renamedPath,
				Dest:  d.path,
				IsDir: true,
			})
		}
		for _, f := range files {
			renamedPath := ""
			if srcDirPath != "" {
				renamedPath = strings.ReplaceAll(f, destDirPath, srcDirPath)
			}
			events = append(events, Event{
				Type:  EventMoved,
				Path:  renamedPath,
				Dest:  f,
				IsDir: false,
			})
		}
	})
	return events
}

func Translate(root string, records []RawRecord) []Event {
	var events []Event
	lastRenamedSrc := ""

	emit := func(ev Event) {
		if len(events) > 0 {
			prev := events[len(events)-1]
			if prev.Type == ev.Type && prev.Path == ev.Path && prev.Dest == ev.Dest && prev.IsDir == ev.IsDir {
				return
			}
		}
		events = append(events, ev)
	}

	for _, record := range records {
		src := filepath.Join(root, record.Name)

		switch record.Action {
		case ActionRenamedOldName:
			lastRenamedSrc = src

		case ActionRenamedNewName:
			dest := src
			src = lastRenamedSrc
			if isDirectory(dest) {
				emit(Event{Type: EventMoved, Path: src, Dest: dest, IsDir: true})
				for _, sub := range generateSubMovedEvents(src, dest) {
					emit(sub)
				}
			} else {
				emit(Event{Type: EventMoved, Path: src, Dest: dest, IsDir: false})
			}

		case ActionModified:
			if isDirectory(src) {
				emit(Event{Type: EventModified, Path: src, IsDir: true})
			} else {
				emit(Event{Type: EventModified, Path: src, IsDir: false})
			}

		case ActionAdded:
			if isDirectory(src) {
				emit(Event{Type: EventCreated, Path: src, IsDir: true})
				for _, sub := range generateSubCreatedEvents(src) {
					emit(sub)
				}
			} else {
				emit(Event{Type: EventCreated, Path: src, IsDir: false})
			}

		case ActionRemoved:
			emit(Event{Type: EventDeleted, Path: src, IsDir: false})

		case ActionRemovedSelf:
			emit(Event{Type: EventDeleted, Path: root, IsDir: true})
		}
	}

	return events
}
