package agents

import "strings"

// AgentInfo defines a known agent key and its command aliases.
type AgentInfo struct {
	Key   string
	Match []string
}

// Agents holds known agent definitions in priority order.
var Agents = []AgentInfo{
	{Key: "gemini", Match: []string{"gemini"}},
	{Key: "codex", Match: []string{"codex"}},
	{Key: "opencode", Match: []string{"opencode"}},
	{Key: "aider", Match: []string{"aider"}},
	{Key: "kilo", Match: []string{"kilo", "kilocode"}},
	{Key: "claude", Match: []string{"claude"}},
	{Key: "goose", Match: []string{"goose"}},
	{Key: "cursor", Match: []string{"cursor", "cursor-agent"}},
	{Key: "windsor", Match: []string{"windsor"}},
	{Key: "crush", Match: []string{"crush"}},
	{Key: "qwen", Match: []string{"qwen"}},
	{Key: "auggie", Match: []string{"auggie"}},
	{Key: "droid", Match: []string{"droid"}},
	{Key: "openhands", Match: []string{"openhands"}},
	{Key: "plandex", Match: []string{"plandex"}},
	{Key: "junie", Match: []string{"junie"}},
	{Key: "hermes", Match: []string{"hermes"}},
	{Key: "agy", Match: []string{"agy"}},
	{Key: "freebuff", Match: []string{"freebuff"}},
	{Key: "pi", Match: []string{"pi"}},
}

var suffixes = []string{".exe", ".cmd", ".ps1", ".js", ".py", ".sh"}

// Detect returns the matched agent key from command tokens, or "" if none matches.
func Detect(tokens []string) string {
	if len(tokens) == 0 {
		return ""
	}
	for _, token := range tokens {
		if strings.HasPrefix(token, "-") || strings.HasPrefix(token, "/") {
			continue
		}
		cmd := strings.ToLower(token)
		cmd = strings.ReplaceAll(cmd, "\\", "/")
		if idx := strings.LastIndex(cmd, "/"); idx != -1 {
			cmd = cmd[idx+1:]
		}
		for _, ext := range suffixes {
			if strings.HasSuffix(cmd, ext) {
				cmd = cmd[:len(cmd)-len(ext)]
			}
		}
		if idx := strings.Index(cmd, "@"); idx != -1 {
			cmd = cmd[:idx]
		}
		for _, agent := range Agents {
			for _, m := range agent.Match {
				if cmd == m {
					return agent.Key
				}
			}
		}
	}
	return ""
}
