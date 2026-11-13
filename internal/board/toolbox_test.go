package board

import (
	"database/sql"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"maestrello/internal/model"
	"maestrello/internal/store"
	"maestrello/internal/testenv"
)

func TestOpencodeSkippedToolNote(t *testing.T) {
	dir := testenv.Dir(t)
	app := newTestApp(t, dir)
	root := kindRoot(t, dir)
	yes, h, kind, tools := true, "opencode", model.KindChat, []string{"web_search", "skills"}
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
	dir := testenv.Dir(t)
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
	var got model.Card
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
	dir := testenv.Dir(t)
	app := newTestApp(t, dir)
	root := kindRoot(t, dir)
	url := app.baseURL + "/api/settings"
	st := model.DefaultSettings()
	st.DefaultRootFolder = root
	st.DefaultTools = map[string][]string{"chat": {"web_search", "web_search"}, "research": {"web_search"}}
	var saved model.Settings
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
	if c := kindCard(t, app, root, model.KindChat, "hi"); !slices.Equal(c.Tools, []string{"web_search"}) {
		t.Errorf("chat tools = %v", c.Tools)
	}
	if c := kindCard(t, app, root, model.KindCode, "hi"); len(c.Tools) != 0 {
		t.Errorf("code tools = %v", c.Tools)
	}
	// Given tools win over the defaults, also an empty list.
	none, kind, text := []string{}, model.KindChat, "hi"
	c, err := app.CreateCard(cardInput{Description: &text, RootFolder: &root, Kind: &kind, Tools: &none})
	if err != nil || len(c.Tools) != 0 {
		t.Errorf("explicit empty tools: %v %v", c, err)
	}
	// Plan approval seeds the created cards from the settings too.
	plan := kindCard(t, app, root, model.KindPlan, "plan it")
	app.setStatus(plan.ID, model.StatusReview)
	plan, _ = app.update(plan.ID, func(c *model.Card) {
		c.PlanTasks = []model.PlanTask{{Title: "look", Description: "d", Kind: "research"}}
	})
	made, err := app.createPlanCards(plan, nil)
	if err != nil || len(made) != 1 || !slices.Equal(made[0].Tools, []string{"web_search"}) {
		t.Errorf("plan cards: %v %v", made, err)
	}
	// An old database without default_tools behaves as before.
	if old := model.DefaultSettings(); old.DefaultTools != nil {
		t.Error("default settings carry tools")
	}
}

func TestToolboxEndpoint(t *testing.T) {
	app := newTestApp(t, testenv.Dir(t))
	var defs []model.ToolDef
	if code := sendJSON(t, "GET", app.baseURL+"/api/toolbox", nil, &defs); code != 200 {
		t.Fatalf("code %d", code)
	}
	byID := map[string]model.ToolDef{}
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
	path := filepath.Join(testenv.Dir(t), "old.db")
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
	s, err := store.OpenStore(path)
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
	if _, err := store.OpenStore(path); err != nil {
		t.Errorf("reopen: %v", err)
	}
}

func TestToolInstructions(t *testing.T) {
	app := &App{}
	chat := &model.Card{Kind: model.KindChat, Tools: []string{"web_search"}}
	got := app.sessionInstructions(chat, "")
	if !strings.Contains(got, "WebSearch") || strings.Contains(got, "You have no tools") || !strings.Contains(got, "cannot read or change any files") {
		t.Errorf("chat instructions = %s", got)
	}
	if got := app.sessionInstructions(&model.Card{Kind: model.KindChat}, ""); got != chatInstructions {
		t.Error("chat instructions changed without tools")
	}
	if got := app.sessionInstructions(&model.Card{Kind: model.KindChat, Harness: "opencode", Tools: []string{"web_fetch", "skills"}}, ""); !strings.Contains(got, "webfetch") || strings.Contains(got, "Skill") {
		t.Errorf("opencode chat instructions = %s", got)
	}
	if got := app.sessionInstructions(&model.Card{Kind: model.KindResearch, RootFolder: "/r", Tools: []string{"web_search"}}, ""); !strings.Contains(got, "WebSearch") {
		t.Errorf("research instructions = %s", got)
	}
	plain := app.sessionInstructions(&model.Card{Kind: model.KindResearch, RootFolder: "/r"}, "")
	if strings.Contains(plain, "WebSearch") || strings.Contains(plain, "Besides") {
		t.Errorf("research instructions without tools = %s", plain)
	}
}

func TestChatWithToolsEndsInNeedsInput(t *testing.T) {
	dir := testenv.Dir(t)
	app := newTestApp(t, dir)
	root := kindRoot(t, dir)
	yes, kind, text, tools := true, model.KindChat, "hello there", []string{"web_search", "web_fetch"}
	c, err := app.CreateCard(cardInput{Description: &text, RootFolder: &root, Kind: &kind, Tools: &tools, SkipRefinement: &yes})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Run(c.ID); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, app, c.ID, model.StatusNeedsInput)
	if in, _ := app.store.OpenInput(c.ID); in == nil || in.Kind != "chat" {
		t.Errorf("open input = %+v", in)
	}
}
