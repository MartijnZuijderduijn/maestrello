package harness

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"maestrello/internal/model"
	"maestrello/internal/testenv"
)

func testSpec() SessionSpec {
	return SessionSpec{
		Card:   &model.Card{Model: "sonnet", Effort: "high", PermissionMode: "auto", SessionID: "sess-1"},
		Prompt: "do the thing", Instructions: "be good",
		MCPURL: "http://127.0.0.1:1/mcp/7", MCPConfigPath: "", Cwd: "/work", Root: "/root",
	}
}

func sessionArgs(t *testing.T, spec SessionSpec) ([]string, []string, string) {
	t.Helper()
	spec.MCPConfigPath = filepath.Join(testenv.Dir(t), "7.mcp.json")
	cmd, err := claudeHarness{bin: "/bin/claude"}.SessionCommand(spec)
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Path != "/bin/claude" || cmd.Dir != spec.Cwd {
		t.Errorf("path %q dir %q", cmd.Path, cmd.Dir)
	}
	in, _ := io.ReadAll(cmd.Stdin)
	return cmd.Args[1:], cmd.Env, string(in)
}

// flagValue returns the argument after flag, and whether the flag is there.
func flagValue(args []string, flag string) (string, bool) {
	i := slices.Index(args, flag)
	if i < 0 || i+1 >= len(args) {
		return "", false
	}
	return args[i+1], true
}

func TestClaudeSessionCommand(t *testing.T) {
	t.Setenv("CLAUDECODE", "1")
	t.Setenv("CLAUDE_CODE_ENTRYPOINT", "cli")
	spec := testSpec()
	args, env, stdin := sessionArgs(t, spec)

	want := []string{"-p", "--output-format", "stream-json", "--verbose", "--model", "sonnet", "--permission-mode", "auto",
		"--effort", "high", "--session-id", "sess-1"}
	if !slices.Equal(args[:len(want)], want) {
		t.Errorf("leading args = %v", args[:len(want)])
	}
	cfg := filepath.Join(filepath.Dir(args[slices.Index(args, "--mcp-config")+1]), "7.mcp.json")
	tail := []string{"--mcp-config", cfg,
		"--permission-prompt-tool", "mcp__maestrello__permission",
		"--allowedTools", "mcp__maestrello__report_done,mcp__maestrello__report_failed,mcp__maestrello__ask_user,mcp__maestrello__add_repo",
		"--disallowedTools", "AskUserQuestion", "--add-dir", "/root", "--append-system-prompt", "be good"}
	if !slices.Equal(args[len(want):], tail) {
		t.Errorf("trailing args = %v", args[len(want):])
	}
	if slices.Contains(args, "--resume") || slices.Contains(args, "--strict-mcp-config") {
		t.Errorf("fresh, non-strict run has --resume or --strict-mcp-config: %v", args)
	}
	if stdin != "do the thing" {
		t.Errorf("stdin = %q", stdin)
	}
	joined := strings.Join(env, "\n")
	if strings.Contains(joined, "CLAUDECODE=") || strings.Contains(joined, "CLAUDE_CODE_ENTRYPOINT=") {
		t.Error("nested session vars leaked into env")
	}
	if !slices.Contains(env, "MCP_TOOL_TIMEOUT=86400000") {
		t.Error("MCP_TOOL_TIMEOUT not set")
	}
}

func TestClaudeSessionCommandVariants(t *testing.T) {
	spec := testSpec()
	spec.Resume, spec.Strict = true, true
	spec.Card.Effort = ""
	args, _, _ := sessionArgs(t, spec)
	if v, ok := flagValue(args, "--resume"); !ok || v != "sess-1" {
		t.Errorf("--resume = %q, %v", v, ok)
	}
	if slices.Contains(args, "--session-id") {
		t.Error("resume still has --session-id")
	}
	if slices.Contains(args, "--effort") {
		t.Error("empty effort still passes --effort")
	}
	if !slices.Contains(args, "--strict-mcp-config") {
		t.Error("strict run lacks --strict-mcp-config")
	}
}

