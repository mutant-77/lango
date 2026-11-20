package gitinfo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/windows"

	"lango/internal/applog"
)

var (
	logger        = applog.New("gitinfo")
	dubiousLogged sync.Map
	hunkHeaderRe  = regexp.MustCompile(`^@@\s+-(\d+)(?:,\d+)?\s+\+(\d+)(?:,\d+)?\s+@@`)
)

const (
	maxFileSizeForLineCount = 256 * 1024      // 256 KB
	totalBudgetForLineCount = 4 * 1024 * 1024 // 4 MB
	maxDiffFileSize         = 2 * 1024 * 1024 // 2 MB
	maxDiffLines            = 5000
)

type File struct {
	Path   string `json:"path"`
	Letter string `json:"letter"`
	Add    int    `json:"add"`
	Del    int    `json:"del"`
}

type Status struct {
	Repo      bool   `json:"repo"`
	Branch    string `json:"branch"`
	Detached  bool   `json:"detached"`
	Unborn    bool   `json:"unborn"`
	Truncated bool   `json:"truncated"`
	Total     int    `json:"total"`
	Files     []File `json:"files"`
}

type Line struct {
	T byte
	S string
}

func (l Line) MarshalJSON() ([]byte, error) {
	return json.Marshal([2]string{string(l.T), l.S})
}

func (l *Line) UnmarshalJSON(b []byte) error {
	var arr [2]string
	if err := json.Unmarshal(b, &arr); err != nil {
		return err
	}
	if len(arr[0]) > 0 {
		l.T = arr[0][0]
	}
	l.S = arr[1]
	return nil
}

type Hunk struct {
	Header   string `json:"header"`
	OldStart int    `json:"old_start"`
	NewStart int    `json:"new_start"`
	Lines    []Line `json:"lines"`
}

type Diff struct {
	Letter   string `json:"letter"`
	Binary   bool   `json:"binary"`
	TooLarge bool   `json:"too_large"`
	Hunks    []Hunk `json:"hunks"`
}

func run(ctx context.Context, dir string, args ...string) ([]byte, []byte, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	cmdArgs := append([]string{
		"--no-optional-locks",
		"-c", "core.quotepath=false",
	}, args...)

	cmd := exec.CommandContext(cmdCtx, "git", cmdArgs...)
	cmd.Dir = dir
	cmd.SysProcAttr = &windows.SysProcAttr{
		CreationFlags: windows.CREATE_NO_WINDOW,
		HideWindow:    true,
	}
	cmd.WaitDelay = time.Second
	cmd.Env = append(os.Environ(),
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_LITERAL_PATHSPECS=1",
		"LC_ALL=C",
	)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}

func firstDubious(dir string) bool {
	_, seen := dubiousLogged.LoadOrStore(strings.ToLower(filepath.Clean(dir)), struct{}{})
	return !seen
}

func mapLetter(x, y byte) string {
	xy := string([]byte{x, y})
	switch xy {
	case "DD", "AU", "UD", "UA", "DU", "AA", "UU":
		return "M"
	}
	if x == 'U' || y == 'U' {
		return "M"
	}
	if x == 'T' || y == 'T' {
		return "M"
	}
	if x == '?' && y == '?' {
		return "A"
	}
	if x == 'A' || y == 'A' {
		return "A"
	}
	if x == 'D' || y == 'D' {
		return "D"
	}
	if x == 'M' || y == 'M' {
		return "M"
	}
	return "M"
}

func countLines(data []byte) int {
	if len(data) == 0 {
		return 0
	}
	n := bytes.Count(data, []byte{'\n'})
	if data[len(data)-1] != '\n' {
		n++
	}
	return n
}

