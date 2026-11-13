package board

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"maestrello/internal/model"
	"maestrello/internal/store"
	"maestrello/internal/testenv"
)

func boolp(b bool) *bool { return &b }

func strp(s string) *string { return &s }

func listp(s ...string) *[]string { return &s }

// noRefinement fails the test when the card went through a refinement session.
func noRefinement(t *testing.T, app *App, c *model.Card) {
	t.Helper()
	evs, _ := app.store.Events(c.ID, 500)
	for _, e := range evs {
		if e.Kind == "refine_result" {
			t.Errorf("card %d was refined: %s", c.ID, e.Data)
		}
	}
	if strings.Contains(c.RefinedPrompt, "Refined:") {
		t.Errorf("fake refinement output in the prompt: %q", c.RefinedPrompt)
	}
}

func TestSkipRefinementOnCreate(t *testing.T) {
	dir := testenv.Dir(t)
	app := newTestApp(t, dir)

	c, err := app.CreateCard(cardInput{Title: strp("Write docs"), Description: strp("Explain the board."), RootFolder: &dir, SkipRefinement: boolp(true)})
	if err != nil {
		t.Fatal(err)
	}
	if c.Status != model.StatusReady || !c.SkipRefinement {
		t.Fatalf("created card: status %s, skip %v", c.Status, c.SkipRefinement)
	}
	if c.RefinedPrompt != "Write docs\n\nExplain the board." {
		t.Errorf("prompt = %q, want title, blank line, description", c.RefinedPrompt)
	}
	if repos, _ := app.store.Repos(c.ID); len(repos) != 0 {
		t.Errorf("planned repos = %v, want none", repos)
	}
	if !hasNote(t, app, c.ID, "refinement skipped") {
		t.Error("no note about the skipped refinement")
	}
	noRefinement(t, app, c)

	// A card with no description is just its title.
	c, err = app.CreateCard(cardInput{Title: strp("Only a title"), RootFolder: &dir, SkipRefinement: boolp(true)})
	if err != nil || c.RefinedPrompt != "Only a title" {
		t.Errorf("title-only card: %+v, %v", c, err)
	}

	// Without the flag the card still waits in Todo.
	c, err = app.CreateCard(cardInput{Title: strp("Plain"), RootFolder: &dir})
	if err != nil || c.Status != model.StatusTodo || c.SkipRefinement {
		t.Errorf("plain card: %+v, %v", c, err)
	}
}

func TestSkipRefinementDefaultSetting(t *testing.T) {
	dir := testenv.Dir(t)
	app := newTestApp(t, dir)
	st, _ := app.store.Settings()
	if st.DefaultSkipRefinement {
		t.Fatal("default_skip_refinement should be off")
	}
	st.DefaultSkipRefinement = true
	if err := app.store.SaveSettings(st); err != nil {
		t.Fatal(err)
	}
	c, err := app.CreateCard(cardInput{Title: strp("Seeded"), RootFolder: &dir})
	if err != nil || c.Status != model.StatusReady || !c.SkipRefinement {
		t.Fatalf("seeded card: %+v, %v", c, err)
	}
	// The card can opt out again.
	c, err = app.CreateCard(cardInput{Title: strp("Opted out"), RootFolder: &dir, SkipRefinement: boolp(false)})
	if err != nil || c.Status != model.StatusTodo || c.SkipRefinement {
		t.Errorf("opted-out card: %+v, %v", c, err)
	}
}

func TestSkipRefinementAutoRun(t *testing.T) {
	dir, root := plainRoot(t)
	app := newTestApp(t, dir)
	c, err := app.CreateCard(cardInput{Title: strp("Edit in place"), Description: strp("LOCAL"), RootFolder: &root,
		SkipRefinement: boolp(true), AutoRun: boolp(true)})
	if err != nil {
		t.Fatal(err)
	}
	c = waitStatus(t, app, c.ID, model.StatusReview)
	waitIdle(t, app, c.ID)
	noRefinement(t, app, c)
	if _, err := os.Stat(filepath.Join(root, "LOCAL.txt")); err != nil {
		t.Error("the agent did not run in the plain folder:", err)
	}
	if !hasNote(t, app, c.ID, "refinement skipped; starting automatically") {
		t.Error("no auto-run note")
	}
}

