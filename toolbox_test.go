package main

import (
	"database/sql"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func toolSpec(kind string, tools ...string) SessionSpec {
	spec := testSpec()
	spec.Card.Kind, spec.Card.Tools = kind, tools
	return spec
}

func TestClaudeToolboxArgs(t *testing.T) {
	// Chat: --tools lists the tool, and it is allowed next to the chat board tools.
	args, _, _ := sessionArgs(t, toolSpec(KindChat, "web_search"))
	if v, ok := flagValue(args, "--tools"); !ok || v != "WebSearch" {
		t.Errorf("chat --tools = %q, %v", v, ok)
	}
	allowed, _ := flagValue(args, "--allowedTools")
	if allowed != "mcp__maestrello__report_failed,mcp__maestrello__ask_user,WebSearch" {
		t.Errorf("chat allowed = %s", allowed)
	}
	if slices.Contains(args, "--add-dir") {
		t.Error("chat got --add-dir")
	}
	// Read-only kinds keep Read,Grep,Glob and add the tool.
	for _, kind := range []string{KindResearch, KindPlan} {
		args, _, _ := sessionArgs(t, toolSpec(kind, "web_search", "skills"))
		if v, _ := flagValue(args, "--tools"); v != "Read,Grep,Glob,WebSearch,Skill" {
			t.Errorf("%s --tools = %q", kind, v)
		}
		allowed, _ := flagValue(args, "--allowedTools")
		if !strings.HasSuffix(allowed, ",WebSearch,Skill") || strings.Contains(allowed, "Edit") || strings.Contains(allowed, "Bash") {
			t.Errorf("%s allowed = %s", kind, allowed)
		}
	}
	// Code: still no --tools flag, but the tool is allowed.
	args, _, _ = sessionArgs(t, toolSpec(KindCode, "web_fetch"))
	if slices.Contains(args, "--tools") {
		t.Errorf("code card has --tools: %v", args)
	}
	allowed, _ = flagValue(args, "--allowedTools")
	if !strings.HasSuffix(allowed, ",WebFetch") {
		t.Errorf("code allowed = %s", allowed)
	}
}

func TestEmptyToolboxArgsUnchanged(t *testing.T) {
	for _, kind := range []string{"", KindCode, KindResearch, KindPlan, KindChat} {
		base, _, _ := sessionArgs(t, toolSpec(kind))
		for _, empty := range [][]string{nil, {}} {
			spec := toolSpec(kind)
			spec.Card.Tools = empty
			got, _, _ := sessionArgs(t, spec)
			got, base := slices.Clone(got), slices.Clone(base)
			i := slices.Index(base, "--mcp-config") + 1
			got[i] = base[i]
			if !slices.Equal(got, base) {
				t.Errorf("%s: args differ with empty toolbox:\n%v\n%v", kind, got, base)
			}
		}
	}
	// And the literal values from before the toolbox.
	args, _, _ := sessionArgs(t, toolSpec(KindChat))
	if v, ok := flagValue(args, "--tools"); !ok || v != "" {
		t.Errorf("chat --tools = %q, %v", v, ok)
	}
}

func TestOpencodeToolbox(t *testing.T) {
	spec := ocSpec(KindChat)
	spec.Card.Tools = []string{"web_search", "skills"}
	spec.Strict = true
	_, _, _, cfg := ocSession(t, spec)
	tools, perm := ocAgent(t, cfg)
	if tools["websearch"] != true || perm["websearch"] != "allow" {
		t.Errorf("websearch: tool %v perm %v", tools["websearch"], perm["websearch"])
	}
	if tools["webfetch"] != false || perm["webfetch"] != "deny" {
		t.Errorf("strict chat: webfetch tool %v perm %v", tools["webfetch"], perm["webfetch"])
	}
	if tools["bash"] != false || tools["edit"] != false || perm["bash"] != "deny" {
		t.Errorf("chat with tools can write: %v %v", tools, perm)
	}
	if _, has := tools["Skill"]; has || tools["skill"] == true {
		t.Errorf("skills leaked into opencode: %v", tools)
	}
	if _, unsupported := spec.Card.nativeTools("opencode"); !slices.Equal(unsupported, []string{"skills"}) {
		t.Errorf("unsupported = %v", unsupported)
	}
	// Strict denies webfetch unless the card asked for it.
	spec = ocSpec(KindCode)
	spec.Card.Tools, spec.Strict = []string{"web_fetch"}, true
	_, _, _, cfg = ocSession(t, spec)
	tools, perm = ocAgent(t, cfg)
	if tools["webfetch"] != true || perm["webfetch"] != "allow" {
		t.Errorf("selected web_fetch in strict: %v %v", tools["webfetch"], perm["webfetch"])
	}
	// An empty toolbox: nothing about websearch.
	_, _, _, cfg = ocSession(t, ocSpec(KindChat))
	tools, perm = ocAgent(t, cfg)
	if tools["websearch"] != false || perm["websearch"] != nil {
		t.Errorf("empty toolbox: %v %v", tools["websearch"], perm["websearch"])
	}
}

func TestOpencodeSkippedToolNote(t *testing.T) {
	dir := testDir(t)
	app := newTestApp(t, dir)
	root := kindRoot(t, dir)
	yes, h, kind, tools := true, "opencode", KindChat, []string{"web_search", "skills"}
	text := "hello"
	c, err := app.CreateCard(cardInput{Description: &text, RootFolder: &root, Kind: &kind, Harness: &h, Tools: &tools, SkipRefinement: &yes})
	if err != nil {
		t.Fatal(err)
	}
	app.Run(c.ID)
	deadline := time.Now().Add(10 * time.Second)
	for !hasEvent(t, app, c.ID, "tool skills not supported by opencode; skipped") {
		if time.Now().After(deadline) {
			t.Fatal("no note about the skipped tool")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if hasEvent(t, app, c.ID, "tool web_search not supported") {
		t.Error("web_search noted as unsupported")
	}
	app.Stop(c.ID)
}

func TestUnknownToolRejected(t *testing.T) {
	dir := testDir(t)
	app := newTestApp(t, dir)
	root := kindRoot(t, dir)
	var out map[string]string
	code := sendJSON(t, "POST", app.baseURL+"/api/cards", map[string]any{"description": "x", "root_folder": root, "tools": []string{"web_search", "telepathy"}}, &out)
	if code != 400 || out["error"] != "unknown tool telepathy" {
		t.Errorf("HTTP create: %d %v", code, out)
	}
	text, isErr := boardCall(t, app, "create_card", map[string]any{"description": "x", "root_folder": root, "tools": []string{"nope"}})
	if !isErr || !strings.Contains(text, "unknown tool nope") {
		t.Errorf("MCP create: %q %v", text, isErr)
	}
	c := newCard(t, app, root, "x")
	if _, isErr := boardCall(t, app, "update_card", map[string]any{"id": c.ID, "tools": []string{"nope"}}); !isErr {
		t.Error("MCP update accepted an unknown tool")
	}
	// Valid ids are kept, duplicates dropped, through HTTP and MCP; the card keeps them.
	var got Card
	code = sendJSON(t, "PATCH", app.baseURL+"/api/cards/"+idStr(c.ID), map[string]any{"tools": []string{"web_fetch", "web_search", "web_fetch"}}, &got)
	if code != 200 || !slices.Equal(got.Tools, []string{"web_fetch", "web_search"}) {
		t.Errorf("patch: %d %v", code, got.Tools)
	}
	if stored, _ := app.store.GetCard(c.ID); !slices.Equal(stored.Tools, []string{"web_fetch", "web_search"}) {
		t.Errorf("stored tools = %v", stored.Tools)
	}
	if _, isErr := boardCall(t, app, "update_card", map[string]any{"id": c.ID, "tools": []string{}}); isErr {
		t.Error("clearing tools failed")
	}
	if stored, _ := app.store.GetCard(c.ID); len(stored.Tools) != 0 {
		t.Errorf("tools not cleared: %v", stored.Tools)
	}
}

func TestSettingsDefaultTools(t *testing.T) {
	dir := testDir(t)
	app := newTestApp(t, dir)
	root := kindRoot(t, dir)
	url := app.baseURL + "/api/settings"
	st := defaultSettings()
	st.DefaultRootFolder = root
	st.DefaultTools = map[string][]string{"chat": {"web_search", "web_search"}, "research": {"web_search"}}
	var saved Settings
	if code := sendJSON(t, "PUT", url, st, &saved); code != 200 || !slices.Equal(saved.DefaultTools["chat"], []string{"web_search"}) {
		t.Fatalf("PUT: %d %v", code, saved.DefaultTools)
	}
	for name, dt := range map[string]map[string][]string{
		"tool": {"chat": {"nope"}}, "kind": {"bogus": {"web_search"}},
	} {
		bad := st
		bad.DefaultTools = dt
		var e map[string]string
		if code := sendJSON(t, "PUT", url, bad, &e); code != 400 || e["error"] == "" {
			t.Errorf("%s: %d %v", name, code, e)
		}
	}
	// A new chat card is seeded; other kinds get their own entry, or nothing.
	if c := kindCard(t, app, root, KindChat, "hi"); !slices.Equal(c.Tools, []string{"web_search"}) {
		t.Errorf("chat tools = %v", c.Tools)
	}
	if c := kindCard(t, app, root, KindCode, "hi"); len(c.Tools) != 0 {
		t.Errorf("code tools = %v", c.Tools)
	}
	// Given tools win over the defaults, also an empty list.
	none, kind, text := []string{}, KindChat, "hi"
	c, err := app.CreateCard(cardInput{Description: &text, RootFolder: &root, Kind: &kind, Tools: &none})
	if err != nil || len(c.Tools) != 0 {
		t.Errorf("explicit empty tools: %v %v", c, err)
	}
	// Plan approval seeds the created cards from the settings too.
	plan := kindCard(t, app, root, KindPlan, "plan it")
	app.setStatus(plan.ID, StatusReview)
	plan, _ = app.update(plan.ID, func(c *Card) { c.PlanTasks = []PlanTask{{Title: "look", Description: "d", Kind: "research"}} })
	made, err := app.createPlanCards(plan, nil)
	if err != nil || len(made) != 1 || !slices.Equal(made[0].Tools, []string{"web_search"}) {
		t.Errorf("plan cards: %v %v", made, err)
	}
	// An old database without default_tools behaves as before.
	if old := defaultSettings(); old.DefaultTools != nil {
		t.Error("default settings carry tools")
	}
}

func TestToolboxEndpoint(t *testing.T) {
	app := newTestApp(t, testDir(t))
	var defs []ToolDef
	if code := sendJSON(t, "GET", app.baseURL+"/api/toolbox", nil, &defs); code != 200 {
		t.Fatalf("code %d", code)
	}
	byID := map[string]ToolDef{}
	for _, d := range defs {
		byID[d.ID] = d
	}
	if d := byID["web_search"]; d.Label == "" || d.Description == "" || !slices.Equal(d.Harnesses, []string{"claude", "opencode"}) {
		t.Errorf("web_search = %+v", d)
	}
	if d := byID["skills"]; !slices.Equal(d.Harnesses, []string{"claude"}) {
		t.Errorf("skills = %+v", d)
	}
}

func TestMigrationAddsTools(t *testing.T) {
	path := filepath.Join(testDir(t), "old.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	// A database from before the toolbox: the kinds migration is done, tools is missing.
	_, err = db.Exec(`CREATE TABLE cards (
		id INTEGER PRIMARY KEY AUTOINCREMENT, title TEXT NOT NULL, description TEXT NOT NULL DEFAULT '', root_folder TEXT NOT NULL DEFAULT '',
		restrict_refinement INTEGER NOT NULL DEFAULT 0, auto_run INTEGER NOT NULL DEFAULT 0, skip_refinement INTEGER NOT NULL DEFAULT 0,
		title_auto INTEGER NOT NULL DEFAULT 0, model TEXT NOT NULL DEFAULT '', effort TEXT NOT NULL DEFAULT '', permission_mode TEXT NOT NULL DEFAULT '',
		harness TEXT NOT NULL DEFAULT 'claude', kind TEXT NOT NULL DEFAULT 'code', plan_tasks TEXT NOT NULL DEFAULT '',
		max_turns INTEGER NOT NULL DEFAULT 0, max_minutes INTEGER NOT NULL DEFAULT 0,
		push_default TEXT NOT NULL DEFAULT '', refined_prompt TEXT NOT NULL DEFAULT '', refine_notes TEXT NOT NULL DEFAULT '',
		status TEXT NOT NULL DEFAULT 'todo', session_id TEXT NOT NULL DEFAULT '', summary TEXT NOT NULL DEFAULT '', fail_reason TEXT NOT NULL DEFAULT '',
		warning TEXT NOT NULL DEFAULT '', activity TEXT NOT NULL DEFAULT '', waiting_on INTEGER NOT NULL DEFAULT 0, turns_used INTEGER NOT NULL DEFAULT 0,
		cost_usd REAL NOT NULL DEFAULT 0, created_at TIMESTAMP NOT NULL, updated_at TIMESTAMP NOT NULL, project_id INTEGER NOT NULL DEFAULT 0);
		INSERT INTO cards (title, root_folder, created_at, updated_at) VALUES ('old', '/r', '2025-01-01 00:00:00', '2025-01-01 00:00:00')`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.GetCard(1)
	if err != nil || len(c.Tools) != 0 {
		t.Fatalf("migrated card: %v %v", c, err)
	}
	c.Tools = []string{"web_search", "skills"}
	if err := s.SaveCard(c); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetCard(1); !slices.Equal(got.Tools, []string{"web_search", "skills"}) {
		t.Errorf("tools round trip = %v", got.Tools)
	}
	if _, err := OpenStore(path); err != nil {
		t.Errorf("reopen: %v", err)
	}
}

func TestToolInstructions(t *testing.T) {
	app := &App{}
	chat := &Card{Kind: KindChat, Tools: []string{"web_search"}}
	got := app.sessionInstructions(chat, "")
	if !strings.Contains(got, "WebSearch") || strings.Contains(got, "You have no tools") || !strings.Contains(got, "cannot read or change any files") {
		t.Errorf("chat instructions = %s", got)
	}
	if got := app.sessionInstructions(&Card{Kind: KindChat}, ""); got != chatInstructions {
		t.Error("chat instructions changed without tools")
	}
	if got := app.sessionInstructions(&Card{Kind: KindChat, Harness: "opencode", Tools: []string{"web_fetch", "skills"}}, ""); !strings.Contains(got, "webfetch") || strings.Contains(got, "Skill") {
		t.Errorf("opencode chat instructions = %s", got)
	}
	if got := app.sessionInstructions(&Card{Kind: KindResearch, RootFolder: "/r", Tools: []string{"web_search"}}, ""); !strings.Contains(got, "WebSearch") {
		t.Errorf("research instructions = %s", got)
	}
	plain := app.sessionInstructions(&Card{Kind: KindResearch, RootFolder: "/r"}, "")
	if strings.Contains(plain, "WebSearch") || strings.Contains(plain, "Besides") {
		t.Errorf("research instructions without tools = %s", plain)
	}
}

func TestChatWithToolsEndsInNeedsInput(t *testing.T) {
	dir := testDir(t)
	app := newTestApp(t, dir)
	root := kindRoot(t, dir)
	yes, kind, text, tools := true, KindChat, "hello there", []string{"web_search", "web_fetch"}
	c, err := app.CreateCard(cardInput{Description: &text, RootFolder: &root, Kind: &kind, Tools: &tools, SkipRefinement: &yes})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Run(c.ID); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, app, c.ID, StatusNeedsInput)
	if in, _ := app.store.OpenInput(c.ID); in == nil || in.Kind != "chat" {
		t.Errorf("open input = %+v", in)
	}
}