func TestClaudeSessionWritesMCPConfig(t *testing.T) {
	spec := testSpec()
	spec.MCPConfigPath = filepath.Join(testenv.Dir(t), "7.mcp.json")
	if _, err := (claudeHarness{bin: "claude"}).SessionCommand(spec); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(spec.MCPConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	if got != `{"mcpServers":{"maestrello":{"type":"http","url":"http://127.0.0.1:1/mcp/7"}}}` {
		t.Errorf("mcp config = %s", got)
	}
}

func TestClaudeRefineCommand(t *testing.T) {
	h := claudeHarness{bin: "/bin/claude"}
	for _, tools := range []string{"Read,Grep,Glob", ""} {
		cmd, err := h.RefineCommand(context.Background(), RefineSpec{
			Model: "sonnet", Effort: "low", Prompt: "refine me", Cwd: "/root", Tools: tools, MaxTurns: 4, Schema: `{"type":"object"}`})
		if err != nil {
			t.Fatal(err)
		}
		args := cmd.Args[1:]
		want := []string{"-p", "--output-format", "json", "--model", "sonnet", "--effort", "low",
			"--permission-mode", "plan", "--tools", tools, "--strict-mcp-config", "--json-schema", `{"type":"object"}`, "--max-turns", "4"}
		if !slices.Equal(args, want) {
			t.Errorf("tools %q: args = %q", tools, args)
		}
		in, _ := io.ReadAll(cmd.Stdin)
		if string(in) != "refine me" || cmd.Dir != "/root" {
			t.Errorf("stdin %q dir %q", in, cmd.Dir)
		}
		if !slices.Contains(cmd.Env, "MCP_TOOL_TIMEOUT=86400000") {
			t.Error("refine env lacks MCP_TOOL_TIMEOUT")
		}
	}
}

func TestClaudeParseRefineResult(t *testing.T) {
	h := claudeHarness{}
	r, err := h.ParseRefineResult([]byte(`{"is_error":false,"result":"text","structured_output":{"refined_prompt":"p"},"total_cost_usd":0.5}`))
	if err != nil || string(r.Structured) != `{"refined_prompt":"p"}` || r.CostUSD != 0.5 || r.IsError || r.Text != "text" {
		t.Errorf("structured: %+v, %v", r, err)
	}
	r, _ = h.ParseRefineResult([]byte(`{"result":"{\"refined_prompt\":\"q\"}"}`))
	if string(r.Structured) != `{"refined_prompt":"q"}` {
		t.Errorf("fallback to result text: %+v", r)
	}
	r, _ = h.ParseRefineResult([]byte(`{"is_error":true,"result":"boom"}`))
	if !r.IsError || r.Text != "boom" {
		t.Errorf("error result: %+v", r)
	}
	if _, err := h.ParseRefineResult([]byte("not json")); err == nil {
		t.Error("non-JSON output parsed")
	}
}

func TestClaudeParseEvent(t *testing.T) {
	h := claudeHarness{}
	parse := func(line string) HarnessEvent {
		t.Helper()
		ev, err := h.ParseEvent([]byte(line))
		if err != nil {
			t.Fatalf("%s: %v", line, err)
		}
		return ev
	}

	ev := parse(`{"type":"assistant","message":{"id":"msg_1","content":[{"type":"text","text":"  hello\nworld"}]}}`)
	if !ev.Assistant || ev.Kind != "assistant" || ev.TurnID != "msg_1" || ev.Activity != "hello" || ev.Result || ev.RateLimited || ev.UsageInfo {
		t.Errorf("assistant text: %+v", ev)
	}
	ev = parse(`{"type":"assistant","message":{"id":"msg_2","content":[{"type":"text","text":"thinking"},{"type":"tool_use","name":"Bash","input":{"command":"ls -l\nmore"}}]}}`)
	if ev.TurnID != "msg_2" || ev.Activity != "Bash: ls -l" {
		t.Errorf("assistant tool_use: %+v", ev)
	}
	ev = parse(`{"type":"assistant","message":{"content":[{"type":"text","text":"no id"}]}}`)
	if !ev.Assistant || ev.TurnID != "" || ev.Activity != "no id" {
		t.Errorf("assistant without id: %+v", ev)
	}

	ev = parse(`{"type":"result","subtype":"success","is_error":false,"result":"ok","total_cost_usd":0.0123}`)
	if !ev.Result || ev.CostUSD != 0.0123 || ev.IsError || ev.RateLimited || ev.Assistant {
		t.Errorf("result success: %+v", ev)
	}
	ev = parse(`{"type":"result","is_error":true,"result":"Claude AI usage limit reached|1731340800","total_cost_usd":0.1}`)
	if !ev.IsError || !ev.RateLimited || ev.ResultError != "Claude AI usage limit reached|1731340800" || ev.RateLimitMsg != ev.ResultError || ev.CostUSD != 0.1 {
		t.Errorf("result usage limit: %+v", ev)
	}
	for _, status := range []string{`429`, `"429"`} {
		ev = parse(`{"type":"result","is_error":true,"result":"API failed","api_error_status":` + status + `}`)
		if !ev.IsError || !ev.RateLimited || ev.RateLimitMsg != "API failed" {
			t.Errorf("result api_error_status %s: %+v", status, ev)
		}
	}
	ev = parse(`{"type":"result","is_error":true,"result":"Failed to authenticate","api_error_status":401}`)
	if !ev.IsError || ev.RateLimited || ev.ResultError != "Failed to authenticate" {
		t.Errorf("result error without rate limit: %+v", ev)
	}

	line := `{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","rateLimitType":"five_hour"}}`
	ev = parse(line)
	if !ev.RateLimited || ev.RateLimitMsg != line || !ev.UsageInfo || ev.Kind != "rate_limit_event" {
		t.Errorf("rate_limit rejected: %+v", ev)
	}
	ev = parse(`{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","overageStatus":"rejected","utilization":0.42}}`)
	if ev.RateLimited || !ev.UsageInfo {
		t.Errorf("allowed with rejected overage: %+v", ev)
	}
	ev = parse(`{"type":"rate_limit_event","rate_limit_info":{"status":"allowed_warning","utilization":0.81}}`)
	if ev.RateLimited || !ev.UsageInfo {
		t.Errorf("allowed_warning: %+v", ev)
	}

	ev = parse(`{"type":"system","subtype":"init","session_id":"s"}`)
	if ev.Kind != "system" || ev.Assistant || ev.Result || ev.RateLimited || ev.UsageInfo {
		t.Errorf("system init: %+v", ev)
	}

	if _, err := h.ParseEvent([]byte("not json at all")); err == nil {
		t.Error("non-JSON line parsed")
	}
}

func TestHarnessRegistry(t *testing.T) {
	for _, name := range []string{"", "claude"} {
		h, err := For(name)
		if err != nil || h.Name() != "claude" {
			t.Errorf("harnessFor(%q) = %v, %v", name, h, err)
		}
	}
	if _, err := For("codex"); err == nil {
		t.Error("codex accepted")
	}
	c := claudeHarness{}.Caps()
	if !c.Resume || !c.PermissionCallback || !c.StructuredOutput || !c.PerTurnEvents {
		t.Errorf("claude caps = %+v", c)
	}
}

func TestClaudeValidation(t *testing.T) {
	h := claudeHarness{}
	for _, e := range []string{"", "low", "medium", "high", "xhigh", "max"} {
		if h.ValidateEffort(e) != nil {
			t.Errorf("effort %q rejected", e)
		}
	}
	if h.ValidateEffort("extreme") == nil {
		t.Error("effort extreme accepted")
	}
	for _, m := range []string{"auto", "acceptEdits", "manual", "dontAsk", "plan", "bypassPermissions"} {
		if h.ValidatePermissionMode(m) != nil {
			t.Errorf("mode %q rejected", m)
		}
	}
	for _, m := range []string{"", "yolo"} {
		if h.ValidatePermissionMode(m) == nil {
			t.Errorf("mode %q accepted", m)
		}
	}
}

func TestToolActivity(t *testing.T) {
	cases := []struct {
		name, input, want string
	}{
		{"Bash", `{"command":"go test ./...\nmore"}`, "Bash: go test ./..."},
		{"Edit", `{"file_path":"/x/main.go","old_string":"a"}`, "Edit: /x/main.go"},
		{"TodoWrite", `{"todos":[]}`, "TodoWrite"},
		{"Weird", `not json`, "Weird"},
	}
	for _, c := range cases {
		if got := toolActivity(c.name, json.RawMessage(c.input)); got != c.want {
			t.Errorf("toolActivity(%s) = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestRateLimitDetection(t *testing.T) {
	for _, s := range []string{"Claude AI usage limit reached|1731340800", "API Error: 429 Too Many Requests", "rate limit exceeded"} {
		if !rateLimitRe.MatchString(s) {
			t.Errorf("not detected as rate limit: %q", s)
		}
	}
	for _, s := range []string{"Failed to authenticate", "tool error: file not found"} {
		if rateLimitRe.MatchString(s) {
			t.Errorf("wrongly detected as rate limit: %q", s)
		}
	}
}

func TestClaudeEnvDropsNestedSessionVars(t *testing.T) {
	t.Setenv("CLAUDECODE", "1")
	t.Setenv("CLAUDE_CODE_ENTRYPOINT", "cli")
	env := strings.Join(ClaudeEnv(), "\n")
	if strings.Contains(env, "CLAUDECODE=") || strings.Contains(env, "CLAUDE_CODE_ENTRYPOINT=") {
		t.Error("nested session vars leaked into child env")
	}
	if !strings.Contains(env, "MCP_TOOL_TIMEOUT=") {
		t.Error("MCP_TOOL_TIMEOUT not set")
	}
}

func TestRateLimitRejected(t *testing.T) {
	cases := map[string]bool{
		`{"type":"rate_limit_event","rate_limit_info":{"status":"rejected"}}`:                           true,
		`{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","overageStatus":"rejected"}}`: false,
		`{"type":"rate_limit_event","rate_limit_info":{"status":"allowed_warning"}}`:                    false,
		`not json "rejected"`: false,
	}
	for line, want := range cases {
		if got := rateLimitRejected([]byte(line)); got != want {
			t.Errorf("rateLimitRejected(%s) = %v", line, got)
		}
	}
}
