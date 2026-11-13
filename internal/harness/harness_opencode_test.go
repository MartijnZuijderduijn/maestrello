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

func ocSpec(kind string) SessionSpec {
	return SessionSpec{
		Card:   &model.Card{Model: "ollama/qwen3-coder:30b", PermissionMode: "auto", SessionID: "ses_abc", Kind: kind},
		Prompt: "do the thing", Instructions: "call mcp__maestrello__report_done",
		MCPURL: "http://127.0.0.1:1/mcp/7", Cwd: "/work", Root: "/root",
	}
}

// ocSession builds a session command and returns its args, env, stdin and parsed config.
func ocSession(t *testing.T, spec SessionSpec) (args, env []string, stdin string, cfg map[string]any) {
	t.Helper()
	spec.MCPConfigPath = filepath.Join(testenv.Dir(t), "7.mcp.json")
	cmd, err := newOpencodeHarness("/bin/opencode").SessionCommand(spec)
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Path != "/bin/opencode" || cmd.Dir != spec.Cwd {
		t.Errorf("path %q dir %q", cmd.Path, cmd.Dir)
	}
	in, _ := io.ReadAll(cmd.Stdin)
	data, err := os.ReadFile(spec.MCPConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(cmd.Env, "OPENCODE_CONFIG="+spec.MCPConfigPath) {
		t.Errorf("OPENCODE_CONFIG missing from env")
	}
	return cmd.Args[1:], cmd.Env, string(in), cfg
}

func ocAgent(t *testing.T, cfg map[string]any) (tools map[string]any, perm map[string]any) {
	t.Helper()
	a := cfg["agent"].(map[string]any)["maestrello"].(map[string]any)
	return a["tools"].(map[string]any), a["permission"].(map[string]any)
}

func TestOpencodeSessionCommand(t *testing.T) {
	args, _, stdin, cfg := ocSession(t, ocSpec(""))
	want := []string{"run", "--format", "json", "--agent", "maestrello", "-m", "ollama/qwen3-coder:30b"}
	if !slices.Equal(args, want) {
		t.Errorf("args = %v", args)
	}
	if stdin != "do the thing" {
		t.Errorf("stdin = %q", stdin)
	}
	mcp := cfg["mcp"].(map[string]any)["maestrello"].(map[string]any)
	if mcp["type"] != "remote" || mcp["url"] != "http://127.0.0.1:1/mcp/7" {
		t.Errorf("mcp = %v", mcp)
	}
	instr := cfg["instructions"].([]any)
	data, err := os.ReadFile(instr[0].(string))
	if err != nil || string(data) != "call maestrello_report_done" {
		t.Errorf("instructions file = %q, %v", data, err)
	}
	tools, perm := ocAgent(t, cfg)
	for _, n := range []string{"bash", "edit", "write", "maestrello_report_done", "maestrello_ask_user", "maestrello_add_repo"} {
		if tools[n] != true {
			t.Errorf("code card: tool %s = %v", n, tools[n])
		}
	}
	if tools["maestrello_report_plan"] != false || tools["maestrello_permission"] != false {
		t.Errorf("code card has plan/permission tools: %v", tools)
	}
	if perm["edit"] != "allow" || perm["bash"] != "allow" {
		t.Errorf("perm = %v", perm)
	}
}

func TestOpencodeSessionKinds(t *testing.T) {
	for _, tc := range []struct {
		kind     string
		on, off  []string
		readOnly bool
	}{
		{model.KindResearch, []string{"read", "grep", "maestrello_report_done"}, []string{"bash", "edit", "write", "maestrello_report_plan", "maestrello_add_repo"}, true},
		{model.KindPlan, []string{"read", "glob", "maestrello_report_plan", "maestrello_ask_user"}, []string{"bash", "edit", "maestrello_report_done", "maestrello_add_repo"}, true},
		{model.KindChat, []string{"maestrello_ask_user", "maestrello_report_failed"}, []string{"read", "bash", "edit", "maestrello_report_done", "maestrello_report_plan"}, true},
	} {
		_, _, _, cfg := ocSession(t, ocSpec(tc.kind))
		tools, perm := ocAgent(t, cfg)
		for _, n := range tc.on {
			if tools[n] != true {
				t.Errorf("%s: %s = %v, want on", tc.kind, n, tools[n])
			}
		}
		for _, n := range tc.off {
			if tools[n] != false {
				t.Errorf("%s: %s = %v, want off", tc.kind, n, tools[n])
			}
		}
		if tools["*"] != false {
			t.Errorf("%s: catch-all not off", tc.kind)
		}
		if perm["edit"] != "deny" || perm["bash"] != "deny" {
			t.Errorf("%s: perm = %v", tc.kind, perm)
		}
	}
	// Plan mode on a code card is read-only too.
	spec := ocSpec("")
	spec.Card.PermissionMode = "plan"
	_, _, _, cfg := ocSession(t, spec)
	tools, perm := ocAgent(t, cfg)
	if perm["edit"] != "deny" || tools["bash"] != false || tools["read"] != true {
		t.Errorf("plan mode: tools %v perm %v", tools, perm)
	}
}

func TestOpencodeSessionResumeAndStrict(t *testing.T) {
	spec := ocSpec("")
	spec.Resume, spec.Strict = true, true
	spec.Card.Model = ""
	args, _, _, cfg := ocSession(t, spec)
	if v, ok := flagValue(args, "-s"); !ok || v != "ses_abc" {
		t.Errorf("-s = %q, %v", v, ok)
	}
	if slices.Contains(args, "-m") {
		t.Errorf("empty model passes -m: %v", args)
	}
	if _, perm := ocAgent(t, cfg); perm["webfetch"] != "deny" {
		t.Errorf("strict run allows webfetch: %v", perm)
	}
	spec.Resume = false
	if args, _, _, _ := ocSession(t, spec); slices.Contains(args, "-s") {
		t.Errorf("fresh run has -s: %v", args)
	}
}

func TestOpencodeRefineCommand(t *testing.T) {
	h := newOpencodeHarness("/bin/opencode")
	ctx, cancel := context.WithCancel(context.Background())
	cmd, err := h.RefineCommand(ctx, RefineSpec{Model: "ollama/x", Prompt: "refine me", Cwd: "/root", Tools: "Read,Grep,Glob", Schema: `{"type":"object"}`})
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Path != "/bin/opencode" || cmd.Dir != "/root" {
		t.Errorf("path %q dir %q", cmd.Path, cmd.Dir)
	}
	if want := []string{"run", "--format", "json", "--agent", "maestrello", "-m", "ollama/x"}; !slices.Equal(cmd.Args[1:], want) {
		t.Errorf("args = %v", cmd.Args[1:])
	}
	in, _ := io.ReadAll(cmd.Stdin)
	if !strings.HasPrefix(string(in), "refine me") || !strings.Contains(string(in), `{"type":"object"}`) {
		t.Errorf("stdin = %q", in)
	}
	var cfg map[string]any
	for _, kv := range cmd.Env {
		if v, ok := strings.CutPrefix(kv, "OPENCODE_CONFIG_CONTENT="); ok {
			json.Unmarshal([]byte(v), &cfg)
		}
	}
	if cfg == nil || cfg["mcp"] != nil {
		t.Fatalf("inline config = %v", cfg)
	}
	tools, perm := ocAgent(t, cfg)
	if tools["read"] != true || tools["grep"] != true || tools["bash"] != false || tools["edit"] != false || perm["edit"] != "deny" || perm["bash"] != "deny" {
		t.Errorf("tools %v perm %v", tools, perm)
	}
	cancel()
	if cmd.Cancel == nil {
		t.Error("refine command is not bound to ctx")
	}
	// No tools: nothing is on.
	cmd, _ = h.RefineCommand(context.Background(), RefineSpec{Tools: ""})
	for _, kv := range cmd.Env {
		if v, ok := strings.CutPrefix(kv, "OPENCODE_CONFIG_CONTENT="); ok {
			json.Unmarshal([]byte(v), &cfg)
		}
	}
	if tools, _ := ocAgent(t, cfg); tools["read"] != false {
		t.Errorf("no-tools refine has read on: %v", tools)
	}
}

func TestOpencodeParseEvent(t *testing.T) {
	h := newOpencodeHarness("")
	ev, err := h.ParseEvent([]byte(`{"type":"text","timestamp":1,"sessionID":"ses_1","part":{"type":"text","messageID":"msg_1","text":"  Hello there\nmore"}}`))
	if err != nil || !ev.Assistant || ev.TurnID != "msg_1" || ev.Text != "Hello there\nmore" || ev.Activity != "Hello there" || ev.SessionID != "ses_1" || ev.Kind != "text" {
		t.Errorf("text: %+v, %v", ev, err)
	}
	ev, _ = h.ParseEvent([]byte(`{"type":"tool_use","sessionID":"ses_1","part":{"type":"tool","messageID":"msg_2","tool":"read","state":{"status":"completed","input":{"filePath":"/a/b.go"}}}}`))
	if !ev.Assistant || ev.TurnID != "msg_2" || ev.Activity != "read: /a/b.go" {
		t.Errorf("tool: %+v", ev)
	}
	ev, _ = h.ParseEvent([]byte(`{"type":"tool_use","sessionID":"ses_1","part":{"tool":"bash","messageID":"m","state":{"input":{"command":"go test ./..."}}}}`))
	if ev.Activity != "bash: go test ./..." {
		t.Errorf("bash activity = %q", ev.Activity)
	}
	ev, _ = h.ParseEvent([]byte(`{"type":"step_finish","sessionID":"ses_1","part":{"type":"step-finish","reason":"stop","cost":0.25}}`))
	if !ev.Result || ev.CostUSD != 0.25 || ev.IsError || ev.Assistant {
		t.Errorf("step_finish: %+v", ev)
	}
	ev, _ = h.ParseEvent([]byte(`{"type":"step_start","sessionID":"ses_1","part":{}}`))
	if ev.Assistant || ev.Result || ev.SessionID != "ses_1" {
		t.Errorf("step_start: %+v", ev)
	}
	ev, _ = h.ParseEvent([]byte(`{"type":"error","sessionID":"ses_1","error":{"name":"UnknownError","data":{"message":"Error: Unable to connect."}}}`))
	if !ev.Result || !ev.IsError || ev.ResultError != "Error: Unable to connect." || ev.RateLimited {
		t.Errorf("error: %+v", ev)
	}
	ev, _ = h.ParseEvent([]byte(`{"type":"error","error":{"name":"APIError","data":{"message":"429 Too Many Requests"}}}`))
	if !ev.RateLimited || ev.RateLimitMsg == "" {
		t.Errorf("rate limit: %+v", ev)
	}
	if _, err := h.ParseEvent([]byte("not json")); err == nil {
		t.Error("garbage parsed")
	}
}

func TestOpencodeParseRefineResult(t *testing.T) {
	h := newOpencodeHarness("")
	obj := `{"title":"T","refined_prompt":"P","repos":[],"notes":"n"}`
	for name, text := range map[string]string{
		"plain":  obj,
		"fenced": "Here you go:\n```json\n" + obj + "\n```\nDone.",
		"prose":  "Result: " + obj + " thanks",
	} {
		line, _ := json.Marshal(map[string]any{"type": "text", "sessionID": "ses_1", "part": map[string]any{"type": "text", "text": text}})
		fin := `{"type":"step_finish","sessionID":"ses_1","part":{"cost":0.5}}`
		res, err := h.ParseRefineResult([]byte(`{"type":"step_start","sessionID":"ses_1","part":{}}` + "\n" + string(line) + "\n" + fin + "\n"))
		if err != nil || res.IsError || res.CostUSD != 0.5 {
			t.Fatalf("%s: %+v, %v", name, res, err)
		}
		var r struct {
			Title         string `json:"title"`
			RefinedPrompt string `json:"refined_prompt"`
		}
		if err := json.Unmarshal(res.Structured, &r); err != nil || r.RefinedPrompt != "P" || r.Title != "T" {
			t.Errorf("%s: structured %s: %v", name, res.Structured, err)
		}
	}
	res, err := h.ParseRefineResult([]byte(`{"type":"error","error":{"data":{"message":"no model"}}}` + "\n"))
	if err != nil || !res.IsError || res.Text != "no model" {
		t.Errorf("error: %+v, %v", res, err)
	}
	res, err = h.ParseRefineResult([]byte(`{"type":"text","part":{"text":"no json here"}}` + "\n"))
	if err != nil || res.Structured != nil || res.Text != "no json here" {
		t.Errorf("no json: %+v, %v", res, err)
	}
	if _, err := h.ParseRefineResult([]byte("garbage")); err == nil {
		t.Error("garbage accepted")
	}
}

func TestOpencodeValidationAndRegistry(t *testing.T) {
	h := newOpencodeHarness("")
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
	c := h.Caps()
	if !c.Resume || c.PermissionCallback || c.StructuredOutput || !c.PerTurnEvents || len(c.Efforts) != 0 {
		t.Errorf("caps = %+v", c)
	}
	if got, err := For("opencode"); err != nil || got.Name() != "opencode" {
		t.Errorf("harnessFor(opencode) = %v, %v", got, err)
	}
	if _, err := For("codex"); err == nil {
		t.Error("codex accepted")
	}
	if h.ResumableSession("5d1c-uuid") || !h.ResumableSession("ses_x") {
		t.Error("ResumableSession wrong")
	}
	if got := parseModelList("ollama/a:b\n\nopencode/c-free\nnot a model\n"); !slices.Equal(got, []string{"ollama/a:b", "opencode/c-free"}) {
		t.Errorf("models = %v", got)
	}
}