func GetStatus(ctx context.Context, dir string) (Status, error) {
	stdout, stderr, err := run(ctx, dir, "rev-parse", "--show-prefix")
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return Status{Repo: false, Files: []File{}}, nil
		}
		errStr := string(stderr)
		if strings.Contains(errStr, "not a git repository") {
			return Status{Repo: false, Files: []File{}}, nil
		}
		if strings.Contains(errStr, "dubious ownership") {
			if firstDubious(dir) {
				logger.Warn("git dubious ownership detected in %s: %s", dir, strings.TrimSpace(errStr))
			}
			return Status{Repo: false, Files: []File{}}, nil
		}
		if _, statErr := os.Stat(dir); statErr != nil {
			return Status{Repo: false, Files: []File{}}, nil
		}
		return Status{Repo: false, Files: []File{}}, err
	}

	prefix := strings.TrimRight(string(stdout), "\r\n")

	statusOut, _, err := run(ctx, dir, "status", "--porcelain=v1", "-z", "--branch", "--untracked-files=all", "--ignore-submodules=all", "--", ".")
	if err != nil {
		return Status{Repo: false, Files: []File{}}, err
	}

	var status Status
	status.Repo = true
	status.Files = []File{}

	tokens := bytes.Split(statusOut, []byte{0})
	if len(tokens) == 0 || (len(tokens) == 1 && len(tokens[0]) == 0) {
		return status, nil
	}

	tok0 := string(tokens[0])
	startIdx := 0
	if strings.HasPrefix(tok0, "## ") {
		startIdx = 1
		header := strings.TrimPrefix(tok0, "## ")
		if strings.HasPrefix(header, "HEAD (no branch)") {
			status.Detached = true
			headOut, _, err := run(ctx, dir, "rev-parse", "--short", "HEAD")
			if err == nil {
				status.Branch = strings.TrimSpace(string(headOut))
			}
		} else if strings.HasPrefix(header, "No commits yet on ") {
			status.Unborn = true
			b := strings.TrimPrefix(header, "No commits yet on ")
			b = strings.SplitN(b, "...", 2)[0]
			status.Branch = strings.TrimSpace(b)
		} else if strings.HasPrefix(header, "Initial commit on ") {
			status.Unborn = true
			b := strings.TrimPrefix(header, "Initial commit on ")
			b = strings.SplitN(b, "...", 2)[0]
			status.Branch = strings.TrimSpace(b)
		} else {
			b := strings.SplitN(header, "...", 2)[0]
			status.Branch = strings.TrimSpace(b)
		}
	}

	var files []File
	untrackedMap := make(map[string]bool)

	for i := startIdx; i < len(tokens); i++ {
		tok := tokens[i]
		if len(tok) < 3 {
			continue
		}
		x := tok[0]
		y := tok[1]
		rawPath := strings.TrimSuffix(string(tok[3:]), "/") // repo anidado: "sub/"

		if x == 'R' || y == 'R' {
			destRel := rawPath
			if prefix != "" {
				if strings.HasPrefix(destRel, prefix) {
					destRel = strings.TrimPrefix(destRel, prefix)
					files = append(files, File{Path: destRel, Letter: "A"})
				}
			} else {
				files = append(files, File{Path: destRel, Letter: "A"})
			}

			i++
			if i < len(tokens) && len(tokens[i]) > 0 {
				origRel := string(tokens[i])
				if prefix != "" {
					if strings.HasPrefix(origRel, prefix) {
						origRel = strings.TrimPrefix(origRel, prefix)
						files = append(files, File{Path: origRel, Letter: "D"})
					}
				} else {
					files = append(files, File{Path: origRel, Letter: "D"})
				}
			}
			continue
		}

		if x == 'C' || y == 'C' {
			destRel := rawPath
			if prefix != "" {
				if strings.HasPrefix(destRel, prefix) {
					destRel = strings.TrimPrefix(destRel, prefix)
					files = append(files, File{Path: destRel, Letter: "A"})
				}
			} else {
				files = append(files, File{Path: destRel, Letter: "A"})
			}
			i++
			continue
		}

		relPath := rawPath
		if prefix != "" {
			if !strings.HasPrefix(relPath, prefix) {
				continue
			}
			relPath = strings.TrimPrefix(relPath, prefix)
		}

		letter := mapLetter(x, y)
		if x == '?' && y == '?' {
			untrackedMap[relPath] = true
		}
		files = append(files, File{Path: relPath, Letter: letter})
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].Path < files[j].Path
	})
	status.Total = len(files)
	if status.Total > 500 {
		status.Truncated = true
		files = files[:500]
	}

	type fileStat struct {
		add int
		del int
	}
	numstatMap := make(map[string]fileStat)

	if !status.Unborn {
		nsOut, _, err := run(ctx, dir, "diff", "--numstat", "-z", "--no-renames", "--ignore-submodules=all", "HEAD", "--", ".")
		if err == nil && len(nsOut) > 0 {
			nsTokens := bytes.Split(nsOut, []byte{0})
			for _, nst := range nsTokens {
				if len(nst) == 0 {
					continue
				}
				parts := strings.SplitN(string(nst), "\t", 3)
				if len(parts) < 3 {
					continue
				}
				p := parts[2]
				if prefix != "" {
					if !strings.HasPrefix(p, prefix) {
						continue
					}
					p = strings.TrimPrefix(p, prefix)
				}
				var add, del int
				if parts[0] != "-" {
					add, _ = strconv.Atoi(parts[0])
				}
				if parts[1] != "-" {
					del, _ = strconv.Atoi(parts[1])
				}
				numstatMap[p] = fileStat{add: add, del: del}
			}
		}
	}

	var totalBytesRead int64
	for i := range files {
		f := &files[i]
		if status.Unborn || untrackedMap[f.Path] {
			fullPath := filepath.Join(dir, filepath.FromSlash(f.Path))
			fi, err := os.Stat(fullPath)
			if err != nil || fi.IsDir() {
				f.Add = -1
				f.Del = 0
				continue
			}
			size := fi.Size()
			if size > maxFileSizeForLineCount || totalBytesRead+size > totalBudgetForLineCount {
				f.Add = -1
				f.Del = 0
				continue
			}
			data, err := os.ReadFile(fullPath)
			if err != nil {
				f.Add = -1
				f.Del = 0
				continue
			}
			totalBytesRead += int64(len(data))

			checkLen := len(data)
			if checkLen > 8192 {
				checkLen = 8192
			}
			if bytes.IndexByte(data[:checkLen], 0) != -1 {
				f.Add = 0
				f.Del = 0
				continue
			}
			f.Add = countLines(data)
			f.Del = 0
		} else {
			if st, ok := numstatMap[f.Path]; ok {
				f.Add = st.add
				f.Del = st.del
			} else {
				f.Add = 0
				f.Del = 0
			}
		}
	}

	status.Files = files
	return status, nil
}

