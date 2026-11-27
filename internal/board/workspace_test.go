package board

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"maestrello/internal/model"
	"maestrello/internal/store"
)

func wsWorkspace(t *testing.T, app *App, p *model.Project, done bool) *WorkspaceView {
	t.Helper()
	v, err := app.Workspace(p.ID, done)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func wsEntry(t *testing.T, v *WorkspaceView, path string) *WorkspaceEntry {
	t.Helper()
	for _, e := range v.Entries {
		if e.Path == path {
			return e
		}
	}
	t.Fatalf("no entry %q in %+v", path, v.Entries)
	return nil
}

func TestWorkspaceGitCard(t *testing.T) {
	_, root, app := repoFixture(t)
	os.MkdirAll(filepath.Join(root, "notes"), 0o755)
	p := mkProject(t, app, "P", root)
	holder := holdRoot(t, app, root, "notes")
	c := readyCard(t, app, root, "WRITE ADD:svc/b", "svc/a")
	inProject(t, app, c, p)
	app.Run(c.ID)
	waitStatus(t, app, c.ID, model.StatusReview)

	e := wsEntry(t, wsWorkspace(t, app, p, false), "svc/a")
	if e.Kind != "git" || len(e.Leases) != 0 || len(e.Uses) != 1 {
		t.Fatalf("entry = %+v", e)
	}
	if u := e.Uses[0]; u.Branch == "" || u.WorktreePath == "" || u.Planned {
		t.Errorf("use = %+v", u)
	}
	answerOpen(t, app, holder.ID)
	waitStatus(t, app, holder.ID, model.StatusReview)
}

func TestWorkspaceLocalLeaseAndWaiter(t *testing.T) {
	dir, root := plainRoot(t)
	app := newTestApp(t, dir)
	p := mkProject(t, app, "P", root)
	holder := holdRoot(t, app, root)
	app.update(holder.ID, func(c *model.Card) { c.ProjectID = p.ID })
	second := readyCard(t, app, root, "LOCAL")
	inProject(t, app, second, p)
	app.Run(second.ID)
	waitWaiting(t, app, second.ID, holder.ID)

	v := wsWorkspace(t, app, p, false)
	if len(v.Entries) != 1 {
		t.Fatalf("entries = %+v", v.Entries)
	}
	e := wsEntry(t, v, ".")
	if e.Kind != "local" || e.Abs != leaseKey(root) {
		t.Errorf("entry = %+v", e)
	}
	if len(e.Leases) != 1 {
		t.Fatalf("leases = %+v", e.Leases)
	}
	if l := e.Leases[0]; l.CardID != holder.ID || !l.Live || l.SessionID == "" || l.OtherBoard || l.OtherProject {
		t.Errorf("lease = %+v", l)
	}
	if len(e.Waiting) != 1 || e.Waiting[0].CardID != second.ID || e.Waiting[0].BlockedBy != holder.ID {
		t.Errorf("waiting = %+v", e.Waiting)
	}
	answerOpen(t, app, holder.ID)
}

func TestWorkspaceOtherProject(t *testing.T) {
	dir, root := plainRoot(t)
	app := newTestApp(t, dir)
	p := mkProject(t, app, "P", root)
	q := mkProject(t, app, "Q", root)
	holder := holdRoot(t, app, root)
	app.update(holder.ID, func(c *model.Card) { c.ProjectID = q.ID })
	mine := newCard(t, app, root, "x")
	inProject(t, app, mine, p)
	app.store.SetPlannedRepos(mine.ID, nil)

	v := wsWorkspace(t, app, p, false)
	for _, e := range v.Entries {
		for _, u := range e.Uses {
			if u.CardID == holder.ID {
				t.Errorf("other project's card in uses: %+v", u)
			}
		}
	}
	// The overlapping lease only shows on entries of this project.
	if err := app.store.SetPlannedRepos(mine.ID, []string{"."}); err != nil {
		t.Fatal(err)
	}
	e := wsEntry(t, wsWorkspace(t, app, p, false), ".")
	if len(e.Leases) != 1 || !e.Leases[0].OtherProject || e.Leases[0].CardID != holder.ID {
		t.Errorf("leases = %+v", e.Leases)
	}
	if !e.Uses[0].Planned || e.Uses[0].Live {
		t.Errorf("use = %+v", e.Uses[0])
	}
	answerOpen(t, app, holder.ID)
}

func TestWorkspaceDoneCards(t *testing.T) {
	dir, root := plainRoot(t)
	app := newTestApp(t, dir)
	p := mkProject(t, app, "P", root)
	c := readyCard(t, app, root, "LOCAL", ".")
	c.Status = model.StatusDone
	inProject(t, app, c, p)

	if v := wsWorkspace(t, app, p, false); len(v.Entries) != 0 {
		t.Errorf("done card shown: %+v", v.Entries)
	}
	v := wsWorkspace(t, app, p, true)
	if len(v.Entries) != 1 || len(v.Entries[0].Uses) != 1 {
		t.Errorf("entries = %+v", v.Entries)
	}
}

func TestWorkspaceUnknownProject(t *testing.T) {
	dir, _ := plainRoot(t)
	app := newTestApp(t, dir)
	if _, err := app.Workspace(9999, false); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("err = %v", err)
	}
}
