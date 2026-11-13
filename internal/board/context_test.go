package board

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"maestrello/internal/model"
	"maestrello/internal/store"
	"maestrello/internal/testenv"
)

// ctxFixture is an app plus a folder and a file inside its sandbox.
func ctxFixture(t *testing.T) (app *App, dir, file string) {
	t.Helper()
	root := testenv.Dir(t)
	app = newTestApp(t, root)
	dir = filepath.Join(root, "docs")
	file = filepath.Join(root, "notes.txt")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	return app, dir, file
}

func ctxCard(t *testing.T, app *App, paths []string) *model.Card {
	t.Helper()
	c, err := app.CreateCard(cardInput{Title: ptr("c"), RootFolder: ptr(app.guard.Root()), Context: &paths})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func ptr[T any](v T) *T { return &v }

func TestContextCreateAndTypes(t *testing.T) {
	app, dir, file := ctxFixture(t)
	c := ctxCard(t, app, []string{dir, " " + file + " ", dir + "/", file})
	want := []model.ContextItem{{Path: dir, Type: "dir"}, {Path: file, Type: "file"}} // duplicates dropped, types recorded
	if len(c.Context) != 2 || c.Context[0] != want[0] || c.Context[1] != want[1] {
		t.Fatalf("context = %v, want %v", c.Context, want)
	}
	got, _ := app.store.GetCard(c.ID)
	if len(got.Context) != 2 || got.Context[1] != want[1] {
		t.Errorf("stored context = %v", got.Context)
	}
}

func TestContextAlwaysArray(t *testing.T) {
	app, _, _ := ctxFixture(t)
	c := newCard(t, app, app.guard.Root(), "") // created straight through the store
	data, _ := json.Marshal(c)
	if !strings.Contains(string(data), `"context":[]`) {
		t.Errorf("created: %s", data)
	}
	got, _ := app.store.GetCard(c.ID)
	data, _ = json.Marshal(got)
	if !strings.Contains(string(data), `"context":[]`) {
		t.Errorf("loaded: %s", data)
	}
}

func TestContextPatchReplaceAndClear(t *testing.T) {
	app, dir, file := ctxFixture(t)
	c := ctxCard(t, app, []string{dir})
	url := fmt.Sprintf("%s/api/cards/%d", app.baseURL, c.ID)
	var got model.Card
	if code := sendJSON(t, "PATCH", url, map[string]any{"context": []string{file}}, &got); code != 200 {
		t.Fatalf("PATCH = %d", code)
	}
	if len(got.Context) != 1 || got.Context[0] != (model.ContextItem{Path: file, Type: "file"}) {
		t.Errorf("replaced: %v", got.Context)
	}
	if code := sendJSON(t, "PATCH", url, map[string]any{"title": "x"}, &got); code != 200 || len(got.Context) != 1 {
		t.Errorf("untouched context: %d %v", code, got.Context)
	}
	got = model.Card{}
	if code := sendJSON(t, "PATCH", url, map[string]any{"context": []string{}}, &got); code != 200 {
		t.Fatalf("clear PATCH = %d", code)
	}
	stored, _ := app.store.GetCard(c.ID)
	if got.Context == nil || len(got.Context) != 0 || len(stored.Context) != 0 {
		t.Errorf("cleared: %v / %v", got.Context, stored.Context)
	}
}

func TestContextRefused(t *testing.T) {
	app, dir, file := ctxFixture(t)
	c := ctxCard(t, app, []string{dir})
	tooMany := make([]string, 21)
	for i := range tooMany {
		tooMany[i] = filepath.Join(dir, fmt.Sprint("f", i))
		os.WriteFile(tooMany[i], nil, 0o644)
	}
	cases := map[string][]string{
		"relative":     {"docs/x"},
		"outside":      {"/etc/hostname"},
		"missing":      {filepath.Join(dir, "nope")},
		"data dir":     {app.dataDir},
		"inside data":  {filepath.Join(app.dataDir, "test.db")},
		"one bad":      {file, filepath.Join(dir, "nope")},
		"empty":        {" "},
		"over the cap": tooMany,
	}
	for name, paths := range cases {
		if _, err := app.EditCard(c.ID, cardInput{Title: ptr("changed"), Context: &paths}); err == nil {
			t.Errorf("%s: edit accepted", name)
		}
		if _, err := app.CreateCard(cardInput{Title: ptr("n"), RootFolder: ptr(app.guard.Root()), Context: &paths}); err == nil {
			t.Errorf("%s: create accepted", name)
		}
	}
	got, _ := app.store.GetCard(c.ID)
	if got.Title != "c" || len(got.Context) != 1 || got.Context[0].Path != dir {
		t.Errorf("refused edits changed the card: %q %v", got.Title, got.Context)
	}
	if cards, _ := app.store.ListCards(); len(cards) != 1 {
		t.Errorf("%d cards after refused creates", len(cards))
	}
}

func TestContextOutsideSandboxFile(t *testing.T) {
	app, _, _ := ctxFixture(t)
	other := testenv.Dir(t) // a sibling scratch dir: not inside the app's sandbox
	if _, err := app.normalizeContext([]string{other}); err == nil || !strings.Contains(err.Error(), "sandbox") {
		t.Errorf("outside dir: %v", err)
	}
}

func TestContextEditRefusedWhenRunning(t *testing.T) {
	app, dir, file := ctxFixture(t)
	c := ctxCard(t, app, []string{dir})
	app.setStatus(c.ID, model.StatusRunning)
	if _, err := app.EditCard(c.ID, cardInput{Context: &[]string{file}}); err == nil || !strings.Contains(err.Error(), "only todo and ready") {
		t.Errorf("edit while running: %v", err)
	}
	got, _ := app.store.GetCard(c.ID)
	if len(got.Context) != 1 || got.Context[0].Path != dir {
		t.Errorf("context changed: %v", got.Context)
	}
}

func TestBoardMCPCreateWithContext(t *testing.T) {
	app, dir, file := ctxFixture(t)
	text, isErr := boardCall(t, app, "create_card", map[string]any{"title": "Ctx", "root_folder": app.guard.Root(), "context": []string{dir, file}})
	if isErr {
		t.Fatal(text)
	}
	var c model.Card
	json.Unmarshal([]byte(text), &c)
	if len(c.Context) != 2 || c.Context[0].Type != "dir" || c.Context[1].Type != "file" {
		t.Errorf("created context: %v", c.Context)
	}
	text, _ = boardCall(t, app, "get_card", map[string]any{"id": c.ID})
	var detail struct{ Card model.Card }
	json.Unmarshal([]byte(text), &detail)
	if len(detail.Card.Context) != 2 {
		t.Errorf("get_card context: %s", text)
	}
	if text, isErr := boardCall(t, app, "update_card", map[string]any{"id": c.ID, "context": []string{"rel"}}); !isErr {
		t.Errorf("relative accepted: %s", text)
	}
	if text, isErr := boardCall(t, app, "update_card", map[string]any{"id": c.ID, "context": []string{}}); isErr {
		t.Errorf("clear: %s", text)
	}
	if got, _ := app.store.GetCard(c.ID); len(got.Context) != 0 {
		t.Errorf("not cleared: %v", got.Context)
	}
}

func TestMigrationAddsContext(t *testing.T) {
	path := filepath.Join(testenv.Dir(t), "old.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	// A database from before context existed.
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

	s, err := store.OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.GetCard(1)
	if err != nil {
		t.Fatal(err)
	}
	if c.Context == nil || len(c.Context) != 0 {
		t.Errorf("migrated context = %#v", c.Context)
	}
	c.Context = []model.ContextItem{{Path: "/x", Type: "dir"}}
	if err := s.SaveCard(c); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetCard(1); len(got.Context) != 1 {
		t.Errorf("saved context = %v", got.Context)
	}
	if _, err := store.OpenStore(path); err != nil {
		t.Errorf("reopen: %v", err)
	}
}
