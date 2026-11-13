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

func mvSpec(kind string) SessionSpec {
	return SessionSpec{
		Card:   &model.Card{Model: "devstral-2", PermissionMode: "auto", SessionID: "abc", Kind: kind},
		Prompt: "do the thing", Instructions: "call mcp__maestrello__report_done",
		MCPURL: "http://127.0.0.1:1/mcp/7", Cwd: "/work", Root: "/root",
	}
}

// mvSession builds a session command; it returns args, env, stdin and the files of the Vibe home.
func mvSession(t *testing.T, spec SessionSpec) (args, env []string, stdin, config, agent string) {
	t.Helper()
	spec.MCPConfigPath = filepath.Join(testenv.Dir(t), "7.mcp.json")
	cmd, err := newMistralHarness("/bin/vibe").SessionCommand(spec)
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Path != "/bin/vibe" || cmd.Dir != spec.Cwd {
		t.Errorf("path %q dir %q", cmd.Path, cmd.Dir)
	}
	in, _ := io.ReadAll(cmd.Stdin)
	home := spec.MCPConfigPath + ".vibe"
	if !slices.Contains(cmd.Env, "VIBE_HOME="+home) {
		t.Errorf("VIBE_HOME missing from env")
	}
	if _, err := os.Stat(spec.MCPConfigPath); err != nil {
		t.Errorf("no file at MCPConfigPath: %v", err)
	}
	c, err := os.ReadFile(filepath.Join(home, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	a, err := os.ReadFile(filepath.Join(home, "agents", "maestrello.toml"))
	if err != nil {
		t.Fatal(err)
	}
	return cmd.Args[1:], cmd.Env, string(in), string(c), string(a)
}

func enabledTools(args []string) []string {
	var out []string
	for i, a := range args {
		if a == "--enabled-tools" && i+1 < len(args) {
			out = append(out, args[i+1])
		}
	}
	return out
}

func TestMistralSessionCommand(t *testing.T) {
	args, _, stdin, cfg, agent := mvSession(t, mvSpec(""))
	if !slices.Equal(args[:7], []string{"-p", "--output", "streaming", "--agent", "maestrello", "--trust", "--model"}) || args[7] != "devstral-2" {
		t.Errorf("args = %v", args)
	}
	if stdin != "do the thing" {
		t.Errorf("stdin = %q", stdin)
	}
	for _, want := range []string{`name = "maestrello"`, `transport = "streamable-http"`, `url = "http://127.0.0.1:1/mcp/7"`, `[tools.bash]`, `[tools.maestrello_report_done]`, `permission = "always"`} {
		if !strings.Contains(cfg, want) {
			t.Errorf("config lacks %s:\n%s", want, cfg)
		}
	}
	if !strings.Contains(agent, `call maestrello_report_done`) || strings.Contains(agent, "mcp__") {
		t.Errorf("agent file = %s", agent)
	}
	tools := enabledTools(args)
	for _, n := range []string{"bash", "edit", "write_file", "read_file", "maestrello_report_done", "maestrello_add_repo"} {
		if !slices.Contains(tools, n) {
			t.Errorf("code card lacks %s: %v", n, tools)
		}
	}
	if slices.Contains(tools, "maestrello_report_plan") || slices.Contains(tools, "maestrello_permission") {
		t.Errorf("code card has plan/permission tools: %v", tools)
	}
}

func TestMistralSessionKinds(t *testing.T) {
	writes := []string{"bash", "edit", "write_file", "search_replace"}
	for _, tc := range []struct {
		kind    string
		on, off []string
	}{
		{model.KindResearch, []string{"read_file", "grep", "maestrello_report_done"}, []string{"maestrello_report_plan", "maestrello_add_repo"}},
		{model.KindPlan, []string{"read_file", "maestrello_report_plan", "maestrello_ask_user"}, []string{"maestrello_report_done", "maestrello_add_repo"}},
		{model.KindChat, []string{"maestrello_ask_user", "maestrello_report_failed"}, []string{"read_file", "grep", "maestrello_report_done"}},
	} {
		args, _, _, cfg, _ := mvSession(t, mvSpec(tc.kind))
		tools := enabledTools(args)
		for _, n := range tc.on {
			if !slices.Contains(tools, n) {
				t.Errorf("%s: %s off", tc.kind, n)
			}
		}
		for _, n := range append(tc.off, writes...) {
			if slices.Contains(tools, n) || strings.Contains(cfg, "[tools."+n+"]") {
				t.Errorf("%s: %s on", tc.kind, n)
			}
		}
	}
	// Plan mode on a code card is read-only too.
	spec := mvSpec("")
	spec.Card.PermissionMode = "plan"
	args, _, _, _, _ := mvSession(t, spec)
	tools := enabledTools(args)
	for _, n := range writes {
		if slices.Contains(tools, n) {
			t.Errorf("plan mode has %s", n)
		}
	}
	if !slices.Contains(tools, "read_file") {
		t.Error("plan mode lost read_file")
	}
}

func TestMistralResumeAndModel(t *testing.T) {
	spec := mvSpec("")
	spec.Resume, spec.Card.Model = true, ""
	args, _, _, _, _ := mvSession(t, spec)
	if slices.Contains(args, "--resume") || slices.Contains(args, "--model") {
		t.Errorf("args = %v", args)
	}
	if newMistralHarness("").Caps().Resume {
		t.Error("Resume must be false: the stream reports no session id")
	}
}

func TestMistralRefineCommand(t *testing.T) {
	h := newMistralHarness("/bin/vibe")
	ctx, cancel := context.WithCancel(context.Background())
	cmd, err := h.RefineCommand(ctx, RefineSpec{Model: "devstral-2", Prompt: "refine me", Cwd: "/root", Tools: "Read,Grep,Glob", Schema: `{"type":"object"}`})
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Path != "/bin/vibe" || cmd.Dir != "/root" {
		t.Errorf("path %q dir %q", cmd.Path, cmd.Dir)
	}
	if got := enabledTools(cmd.Args[1:]); !slices.Equal(got, []string{"read_file", "grep"}) {
		t.Errorf("tools = %v", got)
	}
	if v, _ := flagValue(cmd.Args[1:], "--output"); v != "text" {
		t.Errorf("output = %q", v)
	}
	in, _ := io.ReadAll(cmd.Stdin)
	if !strings.HasPrefix(string(in), "refine me") || !strings.Contains(string(in), `{"type":"object"}`) {
		t.Errorf("stdin = %q", in)
	}
	for _, kv := range cmd.Env {
		if home, ok := strings.CutPrefix(kv, "VIBE_HOME="); ok {
			defer os.RemoveAll(home)
			cfg, _ := os.ReadFile(filepath.Join(home, "config.toml"))
			if strings.Contains(string(cfg), "mcp_servers") || strings.Contains(string(cfg), "bash") {
				t.Errorf("refine config = %s", cfg)
			}
		}
	}
	cancel()
	if cmd.Cancel == nil {
		t.Error("refine command is not bound to ctx")
	}
	cmd, _ = h.RefineCommand(context.Background(), RefineSpec{})
	if got := enabledTools(cmd.Args[1:]); len(got) != 1 || got[0] != "re:^$" {
		t.Errorf("no-tools refine = %v", got)
	}
}

func TestMistralParseEvent(t *testing.T) {
	h := newMistralHarness("")
	ev, err := h.ParseEvent([]byte(`{"role":"assistant","content":"  Hello there\nmore","message_id":"m1"}`))
	if err != nil || !ev.Assistant || ev.TurnID != "m1" || ev.Text != "Hello there\nmore" || ev.Activity != "Hello there" || ev.Kind != "assistant" {
		t.Errorf("text: %+v, %v", ev, err)
	}
	ev, _ = h.ParseEvent([]byte(`{"role":"assistant","content":null,"tool_calls":[{"function":{"name":"read_file","arguments":"{\"path\":\"/a/b.go\"}"}}]}`))
	if !ev.Assistant || ev.TurnID == "" || ev.Activity != "read_file: /a/b.go" {
		t.Errorf("tool: %+v", ev)
	}
	ev2, _ := h.ParseEvent([]byte(`{"role":"assistant","content":"x"}`))
	ev3, _ := h.ParseEvent([]byte(`{"role":"assistant","content":"y"}`))
	if ev2.TurnID == ev3.TurnID {
		t.Error("turn ids of distinct id-less messages collide")
	}
	ev, _ = h.ParseEvent([]byte(`{"role":"tool","content":"ok"}`))
	if ev.Assistant || ev.Result || ev.Kind != "tool" {
		t.Errorf("tool result: %+v", ev)
	}
	if _, err := h.ParseEvent([]byte("not json")); err == nil {
		t.Error("garbage parsed")
	}
}

func TestMistralParseRefineResult(t *testing.T) {
	h := newMistralHarness("")
	obj := `{"title":"T","refined_prompt":"P","repos":[],"notes":"n"}`
	for name, text := range map[string]string{
		"plain":  obj,
		"fenced": "Here you go:\n```json\n" + obj + "\n```\nDone.",
		"prose":  "Result: " + obj + " thanks",
	} {
		res, err := h.ParseRefineResult([]byte(text + "\n"))
		if err != nil || res.IsError || !strings.Contains(string(res.Structured), `"refined_prompt":"P"`) {
			t.Errorf("%s: %+v, %v", name, res, err)
		}
	}
	res, err := h.ParseRefineResult([]byte("no json here\n"))
	if err != nil || res.Structured != nil || res.Text != "no json here" {
		t.Errorf("no json: %+v, %v", res, err)
	}
	if _, err := h.ParseRefineResult([]byte("  \n")); err == nil {
		t.Error("empty output accepted")
	}
}

func TestMistralValidationAndRegistry(t *testing.T) {
	h := newMistralHarness("")
	if h.ValidateEffort("") != nil || h.ValidateEffort("low") == nil {
		t.Error("effort: only empty is valid")
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
	if c := h.Caps(); c.Resume || c.PermissionCallback || c.StructuredOutput || !c.PerTurnEvents || len(c.Efforts) != 0 {
		t.Errorf("caps = %+v", c)
	}
	if got, err := For("mistral"); err != nil || got.Name() != "mistral" {
		t.Errorf("For(mistral) = %v, %v", got, err)
	}
	if !slices.Contains(Names(), "mistral") {
		t.Errorf("Names = %v", Names())
	}
}

func TestMistralUserModels(t *testing.T) {
	home := t.TempDir()
	t.Setenv("VIBE_HOME", home)
	cfg := "theme = \"auto\"\nactive_model = \"mine\"\n\n[[providers]]\nname = \"p\"\n\n[[models]]\nname = \"x\"\nalias = \"mine\"\nprovider = \"p\"\n\n[tools.bash]\npermission = \"ask\"\n"
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := (mistralHarness{}).ListModels(); !slices.Equal(got, []string{"mistral-medium-3.5", "mine"}) {
		t.Errorf("ListModels = %v", got)
	}
	out := mvConfig([]string{"read_file"}, "")
	for _, want := range []string{"active_model = \"mine\"", "[[models]]", "alias = \"mine\"", "[[providers]]"} {
		if !strings.Contains(out, want) {
			t.Errorf("config lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "permission = \"ask\"") || strings.Contains(out, "theme") {
		t.Errorf("config copied unrelated settings:\n%s", out)
	}
}
