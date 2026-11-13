package harness

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"maestrello/internal/model"
	"maestrello/internal/testenv"
)

func ompSpec(kind string) SessionSpec {
	return SessionSpec{
		Card:   &model.Card{Model: "opus", Effort: "high", PermissionMode: "auto", SessionID: "abc123", Kind: kind},
		Prompt: "do the thing", Instructions: "call mcp__maestrello__report_done",
		MCPURL: "http://127.0.0.1:1/mcp/7", Cwd: "/work", Root: "/root",
	}
}

// ompSession builds a session command; it returns args, env, stdin, the agent dir and the instructions.
func ompSession(t *testing.T, spec SessionSpec) (args, env []string, stdin, home, instr string) {
	t.Helper()
	t.Setenv("PI_CODING_AGENT_DIR", filepath.Join(testenv.Dir(t), "user-agent"))
	spec.MCPConfigPath = filepath.Join(testenv.Dir(t), "7.mcp.json")
	cmd, err := newOmpHarness("/bin/omp").SessionCommand(spec)
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Path != "/bin/omp" || cmd.Dir != spec.Cwd {
		t.Errorf("path %q dir %q", cmd.Path, cmd.Dir)
	}
	in, _ := io.ReadAll(cmd.Stdin)
	home = spec.MCPConfigPath + ".omp"
	if !slices.Contains(cmd.Env, "PI_CODING_AGENT_DIR="+home) || !slices.Contains(cmd.Env, "OMP_MCP_TIMEOUT_MS=86400000") {
		t.Errorf("env lacks the agent dir / MCP timeout")
	}
	if _, err := os.Stat(spec.MCPConfigPath); err != nil {
		t.Errorf("no file at MCPConfigPath: %v", err)
	}
	i, _ := os.ReadFile(spec.MCPConfigPath + ".instructions.md")
	return cmd.Args[1:], cmd.Env, string(in), home, string(i)
}

func ompToolList(args []string) []string {
	v, _ := flagValue(args, "--tools")
	return strings.Split(v, ",")
}

