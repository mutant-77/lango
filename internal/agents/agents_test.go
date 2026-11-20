package agents

import "testing"

func testDetect(t *testing.T, tokens []string, expected string) {
	t.Helper()
	got := Detect(tokens)
	if got != expected {
		t.Fatalf("Detect(%v) = %q, want %q", tokens, got, expected)
	}
}

func TestDirectMatch(t *testing.T) {
	testDetect(t, []string{"claude"}, "claude")
	testDetect(t, []string{"codex"}, "codex")
	testDetect(t, []string{"opencode"}, "opencode")
	testDetect(t, []string{"aider"}, "aider")
	testDetect(t, []string{"gemini"}, "gemini")
	testDetect(t, []string{"freebuff"}, "freebuff")
	testDetect(t, []string{"pi"}, "pi")
}

func TestWithFlags(t *testing.T) {
	testDetect(t, []string{"claude", "--continue"}, "claude")
	testDetect(t, []string{"codex", "--model", "gpt-4"}, "codex")
}

func TestWrapperNpx(t *testing.T) {
	testDetect(t, []string{"npx", "claude"}, "claude")
	testDetect(t, []string{"npx", "opencode"}, "opencode")
}

func TestWrapperUvx(t *testing.T) {
	testDetect(t, []string{"uvx", "aider"}, "aider")
}

func TestWrapperPnpm(t *testing.T) {
	testDetect(t, []string{"pnpm", "claude"}, "claude")
	testDetect(t, []string{"pnpm", "dlx", "claude"}, "claude")
}

func TestWrapperChain(t *testing.T) {
	testDetect(t, []string{"npx", "-y", "claude"}, "claude")
}

func TestUnknownCommandReturnsNone(t *testing.T) {
	testDetect(t, []string{"python", "main.py"}, "")
	testDetect(t, []string{"node", "server.js"}, "")
	testDetect(t, []string{}, "")
}

func TestPathPrefixStripped(t *testing.T) {
	testDetect(t, []string{`C:\Users\dev\.local\bin\claude`}, "claude")
	// Unix paths skipped (start with / -> treated as flag)
	testDetect(t, []string{"/usr/local/bin/aider"}, "")
}

func TestExeSuffixStripped(t *testing.T) {
	testDetect(t, []string{"claude.exe"}, "claude")
	testDetect(t, []string{"aider.exe", "--chat"}, "aider")
}

func TestCursorAgentMatch(t *testing.T) {
	// "cursor-agent" matches the "cursor" agent key
	testDetect(t, []string{"cursor-agent"}, "cursor")
	testDetect(t, []string{"cursor"}, "cursor")
}

func TestSuffixOrderCumulative(t *testing.T) {
	// .exe stripped then .js stripped -> claude
	testDetect(t, []string{"claude.js.exe"}, "claude")
	// .js stripped leaving claude.exe, but .exe check already passed -> claude.exe (no match)
	testDetect(t, []string{"claude.exe.js"}, "")
	testDetect(t, []string{"aider.cmd"}, "aider")
	testDetect(t, []string{"gemini.ps1"}, "gemini")
	testDetect(t, []string{"codex.py"}, "codex")
	testDetect(t, []string{"opencode.sh"}, "opencode")
}

func TestVersionCutAtAtSign(t *testing.T) {
	testDetect(t, []string{"claude@latest"}, "claude")
	testDetect(t, []string{"aider@2.1.0"}, "aider")
}

func TestWrapperTokens(t *testing.T) {
	testDetect(t, []string{"npx.cmd", "claude"}, "claude")
}

func TestAllAgentsMatched(t *testing.T) {
	for _, a := range Agents {
		for _, m := range a.Match {
			testDetect(t, []string{m}, a.Key)
		}
	}
}
