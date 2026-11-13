package store

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"maestrello/internal/testenv"
)

// An old database has cards without project_id: opening it groups them by root folder.
func TestProjectMigration(t *testing.T) {
	dir := testenv.Dir(t)
	path := filepath.Join(dir, "old.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	// The cards table as it was before projects (the columns the migration needs).
	if _, err := db.Exec(`CREATE TABLE cards (id INTEGER PRIMARY KEY AUTOINCREMENT, title TEXT NOT NULL,
		description TEXT NOT NULL DEFAULT '', root_folder TEXT NOT NULL DEFAULT '', restrict_refinement INTEGER NOT NULL DEFAULT 0,
		auto_run INTEGER NOT NULL DEFAULT 0, skip_refinement INTEGER NOT NULL DEFAULT 0, model TEXT NOT NULL DEFAULT '',
		effort TEXT NOT NULL DEFAULT '', permission_mode TEXT NOT NULL DEFAULT '', harness TEXT NOT NULL DEFAULT 'claude',
		max_turns INTEGER NOT NULL DEFAULT 0, max_minutes INTEGER NOT NULL DEFAULT 0, push_default TEXT NOT NULL DEFAULT '',
		refined_prompt TEXT NOT NULL DEFAULT '', refine_notes TEXT NOT NULL DEFAULT '', status TEXT NOT NULL DEFAULT 'todo',
		session_id TEXT NOT NULL DEFAULT '', summary TEXT NOT NULL DEFAULT '', fail_reason TEXT NOT NULL DEFAULT '',
		warning TEXT NOT NULL DEFAULT '', activity TEXT NOT NULL DEFAULT '', waiting_on INTEGER NOT NULL DEFAULT 0,
		turns_used INTEGER NOT NULL DEFAULT 0, cost_usd REAL NOT NULL DEFAULT 0, created_at TIMESTAMP NOT NULL, updated_at TIMESTAMP NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	for _, r := range [][2]string{{"one", "/x/svc"}, {"two", "/y/svc"}, {"three", "/x/svc"}} {
		if _, err := db.Exec(`INSERT INTO cards (title, root_folder, created_at, updated_at) VALUES (?,?,?,?)`, r[0], r[1], time.Now(), time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()

	for i := 0; i < 2; i++ { // the second open must change nothing
		s, err := OpenStore(path)
		if err != nil {
			t.Fatal(err)
		}
		ps, _ := s.ListProjects()
		if len(ps) != 2 || ps[0].Name != "svc" || ps[0].RootFolder != "/x/svc" || ps[1].Name != "svc (2)" || ps[1].RootFolder != "/y/svc" {
			t.Fatalf("open %d: projects = %+v", i, ps)
		}
		cards, _ := s.ListCards()
		want := []int64{ps[0].ID, ps[1].ID, ps[0].ID}
		for j, c := range cards {
			if c.ProjectID != want[j] || c.Title != []string{"one", "two", "three"}[j] {
				t.Errorf("open %d: card %d = %+v", i, j, c)
			}
		}
		s.db.Close()
	}
}
