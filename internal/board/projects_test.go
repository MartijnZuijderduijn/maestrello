package board

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"maestrello/internal/model"
	"maestrello/internal/testenv"
)

func mkProject(t *testing.T, app *App, name, root string) *model.Project {
	t.Helper()
	p, err := app.CreateProject(name, root)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func inProject(t *testing.T, app *App, c *model.Card, p *model.Project) {
	t.Helper()
	c.ProjectID = p.ID
	if err := app.store.SaveCard(c); err != nil {
		t.Fatal(err)
	}
}

func TestProjectCRUD(t *testing.T) {
	dir := testenv.Dir(t)
	app := newTestApp(t, dir)
	rootA, rootB := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	base := app.baseURL + "/api/projects"

	var p model.Project
	if code := sendJSON(t, "POST", base, map[string]string{"name": " Alpha ", "root_folder": rootA + "/"}, &p); code != 201 ||
		p.Name != "Alpha" || p.RootFolder != rootA || p.ID == 0 {
		t.Fatalf("create = %d %+v", code, p)
	}
	bad := []map[string]string{
		{"name": "Alpha", "root_folder": rootB},                    // duplicate name
		{"name": "", "root_folder": rootB},                         // no name
		{"name": "Rel", "root_folder": "relative/dir"},             // not absolute
		{"name": "Out", "root_folder": "/tmp/outside-the-sandbox"}, // outside the sandbox
	}
	for _, b := range bad {
		var e map[string]string
		if code := sendJSON(t, "POST", base, b, &e); code != 400 || e["error"] == "" {
			t.Errorf("POST %v = %d %v, want 400", b, code, e)
		}
	}
	var q model.Project
	sendJSON(t, "POST", base, map[string]string{"name": "Beta", "root_folder": rootB}, &q)

	var list []model.Project
	sendJSON(t, "GET", base, nil, &list)
	if len(list) != 2 || list[0].ID != p.ID || list[1].ID != q.ID {
		t.Fatalf("list = %+v", list)
	}

	// Patch: rename, change root; a clash and a bad root are refused.
	var e map[string]string
	if code := sendJSON(t, "PATCH", base+"/"+itoa(q.ID), map[string]string{"name": "Alpha"}, &e); code != 400 {
		t.Errorf("rename to duplicate = %d", code)
	}
	if code := sendJSON(t, "PATCH", base+"/"+itoa(q.ID), map[string]string{"root_folder": "/tmp/x"}, &e); code != 400 {
		t.Errorf("root outside sandbox = %d", code)
	}
	if code := sendJSON(t, "PATCH", base+"/999", map[string]string{"name": "x"}, &e); code != 404 {
		t.Errorf("patch unknown = %d", code)
	}
	card := newCard(t, app, rootB, "keeps root")
	inProject(t, app, card, &q)
	var got model.Project
	if code := sendJSON(t, "PATCH", base+"/"+itoa(q.ID), map[string]string{"name": "Gamma", "root_folder": rootA}, &got); code != 200 ||
		got.Name != "Gamma" || got.RootFolder != rootA {
		t.Fatalf("patch = %d %+v", code, got)
	}
	if c, _ := app.store.GetCard(card.ID); c.RootFolder != rootB {
		t.Errorf("changing a project's root moved the card to %s", c.RootFolder)
	}

	// Delete is refused while the project has cards.
	if code := sendJSON(t, "DELETE", base+"/"+itoa(q.ID), nil, &e); code != 400 || !strings.Contains(e["error"], "card") {
		t.Errorf("delete with cards = %d %v", code, e)
	}
	app.store.DeleteCard(card.ID)
	if code := sendJSON(t, "DELETE", base+"/"+itoa(q.ID), nil, nil); code != 204 {
		t.Errorf("delete empty project = %d", code)
	}
	if _, err := app.store.GetProject(q.ID); err == nil {
		t.Error("project still there")
	}
}

func TestProjectCountsAndSSE(t *testing.T) {
	dir := testenv.Dir(t)
	app := newTestApp(t, dir)
	ch := app.hub.Subscribe()
	p := mkProject(t, app, "Alpha", dir)
	if msg := string(<-ch); !strings.Contains(msg, "event: project") {
		t.Errorf("created event = %q", msg)
	}
	c := newCard(t, app, dir, "x")
	inProject(t, app, c, p)
	var list []model.Project
	sendJSON(t, "GET", app.baseURL+"/api/projects", nil, &list)
	if list[0].Counts[model.StatusTodo] != 1 {
		t.Errorf("counts = %v", list[0].Counts)
	}
	app.store.DeleteCard(c.ID)
	if err := app.DeleteProject(p.ID); err != nil {
		t.Fatal(err)
	}
	if msg := string(<-ch); !strings.Contains(msg, "event: project_deleted") {
		t.Errorf("deleted event = %q", msg)
	}
}

func TestCreateCardProjects(t *testing.T) {
	dir := testenv.Dir(t)
	app := newTestApp(t, dir)
	root := filepath.Join(dir, "services")
	os.MkdirAll(root, 0o755)
	p := mkProject(t, app, "Services", root)
	base := app.baseURL + "/api/cards"

	// project_id without root_folder inherits the project's root.
	var c model.Card
	if code := sendJSON(t, "POST", base, map[string]any{"title": "t", "project_id": p.ID}, &c); code != 201 ||
		c.ProjectID != p.ID || c.RootFolder != root {
		t.Fatalf("create = %d %+v", code, c)
	}
	// An explicit root_folder wins but the card stays in the project.
	var c2 model.Card
	sendJSON(t, "POST", base, map[string]any{"title": "t", "project_id": p.ID, "root_folder": dir}, &c2)
	if c2.ProjectID != p.ID || c2.RootFolder != dir {
		t.Errorf("explicit root = %+v", c2)
	}
	// Unknown project.
	var e map[string]string
	if code := sendJSON(t, "POST", base, map[string]any{"title": "t", "project_id": 999}, &e); code != 400 || !strings.Contains(e["error"], "unknown project") {
		t.Errorf("unknown project = %d %v", code, e)
	}
	// Neither: the default root's project is used, and created when missing.
	st, _ := app.store.Settings()
	st.DefaultRootFolder = filepath.Join(dir, "default")
	app.store.SaveSettings(st)
	var c3, c4 model.Card
	if code := sendJSON(t, "POST", base, map[string]any{"title": "t"}, &c3); code != 201 || c3.ProjectID == 0 || c3.ProjectID == p.ID {
		t.Fatalf("default create = %d %+v", code, c3)
	}
	sendJSON(t, "POST", base, map[string]any{"title": "t"}, &c4)
	if c4.ProjectID != c3.ProjectID {
		t.Errorf("second default card in project %d, want %d", c4.ProjectID, c3.ProjectID)
	}
	if dp, _ := app.store.GetProject(c3.ProjectID); dp.RootFolder != st.DefaultRootFolder || dp.Name != "default" {
		t.Errorf("default project = %+v", dp)
	}
	if ps, _ := app.store.ListProjects(); len(ps) != 2 {
		t.Errorf("projects = %d, want 2", len(ps))
	}

	// PATCH moves a Todo card to another project and keeps its root; unknown is refused.
	var moved model.Card
	if code := sendJSON(t, "PATCH", base+"/"+itoa(c.ID), map[string]any{"project_id": c3.ProjectID}, &moved); code != 200 ||
		moved.ProjectID != c3.ProjectID || moved.RootFolder != root {
		t.Errorf("move project = %d %+v", code, moved)
	}
	if code := sendJSON(t, "PATCH", base+"/"+itoa(c.ID), map[string]any{"project_id": 999}, &e); code != 400 {
		t.Errorf("move to unknown = %d", code)
	}
	// Not in Todo/Ready: refused.
	run := newCard(t, app, dir, "x")
	run.Status = model.StatusRunning
	app.store.SaveCard(run)
	if code := sendJSON(t, "PATCH", base+"/"+itoa(run.ID), map[string]any{"project_id": p.ID}, &e); code != 400 {
		t.Errorf("move running card = %d", code)
	}
}

func TestListCardsByProject(t *testing.T) {
	dir := testenv.Dir(t)
	app := newTestApp(t, dir)
	a, b := mkProject(t, app, "A", dir), mkProject(t, app, "B", dir)
	c1, c2 := newCard(t, app, dir, "1"), newCard(t, app, dir, "2")
	inProject(t, app, c1, a)
	inProject(t, app, c2, b)
	var all, onlyB []model.Card
	sendJSON(t, "GET", app.baseURL+"/api/cards", nil, &all)
	sendJSON(t, "GET", app.baseURL+"/api/cards?project="+itoa(b.ID), nil, &onlyB)
	if len(all) != 2 || len(onlyB) != 1 || onlyB[0].ID != c2.ID {
		t.Errorf("all=%d onlyB=%+v", len(all), onlyB)
	}
	if code := sendJSON(t, "GET", app.baseURL+"/api/cards?project=x", nil, nil); code != 400 {
		t.Errorf("bad project = %d", code)
	}
}

func TestBulkScopedToProject(t *testing.T) {
	dir := testenv.Dir(t)
	app := newTestApp(t, dir)
	a, b := mkProject(t, app, "A", dir), mkProject(t, app, "B", dir)

	var done [2]*model.Card
	for i, p := range []*model.Project{a, b} {
		c := newCard(t, app, dir, "done")
		c.Status = model.StatusDone
		c.ProjectID = p.ID
		app.store.SaveCard(c)
		done[i] = c
	}
	var out map[string]int
	if code := sendJSON(t, "DELETE", app.baseURL+"/api/cards?status=done&project="+itoa(a.ID), nil, &out); code != 200 || out["deleted"] != 1 {
		t.Fatalf("clear = %d %v", code, out)
	}
	if _, err := app.store.GetCard(done[0].ID); err == nil {
		t.Error("project A's done card survived")
	}
	if _, err := app.store.GetCard(done[1].ID); err != nil {
		t.Error("project B's done card was deleted")
	}

	ra, rb := readyCard(t, app, dir, "run in a"), readyCard(t, app, dir, "stay in b")
	inProject(t, app, ra, a)
	inProject(t, app, rb, b)
	var got struct {
		Started int `json:"started"`
	}
	if code := sendJSON(t, "POST", app.baseURL+"/api/cards/run?status=ready&project="+itoa(a.ID), nil, &got); code != 200 || got.Started != 1 {
		t.Fatalf("run = %d %+v", code, got)
	}
	waitStatus(t, app, ra.ID, model.StatusReview)
	if c, _ := app.store.GetCard(rb.ID); c.Status != model.StatusReady {
		t.Errorf("project B's card is %s, want ready", c.Status)
	}
}

func TestBoardMCPProjects(t *testing.T) {
	dir := testenv.Dir(t)
	app := newTestApp(t, dir)
	a, b := mkProject(t, app, "A", dir), mkProject(t, app, "B", dir)
	c1, c2 := newCard(t, app, dir, "1"), newCard(t, app, dir, "2")
	inProject(t, app, c1, a)
	inProject(t, app, c2, b)

	text, isErr := boardCall(t, app, "list_projects", nil)
	if isErr || !strings.Contains(text, `"name": "A"`) || !strings.Contains(text, `"name": "B"`) || !strings.Contains(text, `"todo": 1`) {
		t.Errorf("list_projects = %v %s", isErr, text)
	}
	text, isErr = boardCall(t, app, "list_cards", map[string]any{"project": b.ID})
	if isErr || strings.Contains(text, `"id": `+itoa(c1.ID)+",") || !strings.Contains(text, `"project_id": `+itoa(b.ID)) {
		t.Errorf("list_cards project = %v %s", isErr, text)
	}
	text, isErr = boardCall(t, app, "create_card", map[string]any{"title": "via mcp", "project_id": a.ID})
	if isErr || !strings.Contains(text, `"project_id": `+itoa(a.ID)) {
		t.Errorf("create_card = %v %s", isErr, text)
	}
	if _, isErr = boardCall(t, app, "create_card", map[string]any{"title": "x", "project_id": 999}); !isErr {
		t.Error("create_card with unknown project succeeded")
	}
	text, isErr = boardCall(t, app, "update_card", map[string]any{"id": c1.ID, "project_id": b.ID})
	if isErr || !strings.Contains(text, `"project_id": `+itoa(b.ID)) {
		t.Errorf("update_card = %v %s", isErr, text)
	}
}