func TestOmpSessionCommand(t *testing.T) {
	args, _, stdin, home, instr := ompSession(t, ompSpec(""))
	if !slices.Equal(args[:2], []string{"-p", "--mode"}) || args[2] != "json" {
		t.Errorf("args = %v", args)
	}
	if v, _ := flagValue(args, "--model"); v != "opus" {
		t.Errorf("model = %q", v)
	}
	if v, _ := flagValue(args, "--thinking"); v != "high" {
		t.Errorf("thinking = %q", v)
	}
	if stdin != "do the thing" {
		t.Errorf("stdin = %q", stdin)
	}
	if !strings.Contains(instr, "call mcp__maestrello_report_done") || strings.Contains(instr, "mcp__maestrello__") {
		t.Errorf("instructions = %q", instr)
	}
	mcp, err := os.ReadFile(filepath.Join(home, "mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"maestrello"`, `"type":"http"`, `"url":"http://127.0.0.1:1/mcp/7"`, `"timeout":86400000`} {
		if !strings.Contains(string(mcp), want) {
			t.Errorf("mcp.json lacks %s: %s", want, mcp)
		}
	}
	overlay, _ := flagValue(args, "--config")
	if b, _ := os.ReadFile(overlay); !strings.Contains(string(b), "startupTimeoutMs") || strings.Contains(string(b), "enableProjectConfig") {
		t.Errorf("overlay = %s", b)
	}
	tools := ompToolList(args)
	for _, n := range []string{"bash", "edit", "write", "read", "mcp__maestrello_report_done", "mcp__maestrello_add_repo"} {
		if !slices.Contains(tools, n) {
			t.Errorf("code card lacks %s: %v", n, tools)
		}
	}
	if slices.Contains(tools, "mcp__maestrello_report_plan") {
		t.Errorf("code card has the plan tool: %v", tools)
	}
	if got := strings.Join(args, " "); !strings.Contains(got, "--add-dir /root") {
		t.Errorf("no --add-dir for the root: %v", args)
	}
}

func TestOmpSessionKindsAndStrict(t *testing.T) {
	writes := []string{"bash", "edit", "write"}
	for _, tc := range []struct {
		kind    string
		on, off []string
	}{
		{model.KindResearch, []string{"read", "grep", "mcp__maestrello_report_done"}, []string{"mcp__maestrello_report_plan", "mcp__maestrello_add_repo"}},
		{model.KindPlan, []string{"read", "mcp__maestrello_report_plan", "mcp__maestrello_ask_user"}, []string{"mcp__maestrello_report_done", "mcp__maestrello_add_repo"}},
		{model.KindChat, []string{"mcp__maestrello_ask_user", "mcp__maestrello_report_failed"}, []string{"read", "grep", "mcp__maestrello_report_done"}},
	} {
		args, _, _, _, _ := ompSession(t, ompSpec(tc.kind))
		tools := ompToolList(args)
		for _, n := range tc.on {
			if !slices.Contains(tools, n) {
				t.Errorf("%s: %s off", tc.kind, n)
			}
		}
		for _, n := range append(tc.off, writes...) {
			if slices.Contains(tools, n) {
				t.Errorf("%s: %s on", tc.kind, n)
			}
		}
	}
	spec := ompSpec("")
	spec.Card.PermissionMode = "plan"
	args, _, _, _, _ := ompSession(t, spec)
	tools := ompToolList(args)
	for _, n := range writes {
		if slices.Contains(tools, n) {
			t.Errorf("plan mode has %s", n)
		}
	}
	if !slices.Contains(tools, "read") {
		t.Error("plan mode lost read")
	}
	spec = ompSpec("")
	spec.Strict = true
	args, _, _, _, _ = ompSession(t, spec)
	overlay, _ := flagValue(args, "--config")
	if b, _ := os.ReadFile(overlay); !strings.Contains(string(b), "enableProjectConfig: false") {
		t.Errorf("strict overlay = %s", b)
	}
}

func TestOmpHomeLinksUserDir(t *testing.T) {
	user := testenv.Dir(t)
	os.WriteFile(filepath.Join(user, "agent.db"), []byte("secret"), 0o600)
	os.WriteFile(filepath.Join(user, "mcp.json"), []byte(`{"mcpServers":{"mine":{}}}`), 0o600)
	t.Setenv("PI_CODING_AGENT_DIR", user)
	home := filepath.Join(testenv.Dir(t), "home")
	if err := ompHome(home, "http://x/mcp/1"); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(home, "agent.db")); err != nil || string(b) != "secret" {
		t.Errorf("credentials not linked: %q %v", b, err)
	}
	if b, _ := os.ReadFile(filepath.Join(home, "mcp.json")); strings.Contains(string(b), "mine") || !strings.Contains(string(b), "maestrello") {
		t.Errorf("mcp.json = %s", b)
	}
	if b, _ := os.ReadFile(filepath.Join(user, "mcp.json")); !strings.Contains(string(b), "mine") {
		t.Errorf("the user's mcp.json was changed: %s", b)
	}
}

func TestOmpResume(t *testing.T) {
	h := newOmpHarness("")
	if !h.Caps().Resume {
		t.Error("Resume must be true: the session header carries an id")
	}
	spec := ompSpec("")
	args, _, _, _, _ := ompSession(t, spec)
	if slices.Contains(args, "--resume") {
		t.Errorf("fresh run resumes: %v", args)
	}
	spec.Resume = true
	args, _, _, _, _ = ompSession(t, spec)
	if v, _ := flagValue(args, "--resume"); v != "abc123" {
		t.Errorf("resume = %q in %v", v, args)
	}
	if !h.ResumableSession("0199abcd") || h.ResumableSession("") || h.ResumableSession("--x") {
		t.Error("ResumableSession")
	}
}

