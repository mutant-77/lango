package pty_test

import (
	"os"
	"strings"
	"testing"
	"unicode/utf16"

	"lango/internal/pty"

	"golang.org/x/sys/windows/registry"
)

func TestBuildEnv(t *testing.T) {
	env := pty.BuildEnv()
	if len(env) == 0 {
		t.Fatal("expected non-empty environment")
	}

	seenKeys := make(map[string]string)
	for _, entry := range env {
		if strings.HasPrefix(entry, "=") {
			t.Fatalf("found '=' prefixed entry: %q", entry)
		}
		k, v, ok := strings.Cut(entry, "=")
		if !ok {
			t.Fatalf("invalid entry format: %q", entry)
		}
		if k != strings.ToUpper(k) {
			t.Fatalf("key is not uppercase: %q", k)
		}
		seenKeys[k] = v
	}

	// Verify required terminal variables
	expectedVars := map[string]string{
		"TERM":        "xterm-256color",
		"COLORTERM":   "truecolor",
		"WT_SESSION":  "lango-pty",
		"PYTHONUTF8":  "1",
		"LANG":        "en_US.UTF-8",
	}
	for k, expectedVal := range expectedVars {
		if val, exists := seenKeys[k]; !exists || val != expectedVal {
			t.Errorf("expected %s=%q, got %q (exists=%v)", k, expectedVal, val, exists)
		}
	}

	// Verify PATH matches registry concatenation with %VAR% expanded
	var parts []string
	read := func(root registry.Key, subkey, name string) {
		k, err := registry.OpenKey(root, subkey, registry.QUERY_VALUE)
		if err != nil {
			return
		}
		defer k.Close()
		val, typ, err := k.GetStringValue(name)
		if err != nil || val == "" {
			return
		}
		if typ == registry.EXPAND_SZ {
			if exp, err := registry.ExpandString(val); err == nil {
				val = exp
			}
		}
		parts = append(parts, val)
	}
	read(registry.CURRENT_USER, `Environment`, "PATH")
	read(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Control\Session Manager\Environment`, "Path")

	var expectedPath string
	if len(parts) > 0 {
		expectedPath = strings.Join(parts, ";")
	} else {
		expectedPath = os.Getenv("PATH")
	}

	if seenKeys["PATH"] != expectedPath {
		t.Errorf("PATH mismatch:\ngot:  %s\nwant: %s", seenKeys["PATH"], expectedPath)
	}
	if strings.Contains(strings.ToLower(seenKeys["PATH"]), "%localappdata%") {
		t.Errorf("PATH contains unexpanded %%LOCALAPPDATA%%: %s", seenKeys["PATH"])
	}
}

func TestEnvBlock(t *testing.T) {
	env := []string{"FOO=bar", "BAZ=qux"}
	p := pty.EnvBlock(env)
	if len(p) == 0 || p[len(p)-1] != 0 || p[len(p)-2] != 0 {
		t.Fatal("expected double-NUL terminated block")
	}

	var entries []string
	var curr []uint16
	for i := 0; ; i++ {
		val := p[i]
		if val == 0 {
			if len(curr) == 0 {
				// Double-NUL reached
				break
			}
			entries = append(entries, string(utf16.Decode(curr)))
			curr = curr[:0]
		} else {
			curr = append(curr, val)
		}
	}

	if len(entries) != 2 || entries[0] != "FOO=bar" || entries[1] != "BAZ=qux" {
		t.Fatalf("unexpected entries decoded from env block: %v", entries)
	}
}
