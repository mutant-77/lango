//go:build windows

package pty

import (
	"os"
	"os/exec"
	"sort"
	"strings"
	"unicode/utf16"

	"golang.org/x/sys/windows/registry"
)

// ResolveShell returns the preferred shell on Windows.
func ResolveShell() []string {
	if p, err := exec.LookPath("pwsh.exe"); err == nil {
		return []string{p, "-NoLogo"}
	}
	if p, err := exec.LookPath("powershell.exe"); err == nil {
		return []string{p, "-NoLogo"}
	}
	if p, err := exec.LookPath("cmd.exe"); err == nil {
		return []string{p}
	}
	return []string{"cmd.exe"}
}

// BuildEnv builds an environment list with Windows registry PATH and terminal variables.
func BuildEnv() []string {
	envMap := make(map[string]string)
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "=") {
			continue
		}
		k, v, ok := strings.Cut(entry, "=")
		if !ok || strings.HasPrefix(k, "=") {
			continue
		}
		envMap[strings.ToUpper(k)] = v
	}

	var parts []string
	if val := registryPath(registry.CURRENT_USER, `Environment`, "PATH"); val != "" {
		parts = append(parts, val)
	}
	if val := registryPath(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Control\Session Manager\Environment`, "Path"); val != "" {
		parts = append(parts, val)
	}

	if len(parts) > 0 {
		envMap["PATH"] = strings.Join(parts, ";")
	} else if p := os.Getenv("PATH"); p != "" {
		envMap["PATH"] = p
	}

	envMap["TERM"] = "xterm-256color"
	envMap["COLORTERM"] = "truecolor"
	envMap["WT_SESSION"] = "lango-pty"
	envMap["PYTHONUTF8"] = "1"
	envMap["LANG"] = "en_US.UTF-8"

	keys := make([]string, 0, len(envMap))
	for k := range envMap {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		return strings.ToLower(keys[i]) < strings.ToLower(keys[j])
	})

	env := make([]string, 0, len(keys))
	for _, k := range keys {
		env = append(env, k+"="+envMap[k])
	}
	return env
}

// registryPath lee un valor de PATH del registro con las variables %VAR% expandidas.
func registryPath(root registry.Key, subkey, name string) string {
	k, err := registry.OpenKey(root, subkey, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	val, typ, err := k.GetStringValue(name)
	if err != nil || val == "" {
		return ""
	}
	if typ == registry.EXPAND_SZ {
		if exp, err := registry.ExpandString(val); err == nil {
			return exp
		}
	}
	return val
}

// EnvBlock converts an environment slice to a double-NUL terminated UTF-16 block.
func EnvBlock(env []string) []uint16 {
	if len(env) == 0 {
		return nil
	}
	var block []uint16
	for _, e := range env {
		block = append(block, utf16.Encode([]rune(e))...)
		block = append(block, 0)
	}
	return append(block, 0)
}