func TestOmpRefineCommand(t *testing.T) {
	h := newOmpHarness("/bin/omp")
	ctx, cancel := context.WithCancel(context.Background())
	cmd, err := h.RefineCommand(ctx, RefineSpec{Model: "opus", Prompt: "refine me", Cwd: "/root", Tools: "Read,Grep,Glob,Bash", Schema: `{"type":"object"}`, ExtraDirs: []string{"/ctx"}})
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Path != "/bin/omp" || cmd.Dir != "/root" {
		t.Errorf("path %q dir %q", cmd.Path, cmd.Dir)
	}
	if got := ompToolList(cmd.Args[1:]); !slices.Equal(got, []string{"read", "grep", "glob"}) {
		t.Errorf("tools = %v", got)
	}
	if slices.Contains(cmd.Args, "json") || !slices.Contains(cmd.Args, "--no-session") || !strings.Contains(strings.Join(cmd.Args, " "), "--add-dir /ctx") {
		t.Errorf("args = %v", cmd.Args)
	}
	in, _ := io.ReadAll(cmd.Stdin)
	if !strings.HasPrefix(string(in), "refine me") || !strings.Contains(string(in), `{"type":"object"}`) {
		t.Errorf("stdin = %q", in)
	}
	cancel()
	if cmd.Cancel == nil {
		t.Error("refine command is not bound to ctx")
	}
	cmd, _ = h.RefineCommand(context.Background(), RefineSpec{})
	if !slices.Contains(cmd.Args, "--no-tools") {
		t.Errorf("no-tools refine = %v", cmd.Args)
	}
}

func TestOmpParseEvent(t *testing.T) {
	h := newOmpHarness("")
	ev, err := h.ParseEvent([]byte(`{"type":"session","id":"sess-1","cwd":"/w"}`))
	if err != nil || ev.SessionID != "sess-1" || ev.Assistant || ev.Kind != "session" {
		t.Errorf("session: %+v, %v", ev, err)
	}
	ev, _ = h.ParseEvent([]byte(`{"type":"message_end","message":{"role":"assistant","responseId":"r1","content":[{"type":"thinking","thinking":"hm"},{"type":"text","text":"  Hello there\nmore"}]}}`))
	if !ev.Assistant || ev.TurnID != "r1" || ev.Text != "Hello there\nmore" || ev.Activity != "Hello there" {
		t.Errorf("text: %+v", ev)
	}
	ev, _ = h.ParseEvent([]byte(`{"type":"message_end","message":{"role":"assistant","content":[{"type":"toolCall","name":"read","arguments":{"path":"/a/b.go"}}]}}`))
	if !ev.Assistant || ev.TurnID == "" || ev.Activity != "read: /a/b.go" {
		t.Errorf("tool: %+v", ev)
	}
	ev2, _ := h.ParseEvent([]byte(`{"type":"message_end","message":{"role":"assistant","content":"x"}}`))
	ev3, _ := h.ParseEvent([]byte(`{"type":"message_end","message":{"role":"assistant","content":"y"}}`))
	if ev2.TurnID == ev3.TurnID {
		t.Error("turn ids of distinct id-less messages collide")
	}
	ev, _ = h.ParseEvent([]byte(`{"type":"message_end","message":{"role":"user","content":"hi"}}`))
	if ev.Assistant || ev.Result {
		t.Errorf("user message: %+v", ev)
	}
	ev, _ = h.ParseEvent([]byte(`{"type":"turn_end","message":{"role":"assistant","stopReason":"stop","usage":{"cost":{"total":0.25}}}}`))
	if !ev.Result || ev.CostUSD != 0.25 || ev.IsError {
		t.Errorf("turn_end: %+v", ev)
	}
	ev, _ = h.ParseEvent([]byte(`{"type":"turn_end","message":{"role":"assistant","stopReason":"error","errorMessage":"429 usage limit reached"}}`))
	if !ev.Result || !ev.IsError || !ev.RateLimited || ev.ResultError == "" {
		t.Errorf("rate limit: %+v", ev)
	}
	ev, _ = h.ParseEvent([]byte(`{"type":"turn_end","message":{"stopReason":"error","errorMessage":"boom"}}`))
	if !ev.IsError || ev.RateLimited || ev.ResultError != "boom" {
		t.Errorf("error: %+v", ev)
	}
	if _, err := h.ParseEvent([]byte("not json")); err == nil {
		t.Error("garbage parsed")
	}
}

