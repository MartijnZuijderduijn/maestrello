package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func ocSpec(kind string) SessionSpec {
	return SessionSpec{
		Card:   &Card{Model: "ollama/qwen3-coder:30b", PermissionMode: "auto", SessionID: "ses_abc", Kind: kind},
		Prompt: "do the thing", Instructions: "call mcp__maestrello__report_done",
		MCPURL: "http://127.0.0.1:1/mcp/7", Cwd: "/work", Root: "/root",
	}
}

// ocSession builds a session command and returns its args, env, stdin and parsed config.
func ocSession(t *testing.T, spec SessionSpec) (args, env []string, stdin string, cfg map[string]any) {
	t.Helper()
	spec.MCPConfigPath = filepath.Join(testDir(t), "7.mcp.json")
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
		{KindResearch, []string{"read", "grep", "maestrello_report_done"}, []string{"bash", "edit", "write", "maestrello_report_plan", "maestrello_add_repo"}, true},
		{KindPlan, []string{"read", "glob", "maestrello_report_plan", "maestrello_ask_user"}, []string{"bash", "edit", "maestrello_report_done", "maestrello_add_repo"}, true},
		{KindChat, []string{"maestrello_ask_user", "maestrello_report_failed"}, []string{"read", "bash", "edit", "maestrello_report_done", "maestrello_report_plan"}, true},
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
		var r refineResult
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
	if got, err := harnessFor("opencode"); err != nil || got.Name() != "opencode" {
		t.Errorf("harnessFor(opencode) = %v, %v", got, err)
	}
	if _, err := harnessFor("codex"); err == nil {
		t.Error("codex accepted")
	}
	if h.ResumableSession("5d1c-uuid") || !h.ResumableSession("ses_x") {
		t.Error("ResumableSession wrong")
	}
	if got := parseModelList("ollama/a:b\n\nopencode/c-free\nnot a model\n"); !slices.Equal(got, []string{"ollama/a:b", "opencode/c-free"}) {
		t.Errorf("models = %v", got)
	}
}

func TestEachHarnessOwnBinaryAtCallTime(t *testing.T) {
	app := newTestApp(t, testDir(t))
	app.claudeBin, app.opencodeBin = "/changed/claude", "/changed/opencode"
	for name, want := range map[string]string{"claude": "/changed/claude", "opencode": "/changed/opencode", "": "/changed/claude"} {
		h, err := app.harness(name)
		if err != nil {
			t.Fatal(err)
		}
		cmd, _ := h.RefineCommand(context.Background(), RefineSpec{})
		if cmd.Path != want {
			t.Errorf("%q: path = %q, want %q", name, cmd.Path, want)
		}
	}
	app.opencodeBin = "/changed/again"
	h, _ := app.harness("opencode")
	if cmd, _ := h.RefineCommand(context.Background(), RefineSpec{}); cmd.Path != "/changed/again" {
		t.Errorf("opencode path not read at call time: %q", cmd.Path)
	}
}

func TestParseStartOpencodeFlag(t *testing.T) {
	o, err := parseStart(nil, io.Discard)
	if err != nil || o.opencodeBin != "opencode" || o.claudeBin != "claude" {
		t.Fatalf("defaults: %+v, %v", o, err)
	}
	o, err = parseStart([]string{"-opencode", "testdata/fake-opencode.sh"}, io.Discard)
	if err != nil || !filepath.IsAbs(o.opencodeBin) || !strings.HasSuffix(o.opencodeBin, "testdata/fake-opencode.sh") {
		t.Errorf("relative path not made absolute: %+v, %v", o, err)
	}
}

func TestHarnessAvailability(t *testing.T) {
	dir := testDir(t)
	app := newTestApp(t, dir)
	app.opencodeBin = filepath.Join(dir, "no-such-opencode")
	st := app.harnessStatus("opencode")
	if st.Available || !strings.Contains(st.Error, "does not exist") {
		t.Errorf("missing bin: %+v", st)
	}
	if err := app.requireHarness("opencode"); err == nil || !strings.HasPrefix(err.Error(), "harness opencode is not available: ") {
		t.Errorf("requireHarness = %v", err)
	}
	app.opencodeBin = "definitely-not-on-path-opencode"
	if st := app.harnessStatus("opencode"); st.Available || !strings.Contains(st.Error, "not found in PATH") {
		t.Errorf("PATH miss: %+v", st) // the cache notices the changed binary
	}
	bad := filepath.Join(dir, "bad-opencode.sh")
	os.WriteFile(bad, []byte("#!/usr/bin/env bash\necho nope >&2\nexit 2\n"), 0o755)
	app.opencodeBin = bad
	if st := app.harnessStatus("opencode"); st.Available || !strings.Contains(st.Error, "nope") {
		t.Errorf("failing --version: %+v", st)
	}

	fake, _ := filepath.Abs("testdata/fake-opencode.sh")
	app.opencodeBin = fake
	st = app.harnessStatus("opencode")
	if !st.Available || st.Version != "1.2.10-fake" || st.Error != "" || !slices.Contains(st.Models, "ollama/fake-model:1b") {
		t.Errorf("working fake: %+v", st)
	}
	if err := app.requireHarness("opencode"); err != nil {
		t.Errorf("requireHarness = %v", err)
	}
	// The result is cached: breaking the script does not show until a refresh.
	os.WriteFile(bad, []byte("#!/usr/bin/env bash\nexit 1\n"), 0o755)
	if !app.harnessStatus("opencode").Available {
		t.Error("status not cached")
	}
}

