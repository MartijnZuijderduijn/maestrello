package board

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"maestrello/internal/model"
	"maestrello/internal/store"
	"maestrello/internal/testenv"
)

func TestDeriveTitle(t *testing.T) {
	long := "Refactor the authentication middleware so that tokens are validated once per request instead of twice"
	cases := []struct{ name, in, want string }{
		{"blank", "  \n\t\n ", ""},
		{"empty", "", ""},
		{"only markers", "## \n- \n", ""},
		{"first non-blank line", "\n\n  Fix the login bug  \nmore detail here", "Fix the login bug"},
		{"heading", "## Add dark mode\nbody", "Add dark mode"},
		{"bullet", "- add a thing", "add a thing"},
		{"star and quote", "> * nested quote", "nested quote"},
		{"numbered", "12. Do the twelfth thing", "Do the twelfth thing"},
		{"numbered paren", "2) second", "second"},
		{"keeps version number", "3.5 release notes", "3.5 release notes"},
		{"collapses whitespace", "a   b\t c", "a b c"},
		{"long cut at word", long, "Refactor the authentication middleware so that tokens are…"},
		{"unicode short", "Ünïcödé 日本語 タスク", "Ünïcödé 日本語 タスク"},
		{"unicode long", strings.Repeat("日本語 ", 30), strings.TrimSpace(strings.Repeat("日本語 ", 15)) + "…"},
		{"no spaces long", strings.Repeat("é", 80), strings.Repeat("é", 60) + "…"},
	}
	for _, c := range cases {
		got := deriveTitle(c.in)
		if got != c.want {
			t.Errorf("%s: deriveTitle(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
		if n := len([]rune(strings.TrimSuffix(got, "…"))); n > titleMax {
			t.Errorf("%s: title has %d runes", c.name, n)
		}
	}
}

func TestCreateCardTitleDerivation(t *testing.T) {
	dir := testenv.Dir(t)
	app := newTestApp(t, dir)
	var c model.Card
	if code := sendJSON(t, "POST", app.baseURL+"/api/cards", map[string]any{"description": "Fix the bug\nIt crashes.", "root_folder": dir}, &c); code != 201 {
		t.Fatalf("status %d", code)
	}
	if c.Title != "Fix the bug" || !c.TitleAuto {
		t.Errorf("derived card: %q auto=%v", c.Title, c.TitleAuto)
	}
	var e map[string]any
	if code := sendJSON(t, "POST", app.baseURL+"/api/cards", map[string]any{"title": " ", "description": "\n"}, &e); code != 400 {
		t.Errorf("empty card: status %d", code)
	}
	var d model.Card
	if code := sendJSON(t, "POST", app.baseURL+"/api/cards", map[string]any{"title": "Mine", "description": "other text", "root_folder": dir}, &d); code != 201 || d.Title != "Mine" || d.TitleAuto {
		t.Errorf("explicit title: %d %q auto=%v", code, d.Title, d.TitleAuto)
	}
}

func TestEditCardTitleAuto(t *testing.T) {
	dir := testenv.Dir(t)
	app := newTestApp(t, dir)
	c, err := app.CreateCard(cardInput{Description: strp("First line\nrest"), RootFolder: &dir})
	if err != nil || !c.TitleAuto {
		t.Fatal(c, err)
	}
	// The edit form sends the title back unchanged: the flag stays.
	got, err := app.EditCard(c.ID, cardInput{Title: strp(c.Title), Description: strp("First line\nrest")})
	if err != nil || !got.TitleAuto {
		t.Fatalf("same title: %+v %v", got, err)
	}
	// A changed description re-derives the title, even when the form sends the old title along.
	got, err = app.EditCard(c.ID, cardInput{Title: strp(c.Title), Description: strp("Second line\nrest")})
	if err != nil || got.Title != "Second line" || !got.TitleAuto {
		t.Fatalf("new description: %+v %v", got, err)
	}
	// A different title is mine from then on.
	got, err = app.EditCard(c.ID, cardInput{Title: strp("Renamed")})
	if err != nil || got.Title != "Renamed" || got.TitleAuto {
		t.Fatalf("renamed: %+v %v", got, err)
	}
	got, err = app.EditCard(c.ID, cardInput{Description: strp("Third line")})
	if err != nil || got.Title != "Renamed" {
		t.Fatalf("description change on a typed title: %+v %v", got, err)
	}
}

func TestRefineTitle(t *testing.T) {
	dir := testenv.Dir(t)
	app := newTestApp(t, dir)
	auto, err := app.CreateCard(cardInput{Description: strp("do the thing"), RootFolder: &dir})
	if err != nil {
		t.Fatal(err)
	}
	typed, err := app.CreateCard(cardInput{Title: strp("My own"), Description: strp("do it"), RootFolder: &dir})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []*model.Card{auto, typed} {
		if err := app.Refine(c.ID); err != nil {
			t.Fatal(err)
		}
	}
	if got := waitStatus(t, app, auto.ID, model.StatusReady); got.Title != "Fake refined title" || !got.TitleAuto {
		t.Errorf("auto card: %q auto=%v", got.Title, got.TitleAuto)
	}
	if got := waitStatus(t, app, typed.ID, model.StatusReady); got.Title != "My own" || got.TitleAuto {
		t.Errorf("typed card: %q auto=%v", got.Title, got.TitleAuto)
	}
}

func TestSkipRefinementAutoTitle(t *testing.T) {
	dir := testenv.Dir(t)
	app := newTestApp(t, dir)
	c, err := app.CreateCard(cardInput{Description: strp("Fix the bug\nIt crashes."), RootFolder: &dir, SkipRefinement: boolp(true)})
	if err != nil {
		t.Fatal(err)
	}
	if c.RefinedPrompt != "Fix the bug\nIt crashes." {
		t.Errorf("prompt = %q, want the description only", c.RefinedPrompt)
	}
}

func TestBoardMCPCreateWithoutTitle(t *testing.T) {
	app := newTestApp(t, testenv.Dir(t))
	text, isErr := boardCall(t, app, "create_card", map[string]any{"description": "Write the docs\nplease", "root_folder": app.dataDir})
	if isErr {
		t.Fatal(text)
	}
	var c model.Card
	json.Unmarshal([]byte(text), &c)
	if c.Title != "Write the docs" || !c.TitleAuto {
		t.Errorf("created: %s", text)
	}
}

func TestOpenStoreWithoutTitleAutoColumn(t *testing.T) {
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
		_, err = db.Exec(`INSERT INTO cards (title, status, created_at, updated_at) VALUES ('old card', 'todo', '2025-01-01T00:00:00Z', '2025-01-01T00:00:00Z')`)
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
	if err != nil || len(cards) != 1 || cards[0].Title != "old card" || cards[0].TitleAuto {
		t.Fatalf("old card: %+v, %v", cards, err)
	}
	cards[0].TitleAuto = true
	if err := s.SaveCard(cards[0]); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetCard(cards[0].ID); !got.TitleAuto {
		t.Error("title_auto not saved on the migrated database")
	}
}