func TestOmpParseRefineResult(t *testing.T) {
	h := newOmpHarness("")
	obj := `{"title":"T","refined_prompt":"P","repos":[],"notes":"n"}`
	res, err := h.ParseRefineResult([]byte("Here:\n```json\n" + obj + "\n```\n"))
	if err != nil || res.IsError || !strings.Contains(string(res.Structured), `"refined_prompt":"P"`) {
		t.Errorf("fenced: %+v, %v", res, err)
	}
	if _, err := h.ParseRefineResult([]byte("  \n")); err == nil {
		t.Error("empty output accepted")
	}
}

func TestOmpValidationAndRegistry(t *testing.T) {
	h := newOmpHarness("")
	if h.ValidateEffort("") != nil || h.ValidateEffort("high") != nil || h.ValidateEffort("bogus") == nil {
		t.Error("effort validation")
	}
	for _, m := range []string{"auto", "plan"} {
		if h.ValidatePermissionMode(m) != nil {
			t.Errorf("mode %q rejected", m)
		}
	}
	for _, m := range []string{"", "acceptEdits", "yolo"} {
		if h.ValidatePermissionMode(m) == nil {
			t.Errorf("mode %q accepted", m)
		}
	}
	if c := h.Caps(); c.PermissionCallback || c.StructuredOutput || !c.PerTurnEvents {
		t.Errorf("caps = %+v", c)
	}
	if got, err := For("omp"); err != nil || got.Name() != "omp" {
		t.Errorf("For(omp) = %v, %v", got, err)
	}
	if !slices.Contains(Names(), "omp") {
		t.Errorf("Names = %v", Names())
	}
}
func TestOmpListModels(t *testing.T) {
	h := newOmpHarness(testenv.FakeOmp())
	// Only chat models, as the provider/id selector `--model` takes.
	if got, want := h.ListModels(), []string{"anthropic/claude-opus-4-5", "ollama/devstral:24b"}; !slices.Equal(got, want) {
		t.Errorf("ListModels = %v, want %v", got, want)
	}
	t.Setenv("FAKE_OMP_NO_MODELS", "1")
	if got := h.ListModels(); got != nil {
		t.Errorf("failing catalog: %v, want nil", got)
	}
	if got := newOmpHarness("/no/such/omp").ListModels(); got != nil {
		t.Errorf("missing binary: %v, want nil", got)
	}
	var _ ModelLister = h
}

func TestParseOmpModels(t *testing.T) {
	out := []byte("noise\n" + `{"models":[{"kind":"chat","selector":"a/b"},{"kind":"chat","selector":"a/b"},{"kind":"image","selector":"a/img"},{"selector":"c/d"},{"kind":"chat","selector":""}]}`)
	if got := parseOmpModels(out); !slices.Equal(got, []string{"a/b", "c/d"}) {
		t.Errorf("parse = %v", got)
	}
	for _, bad := range []string{"", "not json", `{"models":`} {
		if got := parseOmpModels([]byte(bad)); got != nil {
			t.Errorf("parse(%q) = %v", bad, got)
		}
	}
}

func TestOmpModelPassedToCommand(t *testing.T) {
	h := newOmpHarness("omp")
	cmd, err := h.SessionCommand(SessionSpec{Card: &model.Card{Model: "ollama/devstral:24b", PermissionMode: "auto"}, MCPConfigPath: filepath.Join(t.TempDir(), "mcp.json"), MCPURL: "http://127.0.0.1:1/mcp/1"})
	if err != nil {
		t.Fatal(err)
	}
	if i := slices.Index(cmd.Args, "--model"); i < 0 || cmd.Args[i+1] != "ollama/devstral:24b" {
		t.Errorf("args = %v", cmd.Args)
	}
}
