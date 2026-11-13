package store

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"maestrello/internal/model"
	"maestrello/internal/testenv"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := OpenStore(filepath.Join(testenv.Dir(t), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestStoreCards(t *testing.T) {
	s := newTestStore(t)
	c := &model.Card{Title: "first", RootFolder: "/r", Model: "sonnet", PermissionMode: "auto", MaxTurns: 50}
	if err := s.CreateCard(c); err != nil {
		t.Fatal(err)
	}
	if c.ID == 0 || c.Status != model.StatusTodo {
		t.Fatalf("created card: %+v", c)
	}

	c.Status = model.StatusReady
	c.RefinedPrompt = "do it"
	c.CostUSD = 0.25
	if err := s.SaveCard(c); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetCard(c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != model.StatusReady || got.RefinedPrompt != "do it" || got.CostUSD != 0.25 || got.MaxTurns != 50 {
		t.Errorf("round trip: %+v", got)
	}

	s.CreateCard(&model.Card{Title: "second"})
	all, _ := s.ListCards()
	if len(all) != 2 {
		t.Fatalf("ListCards = %d cards", len(all))
	}
	ready, _ := s.CardsWithStatus(model.StatusReady)
	if len(ready) != 1 || ready[0].ID != c.ID {
		t.Errorf("CardsWithStatus(ready) = %v", ready)
	}

	if err := s.DeleteCard(c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetCard(c.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetCard after delete: %v", err)
	}
}

func TestStoreRepos(t *testing.T) {
	s := newTestStore(t)
	c := &model.Card{Title: "c"}
	s.CreateCard(c)

	if err := s.SetPlannedRepos(c.ID, []string{"a", " b ", "", "a"}); err != nil {
		t.Fatal(err)
	}
	repos, _ := s.Repos(c.ID)
	if len(repos) != 2 || repos[0].RepoPath != "a" || repos[1].RepoPath != "b" {
		t.Fatalf("planned repos = %+v", repos)
	}

	// A checked-out repo survives re-planning; unchecked ones are replaced.
	repos[0].WorktreePath = "/w/a"
	s.SaveRepo(repos[0])
	s.SetPlannedRepos(c.ID, []string{"c"})
	repos, _ = s.Repos(c.ID)
	var paths []string
	for _, r := range repos {
		paths = append(paths, r.RepoPath)
	}
	if len(paths) != 2 || paths[0] != "a" || paths[1] != "c" {
		t.Errorf("after re-plan = %v", paths)
	}

	// AddRepo is idempotent and returns the existing row.
	r1 := &model.CardRepo{CardID: c.ID, RepoPath: "d"}
	r2 := &model.CardRepo{CardID: c.ID, RepoPath: "d"}
	s.AddRepo(r1)
	s.AddRepo(r2)
	if r1.ID == 0 || r1.ID != r2.ID {
		t.Errorf("AddRepo ids %d, %d", r1.ID, r2.ID)
	}
}

func TestStoreInputs(t *testing.T) {
	s := newTestStore(t)
	c := &model.Card{Title: "c"}
	s.CreateCard(c)

	if in, _ := s.OpenInput(c.ID); in != nil {
		t.Fatal("unexpected open input")
	}
	in, err := s.CreateInput(c.ID, "question", map[string]string{"question": "why?"})
	if err != nil {
		t.Fatal(err)
	}
	open, _ := s.OpenInput(c.ID)
	if open == nil || open.ID != in.ID || open.Kind != "question" {
		t.Fatalf("OpenInput = %+v", open)
	}
	var p map[string]string
	json.Unmarshal(open.Payload, &p)
	if p["question"] != "why?" {
		t.Errorf("payload = %s", open.Payload)
	}

	s.AnswerInput(in.ID, "because")
	s.AnswerInput(in.ID, "overwritten?") // answered inputs stay as they are
	if open, _ := s.OpenInput(c.ID); open != nil {
		t.Error("input still open after answer")
	}
	all, _ := s.Inputs(c.ID)
	if all[0].Answer != "because" || all[0].AnsweredAt == nil {
		t.Errorf("answered input = %+v", all[0])
	}
}

func TestStoreEvents(t *testing.T) {
	s := newTestStore(t)
	c := &model.Card{Title: "c"}
	s.CreateCard(c)
	for i := 0; i < 10; i++ {
		data, _ := json.Marshal(map[string]int{"n": i})
		s.AddEvent(c.ID, "board", data)
	}
	evs, _ := s.Events(c.ID, 3)
	if len(evs) != 3 {
		t.Fatalf("got %d events", len(evs))
	}
	// The last three, oldest first.
	for i, want := range []int{7, 8, 9} {
		var d map[string]int
		json.Unmarshal(evs[i].Data, &d)
		if d["n"] != want {
			t.Errorf("event %d = %d, want %d", i, d["n"], want)
		}
	}
}

func TestStoreSettings(t *testing.T) {
	s := newTestStore(t)
	st, err := s.Settings()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(st, model.DefaultSettings()) { // Settings holds a map now, so no ==
		t.Errorf("defaults = %+v", st)
	}
	if st.DefaultPermissionMode != "auto" || st.DefaultMaxTurns != 50 {
		t.Errorf("unexpected defaults: %+v", st)
	}
	st.DefaultModel = "opus"
	st.DefaultRootFolder = "/x"
	s.SaveSettings(st)
	got, _ := s.Settings()
	if !reflect.DeepEqual(got, st) {
		t.Errorf("round trip = %+v", got)
	}
}

func TestStoreSettingsOldJSON(t *testing.T) {
	s := newTestStore(t)
	old := `{"default_model":"opus","default_effort":"high","default_max_turns":7}`
	if _, err := s.db.Exec(`INSERT INTO settings (key, value) VALUES ('settings', ?)`, old); err != nil {
		t.Fatal(err)
	}
	st, err := s.Settings()
	if err != nil {
		t.Fatal(err)
	}
	want := model.DefaultSettings()
	want.DefaultModel, want.DefaultEffort, want.DefaultMaxTurns = "opus", "high", 7
	if !reflect.DeepEqual(st, want) {
		t.Errorf("got %+v, want %+v", st, want)
	}
	st.DefaultPushDefault = "pr"
	s.SaveSettings(st)
	if got, _ := s.Settings(); got.DefaultPushDefault != "pr" {
		t.Errorf("push default not persisted: %+v", got)
	}
}

func TestOldDatabaseGetsHarness(t *testing.T) {
	path := filepath.Join(testenv.Dir(t), "old.db")
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	// Make it a database from before the harness column and setting.
	if err := s.CreateCard(&model.Card{Title: "old"}); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`ALTER TABLE cards DROP COLUMN harness`,
		`INSERT INTO settings (key, value) VALUES ('settings', '{"default_model":"opus"}')`,
	} {
		if _, err := s.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	s.db.Close()

	s, err = OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.GetCard(1)
	if err != nil || c.Harness != "claude" {
		t.Errorf("old card: %+v, %v", c, err)
	}
	st, err := s.Settings()
	if err != nil || st.Harness != "claude" || st.DefaultModel != "opus" {
		t.Errorf("old settings: %+v, %v", st, err)
	}
}