func TestHarnessesEndpointAvailability(t *testing.T) {
	dir := testDir(t)
	app := newTestApp(t, dir)
	var got []harnessInfo
	sendJSON(t, "GET", app.baseURL+"/api/harnesses", nil, &got)
	if len(got) != 2 {
		t.Fatalf("harnesses = %+v", got)
	}
	if !got[0].Available || got[0].Version == "" || !slices.Equal(got[0].Models, knownModels) {
		t.Errorf("claude = %+v", got[0])
	}
	if !got[1].Available || got[1].Version != "1.2.10-fake" || !slices.Contains(got[1].Models, "opencode/fake-free") {
		t.Errorf("opencode = %+v", got[1])
	}
	// Point opencode at nothing: cached until ?refresh=1 (the changed path triggers a re-check anyway).
	app.opencodeBin = filepath.Join(dir, "gone")
	sendJSON(t, "GET", app.baseURL+"/api/harnesses?refresh=1", nil, &got)
	if got[1].Available || got[1].Error == "" || len(got[1].Models) != 0 || got[1].Models == nil {
		t.Errorf("unavailable opencode = %+v", got[1])
	}
	if !got[0].Available {
		t.Errorf("claude lost: %+v", got[0])
	}
}

func TestUnavailableHarnessFailsCard(t *testing.T) {
	dir := testDir(t)
	app := newTestApp(t, dir)
	marker := filepath.Join(dir, "started")
	bin := filepath.Join(dir, "touchy-opencode.sh")
	// --version fails, and any other call would leave a marker: it must never run.
	os.WriteFile(bin, []byte("#!/usr/bin/env bash\n[ \"${1:-}\" = --version ] && exit 1\ntouch "+marker+"\n"), 0o755)
	app.opencodeBin = bin

	// A registered but unavailable harness is still accepted for cards and settings.
	var c Card
	if code := sendJSON(t, "POST", app.baseURL+"/api/cards", map[string]any{
		"title": "t", "root_folder": dir, "harness": "opencode", "model": "ollama/x", "effort": "", "permission_mode": "auto"}, &c); code != 201 {
		t.Fatalf("create opencode card = %d", code)
	}
	card, _ := app.store.GetCard(c.ID)
	card.Status, card.RefinedPrompt = StatusReady, "do it"
	app.store.SaveCard(card)
	app.Run(c.ID)
	card = waitStatus(t, app, c.ID, StatusFailed)
	if !strings.HasPrefix(card.FailReason, "harness opencode is not available: ") {
		t.Errorf("fail reason = %q", card.FailReason)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("a process was started")
	}

	// Refinement uses the settings' harness.
	st, _ := app.store.Settings()
	st.Harness = "opencode"
	app.store.SaveSettings(st)
	c2 := newCard(t, app, dir, "refine me")
	app.Refine(c2.ID)
	card = waitStatus(t, app, c2.ID, StatusFailed)
	if !strings.Contains(card.FailReason, "harness opencode is not available: ") {
		t.Errorf("refine fail reason = %q", card.FailReason)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("a refinement process was started")
	}
}

func TestOpencodeCardEndToEnd(t *testing.T) {
	dir, root := plainRoot(t)
	app := newTestApp(t, dir)
	// Refine through opencode too: settings pick the harness.
	st, _ := app.store.Settings()
	st.Harness, st.RefineModel, st.RefineEffort = "opencode", "ollama/fake-model:1b", ""
	app.store.SaveSettings(st)
	c := newCard(t, app, root, "WRITE hello")
	c.Harness, c.Effort, c.Model = "opencode", "", "ollama/fake-model:1b"
	app.store.SaveCard(c)
	if err := app.Refine(c.ID); err != nil {
		t.Fatal(err)
	}
	c = waitStatus(t, app, c.ID, StatusReady)
	if !strings.Contains(c.RefinedPrompt, "hello") {
		t.Fatalf("refined prompt = %q", c.RefinedPrompt)
	}
	app.Run(c.ID)
	c = waitStatus(t, app, c.ID, StatusReview)
	waitIdle(t, app, c.ID)
	if !strings.HasPrefix(c.SessionID, "ses_fake") {
		t.Errorf("session id %q was not taken from the stream", c.SessionID)
	}
	if c.TurnsUsed < 1 {
		t.Errorf("turns used = %d", c.TurnsUsed)
	}
	if !strings.Contains(c.Summary, "fake work done") {
		t.Errorf("summary = %q", c.Summary)
	}
	if _, err := os.Stat(filepath.Join(root, "CHANGED.txt")); err != nil {
		t.Errorf("agent did not work in the root folder: %v", err)
	}
	// Send back: resumes the same opencode session.
	id := c.SessionID
	if err := app.SendBack(c.ID, "more please"); err != nil {
		t.Fatal(err)
	}
	c = waitStatus(t, app, c.ID, StatusReview)
	if c.SessionID != id {
		t.Errorf("session id changed on resume: %q -> %q", id, c.SessionID)
	}
}