func TestSkipRefinementLaterInTodo(t *testing.T) {
	dir := testenv.Dir(t)
	app := newTestApp(t, dir)
	c := newCard(t, app, dir, "do the thing")
	app.store.SetPlannedRepos(c.ID, []string{"stale"})

	got, err := app.EditCard(c.ID, cardInput{SkipRefinement: boolp(true), RefinedPrompt: strp("   ")})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != model.StatusReady || got.RefinedPrompt != "Test card\n\ndo the thing" {
		t.Errorf("edited card: %s %q", got.Status, got.RefinedPrompt)
	}
	if repos, _ := app.store.Repos(c.ID); len(repos) != 0 {
		t.Errorf("planned repos = %v, want none", repos)
	}

	// On a Ready card the flag cannot change.
	other := readyCard(t, app, dir, "refined for real")
	got, err = app.EditCard(other.ID, cardInput{SkipRefinement: boolp(true)})
	if err != nil || got.SkipRefinement || got.RefinedPrompt != other.RefinedPrompt {
		t.Errorf("ready card after edit: %+v, %v", got, err)
	}

	// Asking for refinement afterwards clears the flag, since the prompt is a refined one then.
	if err := app.Refine(c.ID); err != nil {
		t.Fatal(err)
	}
	got = waitStatus(t, app, c.ID, model.StatusReady)
	if got.SkipRefinement || !strings.Contains(got.RefinedPrompt, "Refined:") {
		t.Errorf("refined card: skip %v, prompt %q", got.SkipRefinement, got.RefinedPrompt)
	}
}

