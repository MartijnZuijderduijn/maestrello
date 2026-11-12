package main

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// kindCard creates a card of a kind through CreateCard (so it has a project), skipping refinement,
// and returns it in Ready.
func kindCard(t *testing.T, app *App, root, kind, text string) *Card {
	t.Helper()
	yes := true
	c, err := app.CreateCard(cardInput{Description: &text, RootFolder: &root, Kind: &kind, SkipRefinement: &yes})
	if err != nil {
		t.Fatal(err)
	}
	if c.Status != StatusReady || c.Kind != kind {
		t.Fatalf("new %s card: status %q kind %q", kind, c.Status, c.Kind)
	}
	return c
}

func kindRoot(t *testing.T, dir string) string {
	t.Helper()
	root := filepath.Join(dir, "project")
	os.MkdirAll(root, 0o755)
	os.WriteFile(filepath.Join(root, "notes.txt"), []byte("hello\n"), 0o644)
	return root
}

func hasEvent(t *testing.T, app *App, id int64, substr string) bool {
	t.Helper()
	evs, _ := app.store.Events(id, 500)
	for _, e := range evs {
		if strings.Contains(string(e.Data), substr) {
			return true
		}
	}
	return false
}

func TestUnknownKindRejected(t *testing.T) {
	dir := testDir(t)
	app := newTestApp(t, dir)
	root := kindRoot(t, dir)

	var out map[string]string
	code := sendJSON(t, "POST", app.baseURL+"/api/cards", map[string]any{"description": "x", "root_folder": root, "kind": "bogus"}, &out)
	if code != 400 || out["error"] != "invalid kind" {
		t.Errorf("HTTP create: %d %v", code, out)
	}
	text, isErr := boardCall(t, app, "create_card", map[string]any{"description": "x", "root_folder": root, "kind": "bogus"})
	if !isErr || !strings.Contains(text, "invalid kind") {
		t.Errorf("MCP create: %q %v", text, isErr)
	}
	c := newCard(t, app, root, "x")
	code = sendJSON(t, "PATCH", app.baseURL+"/api/cards/"+idStr(c.ID), map[string]any{"kind": "bogus"}, &out)
	if code != 400 {
		t.Errorf("HTTP patch = %d", code)
	}
	if text, isErr := boardCall(t, app, "update_card", map[string]any{"id": c.ID, "kind": "bogus"}); !isErr {
		t.Errorf("MCP update accepted a bad kind: %s", text)
	}
	if got, _ := app.store.GetCard(c.ID); got.Kind != KindCode {
		t.Errorf("kind changed to %q", got.Kind)
	}
	// A valid kind goes through, in Todo or Ready only.
	text, isErr = boardCall(t, app, "update_card", map[string]any{"id": c.ID, "kind": "research"})
	if isErr {
		t.Fatalf("update kind: %s", text)
	}
	app.setStatus(c.ID, StatusReview)
	if _, isErr := boardCall(t, app, "update_card", map[string]any{"id": c.ID, "kind": "plan"}); !isErr {
		t.Error("kind edited in Review")
	}
}

func idStr(n int64) string { b, _ := json.Marshal(n); return string(b) }