func splitFileLines(data []byte) []string {
	if len(data) == 0 {
		return nil
	}
	s := string(data)
	if strings.HasSuffix(s, "\n") {
		s = s[:len(s)-1]
	}
	if strings.HasSuffix(s, "\r") {
		s = s[:len(s)-1]
	}
	raw := strings.Split(s, "\n")
	lines := make([]string, len(raw))
	for i, l := range raw {
		l = strings.TrimRight(l, "\r")
		lines[i] = strings.ToValidUTF8(l, "\uFFFD")
	}
	return lines
}

func GetDiff(ctx context.Context, dir, rel string) (Diff, error) {
	rel = filepath.ToSlash(filepath.Clean(rel))
	rel = strings.TrimPrefix(rel, "./")
	rel = strings.TrimPrefix(rel, "/")

	if rel == "" || rel == "." {
		return Diff{}, nil
	}

	statusOut, stderr, err := run(ctx, dir, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignore-submodules=all", "--", rel)
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) || strings.Contains(string(stderr), "not a git repository") {
			return Diff{}, nil
		}
		return Diff{}, err
	}

	if len(statusOut) == 0 {
		return Diff{}, nil
	}

	tokens := bytes.Split(statusOut, []byte{0})
	if len(tokens) == 0 || len(tokens[0]) < 3 {
		return Diff{}, nil
	}

	tok := tokens[0]
	x := tok[0]
	y := tok[1]
	isUntracked := (x == '?' && y == '?')

	var letter string
	if x == 'R' || y == 'R' {
		letter = "A"
	} else if x == 'C' || y == 'C' {
		letter = "A"
	} else {
		letter = mapLetter(x, y)
	}

	fullPath := filepath.Join(dir, filepath.FromSlash(rel))

	buildDiffFromFile := func() (Diff, error) {
		fi, err := os.Stat(fullPath)
		if err != nil {
			return Diff{Letter: letter}, nil
		}
		if fi.Size() > maxDiffFileSize {
			return Diff{Letter: letter, TooLarge: true}, nil
		}
		data, err := os.ReadFile(fullPath)
		if err != nil {
			return Diff{Letter: letter}, nil
		}
		checkLen := len(data)
		if checkLen > 8192 {
			checkLen = 8192
		}
		if bytes.IndexByte(data[:checkLen], 0) != -1 {
			return Diff{Letter: letter, Binary: true}, nil
		}
		lines := splitFileLines(data)
		if len(lines) > maxDiffLines {
			return Diff{Letter: letter, TooLarge: true}, nil
		}
		if len(lines) == 0 {
			return Diff{Letter: letter}, nil
		}
		hunkLines := make([]Line, len(lines))
		for i, l := range lines {
			hunkLines[i] = Line{T: '+', S: l}
		}
		header := fmt.Sprintf("@@ -0,0 +1,%d @@", len(lines))
		if len(lines) == 1 {
			header = "@@ -0,0 +1 @@"
		}
		return Diff{
			Letter: letter,
			Hunks: []Hunk{{
				Header:   header,
				OldStart: 0,
				NewStart: 1,
				Lines:    hunkLines,
			}},
		}, nil
	}

	if isUntracked {
		return buildDiffFromFile()
	}

	diffOut, diffErr, err := run(ctx, dir, "diff", "--no-color", "--no-ext-diff", "--no-renames", "--ignore-submodules=all", "-U3", "HEAD", "--", rel)
	if err != nil {
		errStr := string(diffErr)
		if strings.Contains(errStr, "bad revision 'HEAD'") ||
			strings.Contains(errStr, "unknown revision or path not in the working tree") ||
			strings.Contains(errStr, "Needed a single revision") {
			return buildDiffFromFile()
		}
		return Diff{Letter: letter}, err
	}

	if len(diffOut) > maxDiffFileSize {
		return Diff{Letter: letter, TooLarge: true}, nil
	}
	if fi, err := os.Stat(fullPath); err == nil && fi.Size() > maxDiffFileSize {
		return Diff{Letter: letter, TooLarge: true}, nil
	}

	diffStr := strings.TrimRight(string(diffOut), "\r\n")
	if strings.Contains(diffStr, "Binary files ") && strings.Contains(diffStr, " differ") {
		return Diff{Letter: letter, Binary: true}, nil
	}

	rawLines := strings.Split(diffStr, "\n")
	if len(rawLines) > maxDiffLines {
		return Diff{Letter: letter, TooLarge: true}, nil
	}

	var hunks []Hunk
	var currentHunk *Hunk
	var totalLines int

	for _, line := range rawLines {
		line = strings.TrimRight(line, "\r")
		if strings.HasPrefix(line, "@@") {
			if currentHunk != nil {
				hunks = append(hunks, *currentHunk)
				currentHunk = nil
			}
			match := hunkHeaderRe.FindStringSubmatch(line)
			if len(match) < 3 {
				continue
			}
			oldStart, _ := strconv.Atoi(match[1])
			newStart, _ := strconv.Atoi(match[2])
			currentHunk = &Hunk{
				Header:   strings.ToValidUTF8(line, "\uFFFD"),
				OldStart: oldStart,
				NewStart: newStart,
				Lines:    []Line{},
			}
			continue
		}
		if currentHunk == nil {
			continue
		}
		if strings.HasPrefix(line, `\`) {
			continue
		}
		if len(line) == 0 {
			continue
		}
		t := line[0]
		if t != ' ' && t != '+' && t != '-' {
			continue
		}
		s := strings.TrimRight(line[1:], "\r")
		s = strings.ToValidUTF8(s, "\uFFFD")
		currentHunk.Lines = append(currentHunk.Lines, Line{T: t, S: s})
		totalLines++
	}
	if currentHunk != nil {
		hunks = append(hunks, *currentHunk)
	}

	if totalLines > maxDiffLines {
		return Diff{Letter: letter, TooLarge: true}, nil
	}

	return Diff{
		Letter: letter,
		Hunks:  hunks,
	}, nil
}