// A Todo card that has the flag but was not promoted (say, dragged back to Todo) needs no refined prompt to move on.
func flaggedTodo(t *testing.T, app *App, root, desc string) *model.Card {
	t.Helper()
	c := newCard(t, app, root, desc)
	c.SkipRefinement = true
	if err := app.store.SaveCard(c); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestSkipRefinementMoveAndRun(t *testing.T) {
	dir, root := plainRoot(t)
	app := newTestApp(t, dir)

	// Drag to Ready.
	c := flaggedTodo(t, app, root, "ready please")
	if err := app.Move(c.ID, model.StatusReady); err != nil {
		t.Fatal(err)
	}
	got, _ := app.store.GetCard(c.ID)
	if got.Status != model.StatusReady || got.RefinedPrompt != "Test card\n\nready please" {
		t.Errorf("moved to ready: %s %q", got.Status, got.RefinedPrompt)
	}

	// Drag to Running.
	c = flaggedTodo(t, app, root, "LOCAL")
	if err := app.Move(c.ID, model.StatusRunning); err != nil {
		t.Fatal(err)
	}
	c = waitStatus(t, app, c.ID, model.StatusReview)
	waitIdle(t, app, c.ID)
	noRefinement(t, app, c)
	if c.RefinedPrompt != "Test card\n\nLOCAL" {
		t.Errorf("prompt = %q", c.RefinedPrompt)
	}

	// Run on the Todo card, keeping a prompt it already has.
	c = flaggedTodo(t, app, root, "ignored text")
	app.update(c.ID, func(c *model.Card) { c.RefinedPrompt = "my own prompt" })
	if err := app.Run(c.ID); err != nil {
		t.Fatal(err)
	}
	c = waitStatus(t, app, c.ID, model.StatusReview)
	if c.RefinedPrompt != "my own prompt" {
		t.Errorf("prompt = %q, want the one it had", c.RefinedPrompt)
	}
}

func TestSkipRefinementStartAll(t *testing.T) {
	dir, root := plainRoot(t)
	app := newTestApp(t, dir)
	flagged := flaggedTodo(t, app, root, "start me")
	plain := newCard(t, app, root, "stay in todo")

	resp, err := http.Post(app.baseURL+"/api/cards/run?status=ready", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Started int               `json:"started"`
		Errors  map[string]string `json:"errors"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	resp.Body.Close()
	if out.Started != 1 || len(out.Errors) != 0 {
		t.Errorf("start all = %+v", out)
	}
	waitStatus(t, app, flagged.ID, model.StatusReview)
	if got, _ := app.store.GetCard(plain.ID); got.Status != model.StatusTodo {
		t.Errorf("card without the flag is %s, want todo", got.Status)
	}
}

func TestNoFlagMovesWithoutRefinedPrompt(t *testing.T) {
	dir := testenv.Dir(t)
	app := newTestApp(t, dir)
	c := newCard(t, app, dir, "")
	if err := app.Run(c.ID); err == nil {
		t.Error("Run on a Todo card should be refused: only ready cards run")
	}
	if err := app.Move(c.ID, model.StatusReady); err != nil {
		t.Fatalf("move to ready: %v", err)
	}
	if got, _ := app.store.GetCard(c.ID); got.Status != model.StatusReady || got.RefinedPrompt != cardText(got) {
		t.Errorf("card = %s %q, want ready with the card text as prompt", got.Status, got.RefinedPrompt)
	}
}

func TestBoardMCPCreateWithPrompt(t *testing.T) {
	_, root, app := repoFixture(t)

	text, isErr := boardCall(t, app, "create_card", map[string]any{
		"title": "Handed over", "description": "short", "root_folder": root,
		"refined_prompt": "Do exactly this, in svc/a.", "repos": []string{"svc/a", "/svc/b/", "."},
	})
	if isErr {
		t.Fatal(text)
	}
	var c model.Card
	json.Unmarshal([]byte(text), &c)
	got, _ := app.store.GetCard(c.ID)
	if got.Status != model.StatusReady || got.RefinedPrompt != "Do exactly this, in svc/a." || !got.SkipRefinement {
		t.Errorf("card = %s %q skip %v", got.Status, got.RefinedPrompt, got.SkipRefinement)
	}
	var paths []string
	repos, _ := app.store.Repos(c.ID)
	for _, r := range repos {
		paths = append(paths, r.RepoPath)
	}
	if strings.Join(paths, ",") != ".,svc/a,svc/b" {
		t.Errorf("repos = %v", paths)
	}
	noRefinement(t, app, got)

	// skip_refinement alone uses the card text.
	text, isErr = boardCall(t, app, "create_card", map[string]any{"title": "Just skip", "description": "text", "root_folder": root, "skip_refinement": true})
	json.Unmarshal([]byte(text), &c)
	if isErr || c.Status != model.StatusReady || c.RefinedPrompt != "Just skip\n\ntext" {
		t.Errorf("skip only: %v %s", isErr, text)
	}

	// An unknown repo is refused and no card is left behind.
	before, _ := app.store.ListCards()
	for _, bad := range []string{"nope", "../outside", "svc/c"} {
		if text, isErr := boardCall(t, app, "create_card", map[string]any{
			"title": "Bad repo", "root_folder": root, "refined_prompt": "x", "repos": []string{bad}}); !isErr {
			t.Errorf("repo %q accepted: %s", bad, text)
		}
	}
	if after, _ := app.store.ListCards(); len(after) != len(before) {
		t.Errorf("%d cards after refused creates, want %d", len(after), len(before))
	}

	// The tool descriptions tell a client it can skip refinement.
	var names []string
	for _, tl := range boardTools {
		if tl["name"] == "create_card" {
			props := tl["inputSchema"].(map[string]any)["properties"].(map[string]any)
			for _, k := range []string{"skip_refinement", "refined_prompt", "repos"} {
				if props[k] == nil {
					names = append(names, k)
				}
			}
			if !strings.Contains(tl["description"].(string), "skip") {
				t.Error("create_card description does not mention skipping refinement")
			}
		}
	}
	if len(names) > 0 {
		t.Errorf("create_card lacks properties %v", names)
	}
}

func TestSkipRefinementAPI(t *testing.T) {
	dir := testenv.Dir(t)
	app := newTestApp(t, dir)
	body, _ := json.Marshal(map[string]any{"title": "Via API", "description": "d", "root_folder": dir, "skip_refinement": true})
	resp, err := http.Post(app.baseURL+"/api/cards", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	var c model.Card
	json.NewDecoder(resp.Body).Decode(&c)
	resp.Body.Close()
	if resp.StatusCode != 201 || c.Status != model.StatusReady || !c.SkipRefinement {
		t.Errorf("POST /api/cards: %d %+v", resp.StatusCode, c)
	}
}

// A database from before skip_refinement existed opens, and its cards read back with the flag off.
func TestOpenStoreWithoutSkipColumn(t *testing.T) {
	path := filepath.Join(testenv.Dir(t), "old.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE cards (
		id INTEGER PRIMARY KEY AUTOINCREMENT, title TEXT NOT NULL, description TEXT NOT NULL DEFAULT '',
		root_folder TEXT NOT NULL DEFAULT '', restrict_refinement INTEGER NOT NULL DEFAULT 0, auto_run INTEGER NOT NULL DEFAULT 0,
		model TEXT NOT NULL DEFAULT '', effort TEXT NOT NULL DEFAULT '', permission_mode TEXT NOT NULL DEFAULT '',
		harness TEXT NOT NULL DEFAULT 'claude', max_turns INTEGER NOT NULL DEFAULT 0, max_minutes INTEGER NOT NULL DEFAULT 0,
		push_default TEXT NOT NULL DEFAULT '', refined_prompt TEXT NOT NULL DEFAULT '', refine_notes TEXT NOT NULL DEFAULT '',
		status TEXT NOT NULL DEFAULT 'todo', session_id TEXT NOT NULL DEFAULT '', summary TEXT NOT NULL DEFAULT '',
		fail_reason TEXT NOT NULL DEFAULT '', warning TEXT NOT NULL DEFAULT '', activity TEXT NOT NULL DEFAULT '',
		waiting_on INTEGER NOT NULL DEFAULT 0, turns_used INTEGER NOT NULL DEFAULT 0, cost_usd REAL NOT NULL DEFAULT 0,
		created_at TIMESTAMP NOT NULL, updated_at TIMESTAMP NOT NULL)`)
	if err == nil {
		_, err = db.Exec(`INSERT INTO cards (title, status, created_at, updated_at) VALUES ('old card', 'ready', '2025-01-01T00:00:00Z', '2025-01-01T00:00:00Z')`)
	}
	if err != nil {
		t.Fatal(err)
	}
	db.Close()

	s, err := store.OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	cards, err := s.ListCards()
	if err != nil || len(cards) != 1 || cards[0].Title != "old card" || cards[0].SkipRefinement {
		t.Fatalf("old card: %+v, %v", cards, err)
	}
	cards[0].SkipRefinement = true
	if err := s.SaveCard(cards[0]); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetCard(cards[0].ID); !got.SkipRefinement {
		t.Error("flag not saved on the migrated database")
	}
}