func TestMigrationAddsKind(t *testing.T) {
	path := filepath.Join(testDir(t), "old.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	// A database from before kinds existed: the cards table without kind and plan_tasks.
	_, err = db.Exec(`CREATE TABLE cards (
		id INTEGER PRIMARY KEY AUTOINCREMENT, title TEXT NOT NULL, description TEXT NOT NULL DEFAULT '', root_folder TEXT NOT NULL DEFAULT '',
		restrict_refinement INTEGER NOT NULL DEFAULT 0, auto_run INTEGER NOT NULL DEFAULT 0, skip_refinement INTEGER NOT NULL DEFAULT 0,
		title_auto INTEGER NOT NULL DEFAULT 0, model TEXT NOT NULL DEFAULT '', effort TEXT NOT NULL DEFAULT '', permission_mode TEXT NOT NULL DEFAULT '',
		harness TEXT NOT NULL DEFAULT 'claude', max_turns INTEGER NOT NULL DEFAULT 0, max_minutes INTEGER NOT NULL DEFAULT 0,
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
	if err != nil {
		t.Fatal(err)
	}
	if c.Kind != KindCode || len(c.PlanTasks) != 0 {
		t.Errorf("migrated card: kind %q, tasks %v", c.Kind, c.PlanTasks)
	}
	if _, err := OpenStore(path); err != nil { // migrating twice is fine
		t.Errorf("reopen: %v", err)
	}
}

func TestClaudeSessionCommandKinds(t *testing.T) {
	codeArgs, _, _ := sessionArgs(t, testSpec()) // no kind: a code card, as before kinds existed
	if slices.Contains(codeArgs, "--tools") {
		t.Errorf("code card restricts tools: %v", codeArgs)
	}
	spec := testSpec()
	spec.Card.Kind = KindCode
	args, _, _ := sessionArgs(t, spec)
	args = slices.Clone(args)
	i := slices.Index(args, "--mcp-config") + 1
	args[i] = codeArgs[i] // each call writes its config in a scratch folder of its own
	if !slices.Equal(args, codeArgs) {
		t.Errorf("explicit code kind changes the args:\n%v\n%v", args, codeArgs)
	}

	for _, kind := range []string{KindResearch, KindPlan} {
		spec := testSpec()
		spec.Card.Kind = kind
		args, _, _ := sessionArgs(t, spec)
		if v, ok := flagValue(args, "--tools"); !ok || v != "Read,Grep,Glob" {
			t.Errorf("%s: --tools = %q, %v", kind, v, ok)
		}
		allowed, _ := flagValue(args, "--allowedTools")
		if slices.Contains(strings.Split(allowed, ","), "mcp__maestrello__add_repo") {
			t.Errorf("%s allows add_repo: %s", kind, allowed)
		}
		if got := slices.Contains(strings.Split(allowed, ","), "mcp__maestrello__report_plan"); got != (kind == KindPlan) {
			t.Errorf("%s: report_plan allowed = %v (%s)", kind, got, allowed)
		}
		if !strings.Contains(allowed, "mcp__maestrello__ask_user") || !strings.Contains(allowed, "mcp__maestrello__report_failed") {
			t.Errorf("%s: allowed = %s", kind, allowed)
		}
		if got := strings.Contains(allowed, "mcp__maestrello__report_done"); got != (kind == KindResearch) {
			t.Errorf("%s: report_done allowed = %v", kind, got)
		}
	}

	spec = testSpec()
	spec.Card.Kind = KindChat
	args, _, _ = sessionArgs(t, spec)
	if v, ok := flagValue(args, "--tools"); !ok || v != "" {
		t.Errorf("chat: --tools = %q, %v", v, ok)
	}
	allowed, _ := flagValue(args, "--allowedTools")
	if strings.Contains(allowed, "add_repo") || strings.Contains(allowed, "report_plan") {
		t.Errorf("chat allowed = %s", allowed)
	}
}

func TestResearchCard(t *testing.T) {
	dir := testDir(t)
	app := newTestApp(t, dir)
	root := kindRoot(t, dir) // a plain folder: a code card would work in it in place, with a lease
	c := kindCard(t, app, root, KindResearch, "CWD look around")

	if err := app.Run(c.ID); err != nil {
		t.Fatal(err)
	}
	c = waitStatus(t, app, c.ID, StatusReview)
	if c.Summary != "fake work done" || c.Warning != "" {
		t.Errorf("card after run: summary %q warning %q", c.Summary, c.Warning)
	}
	if !hasEvent(t, app, c.ID, "cwd: "+root) {
		t.Error("the session did not run in the root folder")
	}
	repos, _ := app.store.Repos(c.ID)
	if len(repos) != 0 {
		t.Errorf("research card has repos: %v", repos[0])
	}
	if entries, _ := os.ReadDir(app.leaseDir()); len(entries) != 0 {
		t.Errorf("research card holds leases: %v", entries)
	}
	if entries, _ := os.ReadDir(app.workDir(c.ID)); len(entries) != 0 {
		t.Errorf("research card has a workspace: %v", entries)
	}
	if hasEvent(t, app, c.ID, "checked out") || hasEvent(t, app, c.ID, "local work") {
		t.Error("research card checked something out")
	}

	if err := app.Approve(c.ID, nil); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, app, c.ID, StatusDone)
	if err := app.ApproveSelected(c.ID, nil, []int{0}); err == nil {
		t.Error("selecting tasks on a research card should fail")
	}
}

func TestResearchRunsInPlainFolderWithoutPlanningDot(t *testing.T) {
	dir := testDir(t)
	app := newTestApp(t, dir)
	c := kindCard(t, app, kindRoot(t, dir), KindResearch, "go")
	app.Run(c.ID)
	waitStatus(t, app, c.ID, StatusReview)
	if hasEvent(t, app, c.ID, "working there in place") {
		t.Error("research card planned the root folder for editing")
	}
}

func TestPlanCard(t *testing.T) {
	dir := testDir(t)
	app := newTestApp(t, dir)
	root := kindRoot(t, dir)
	c := kindCard(t, app, root, KindPlan, "PLAN the big thing")
	if err := app.Run(c.ID); err != nil {
		t.Fatal(err)
	}
	c = waitStatus(t, app, c.ID, StatusReview)
	if !strings.Contains(c.Summary, "Fake plan") || len(c.PlanTasks) != 2 {
		t.Fatalf("plan: %q tasks %v", c.Summary, c.PlanTasks)
	}
	if c.PlanTasks[0].Kind != KindCode || c.PlanTasks[1].Kind != KindResearch {
		t.Errorf("task kinds = %q, %q", c.PlanTasks[0].Kind, c.PlanTasks[1].Kind)
	}
	// The agent never creates cards: only the plan card exists until I approve.
	if all, _ := app.store.ListCards(); len(all) != 1 {
		t.Fatalf("%d cards before approval", len(all))
	}

	if err := app.ApproveSelected(c.ID, nil, []int{5}); err == nil {
		t.Error("an out-of-range task was accepted")
	}
	if all, _ := app.store.ListCards(); len(all) != 1 {
		t.Fatalf("a refused approval created cards: %d", len(all))
	}
	if err := app.ApproveSelected(c.ID, nil, []int{1}); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, app, c.ID, StatusDone)
	all, _ := app.store.ListCards()
	if len(all) != 2 {
		t.Fatalf("%d cards after approving one task", len(all))
	}
	n := all[1]
	if n.Title != "Second fake task" || n.Description != "Do the second part." || n.Kind != KindResearch ||
		n.Status != StatusTodo || n.ProjectID != c.ProjectID || n.RootFolder != root {
		t.Errorf("new card: %+v", n)
	}
	if !hasEvent(t, app, n.ID, "created from plan #"+idStr(c.ID)) {
		t.Error("new card has no note naming the plan")
	}
	if !hasEvent(t, app, c.ID, "created #"+idStr(n.ID)) {
		t.Error("plan card has no note listing the new card")
	}
}

func TestPlanApproveAllByDefaultAndNone(t *testing.T) {
	dir := testDir(t)
	app := newTestApp(t, dir)
	root := kindRoot(t, dir)
	c := kindCard(t, app, root, KindPlan, "PLAN a")
	app.Run(c.ID)
	waitStatus(t, app, c.ID, StatusReview)
	if err := app.Approve(c.ID, nil); err != nil {
		t.Fatal(err)
	}
	if all, _ := app.store.ListCards(); len(all) != 3 || all[1].Kind != KindCode || all[1].Status != StatusTodo {
		t.Errorf("approving without a selection should create every task: %d cards", len(all))
	}

	c2 := kindCard(t, app, root, KindPlan, "PLAN b")
	app.Run(c2.ID)
	waitStatus(t, app, c2.ID, StatusReview)
	if err := app.ApproveSelected(c2.ID, nil, []int{}); err != nil {
		t.Fatal(err)
	}
	if all, _ := app.store.ListCards(); len(all) != 4 {
		t.Errorf("an empty selection created cards: %d", len(all))
	}
}

func TestPlanApproveViaMCP(t *testing.T) {
	dir := testDir(t)
	app := newTestApp(t, dir)
	c := kindCard(t, app, kindRoot(t, dir), KindPlan, "PLAN c")
	app.Run(c.ID)
	waitStatus(t, app, c.ID, StatusReview)
	text, isErr := boardCall(t, app, "approve_card", map[string]any{"id": c.ID, "tasks": []int{0}})
	if isErr {
		t.Fatal(text)
	}
	all, _ := app.store.ListCards()
	if len(all) != 2 || all[1].Title != "First fake task" {
		t.Errorf("cards after MCP approval: %d", len(all))
	}
	if !hasEvent(t, app, c.ID, "approved by you via MCP") {
		t.Error("no MCP origin note")
	}
}

func TestReportPlanAndAddRepoRefusals(t *testing.T) {
	dir := testDir(t)
	app := newTestApp(t, dir)
	root := kindRoot(t, dir)
	code := newCard(t, app, root, "x")
	plan := kindCard(t, app, root, KindPlan, "x")

	args := map[string]any{"name": "report_plan", "arguments": map[string]any{"plan": "p", "tasks": []map[string]any{{"title": "t", "description": "d"}}}}
	_, out := rpc(t, app, code.ID, "tools/call", args)
	if text, isErr := toolText(t, out); !isErr || !strings.Contains(text, "only for plan cards") {
		t.Errorf("report_plan on a code card: %q %v", text, isErr)
	}
	if got, _ := app.store.GetCard(code.ID); got.Status != StatusTodo || len(got.PlanTasks) != 0 {
		t.Errorf("refused report_plan changed the card: %+v", got)
	}
	// Bad tasks are tool errors the plan agent can fix.
	for name, tasks := range map[string]any{
		"none": []any{}, "no title": []map[string]any{{"description": "d"}},
		"chat task": []map[string]any{{"title": "t", "kind": "chat"}}, "unknown repo": []map[string]any{{"title": "t", "repos": []string{"nope"}}},
	} {
		_, out := rpc(t, app, plan.ID, "tools/call", map[string]any{"name": "report_plan", "arguments": map[string]any{"plan": "p", "tasks": tasks}})
		if text, isErr := toolText(t, out); !isErr {
			t.Errorf("%s: accepted: %s", name, text)
		}
	}
	if got, _ := app.store.GetCard(plan.ID); got.Status != StatusReady {
		t.Errorf("a refused plan moved the card to %s", got.Status)
	}

	for _, kind := range []string{KindResearch, KindPlan, KindChat} {
		c := kindCard(t, app, root, kind, "x")
		_, out := rpc(t, app, c.ID, "tools/call", map[string]any{"name": "add_repo", "arguments": map[string]any{"path": "."}})
		if text, isErr := toolText(t, out); !isErr || !strings.Contains(text, "not available on "+kind) {
			t.Errorf("add_repo on %s: %q %v", kind, text, isErr)
		}
		if repos, _ := app.store.Repos(c.ID); len(repos) != 0 {
			t.Errorf("%s: add_repo left a repo", kind)
		}
		_, out = rpc(t, app, c.ID, "tools/list", nil)
		names := toolNames(out)
		if contains(names, "add_repo") || contains(names, "report_plan") != (kind == KindPlan) || contains(names, "report_done") != (kind == KindResearch) {
			t.Errorf("%s tools = %v", kind, names)
		}
	}
	_, out = rpc(t, app, code.ID, "tools/list", nil)
	if names := toolNames(out); contains(names, "report_plan") || !contains(names, "add_repo") || !contains(names, "report_done") {
		t.Errorf("code tools = %v", names)
	}
}

func TestChatCard(t *testing.T) {
	dir := testDir(t)
	app := newTestApp(t, dir)
	c := kindCard(t, app, kindRoot(t, dir), KindChat, "hello there")
	if !c.SkipRefinement || c.RefinedPrompt != "hello there" {
		t.Errorf("chat card should use its text as the prompt: %+v", c)
	}
	if err := app.Refine(c.ID); err == nil {
		t.Error("a chat card was refined")
	}
	if got, _ := app.store.GetCard(c.ID); got.Status != StatusReady {
		t.Errorf("refusing to refine moved the card to %s", got.Status)
	}

	if err := app.Run(c.ID); err != nil {
		t.Fatal(err)
	}
	c = waitStatus(t, app, c.ID, StatusNeedsInput)
	in, _ := app.store.OpenInput(c.ID)
	if in == nil || in.Kind != "chat" {
		t.Fatalf("open input = %+v", in)
	}
	var p struct{ Text string }
	json.Unmarshal(in.Payload, &p)
	if !strings.Contains(p.Text, "Fake chat reply to: hello there") || strings.Contains(p.Text, "resumed") {
		t.Errorf("reply = %q", p.Text)
	}
	if repos, _ := app.store.Repos(c.ID); len(repos) != 0 {
		t.Error("chat card has repos")
	}

	// Answering resumes the same session.
	if err := app.Answer(c.ID, in.ID, "and then?", false); err != nil {
		t.Fatal(err)
	}
	c2 := waitStatus(t, app, c.ID, StatusNeedsInput)
	if c2.SessionID != c.SessionID {
		t.Error("the session changed")
	}
	in2, _ := app.store.OpenInput(c.ID)
	if in2 == nil || in2.ID == in.ID || !strings.Contains(string(in2.Payload), "(resumed) to: and then?") {
		t.Errorf("second reply = %+v", in2)
	}

	// Closing the chat moves it to Done and answers the open input.
	if err := app.Move(c.ID, StatusDone); err != nil {
		t.Fatal(err)
	}
	got, _ := app.store.GetCard(c.ID)
	if got.Status != StatusDone {
		t.Errorf("status = %s", got.Status)
	}
	if open, _ := app.store.OpenInput(c.ID); open != nil {
		t.Error("input still open after closing the chat")
	}
}

func TestChatFailureIsStillAFailure(t *testing.T) {
	dir := testDir(t)
	app := newTestApp(t, dir)
	c := kindCard(t, app, kindRoot(t, dir), KindChat, "FAIL now")
	app.Run(c.ID)
	waitStatus(t, app, c.ID, StatusFailed)
}

func TestChatHidesRefinementOnEdit(t *testing.T) {
	dir := testDir(t)
	app := newTestApp(t, dir)
	root := kindRoot(t, dir)
	c := newCard(t, app, root, "talk to me")
	kind := KindChat
	got, err := app.EditCard(c.ID, cardInput{Kind: &kind})
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != KindChat || got.Status != StatusReady || !got.SkipRefinement {
		t.Errorf("a Todo card edited into a chat should go straight to Ready: %+v", got)
	}
}
