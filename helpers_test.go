package main

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

func TestSlug(t *testing.T) {
	cases := map[string]string{
		"Fix the Login bug!":       "fix-the-login-bug",
		"  --weird__chars-- ":      "weird-chars",
		"":                         "task",
		"!!!":                      "task",
		strings.Repeat("abc ", 20): "abc-abc-abc-abc-abc-abc-abc-abc-abc-abc",
	}
	for in, want := range cases {
		if got := slug(in); got != want {
			t.Errorf("slug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBranchName(t *testing.T) {
	if got := branchName(&Card{ID: 7, Title: "Add dark mode"}); got != "maestrello/7-add-dark-mode" {
		t.Errorf("branchName = %q", got)
	}
}

func TestNewUUID(t *testing.T) {
	re := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		u := newUUID()
		if !re.MatchString(u) {
			t.Fatalf("not a v4 UUID: %s", u)
		}
		if seen[u] {
			t.Fatalf("duplicate UUID %s", u)
		}
		seen[u] = true
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

func TestFirstLineAndTail(t *testing.T) {
	if got := firstLine("one\ntwo", 10); got != "one" {
		t.Errorf("firstLine = %q", got)
	}
	if got := firstLine("abcdefgh", 3); got != "abc…" {
		t.Errorf("firstLine truncation = %q", got)
	}
	if got := tail("  hello world  ", 5); got != "…world" {
		t.Errorf("tail = %q", got)
	}
	if got := tail("short", 10); got != "short" {
		t.Errorf("tail short = %q", got)
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
	env := strings.Join(claudeEnv(), "\n")
	if strings.Contains(env, "CLAUDECODE=") || strings.Contains(env, "CLAUDE_CODE_ENTRYPOINT=") {
		t.Error("nested session vars leaked into child env")
	}
	if !strings.Contains(env, "MCP_TOOL_TIMEOUT=") {
		t.Error("MCP_TOOL_TIMEOUT not set")
	}
}

func TestCardInputValidation(t *testing.T) {
	str := func(s string) *string { return &s }
	base := func() *Card {
		return &Card{Title: "t", RootFolder: "/abs", PermissionMode: "auto", Effort: "medium"}
	}
	bad := []cardInput{
		{Title: str("  ")},
		{RootFolder: str("relative/path")},
		{PermissionMode: str("yolo")},
		{Effort: str("extreme")},
		{PushDefault: str("force-push")},
	}
	for i, in := range bad {
		if err := in.apply(base()); err == nil {
			t.Errorf("case %d: expected validation error", i)
		}
	}
	c := base()
	in := cardInput{Title: str(" New "), PermissionMode: str("acceptEdits"), PushDefault: str("pr")}
	if err := in.apply(c); err != nil {
		t.Fatal(err)
	}
	if c.Title != "New" || c.PermissionMode != "acceptEdits" || c.PushDefault != "pr" {
		t.Errorf("apply did not set fields: %+v", c)
	}
}
