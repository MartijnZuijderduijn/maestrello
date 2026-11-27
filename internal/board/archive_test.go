package board

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"maestrello/internal/model"
	"maestrello/internal/testenv"
)

func TestArchiveAndRestoreCard(t *testing.T) {
	dir := testenv.Dir(t)
	app := newTestApp(t, dir)
	review := cardWithStatus(t, app, dir, model.StatusReview)
	todo := cardWithStatus(t, app, dir, model.StatusTodo)
	refining := cardWithStatus(t, app, dir, model.StatusRefining)
	url := func(id int64, action string) string { return fmt.Sprintf("%s/api/cards/%d/%s", app.baseURL, id, action) }

	if code := sendJSON(t, "POST", url(review.ID, "archive"), nil, nil); code != 200 {
		t.Fatalf("archive = %d, want 200", code)
	}
	// A refining card cannot be archived, as it cannot be deleted.
	var e map[string]string
	if code := sendJSON(t, "POST", url(refining.ID, "archive"), nil, &e); code != 400 || !strings.Contains(e["error"], "being refined") {
		t.Errorf("archive refining card = %d %v, want 400", code, e)
	}

	// Hidden from the board listing and the counts, but still stored with its events.
	ids := cardIDs(t, app)
	if ids[review.ID] || !ids[todo.ID] || !ids[refining.ID] {
		t.Errorf("board cards = %v, want the archived #%d gone", ids, review.ID)
	}
	var board []model.Card
	sendJSON(t, "GET", app.baseURL+"/api/cards", nil, &board)
	for _, c := range board {
		if c.ID == review.ID {
			t.Errorf("GET /api/cards lists the archived card")
		}
	}
	counts, _ := app.store.ProjectCounts()
	total := 0
	for _, byStatus := range counts {
		for _, n := range byStatus {
			total += n
		}
	}
	if total != 2 {
		t.Errorf("project counts = %v, want 2 cards", counts)
	}
	if ev, _ := app.store.Events(review.ID, 100); len(ev) == 0 {
		t.Errorf("archived card lost its events")
	}
	if text, _ := boardCall(t, app, "list_cards", nil); strings.Contains(text, fmt.Sprintf(`"id": %d,`, review.ID)) {
		t.Errorf("MCP list_cards shows the archived card: %s", text)
	}

	// The trash lists it, over HTTP and MCP; it was moved to Done on the way.
	var trash []model.Card
	if code := sendJSON(t, "GET", app.baseURL+"/api/cards?archived=1", nil, &trash); code != 200 || len(trash) != 1 || trash[0].ID != review.ID || !trash[0].Archived || trash[0].Status != model.StatusDone {
		t.Errorf("archived list = %d %+v", code, trash)
	}
	text, isErr := boardCall(t, app, "list_cards", map[string]any{"archived": true})
	var summaries []cardSummary
	if err := json.Unmarshal([]byte(text), &summaries); isErr || err != nil || len(summaries) != 1 || summaries[0].ID != review.ID || !summaries[0].Archived {
		t.Errorf("MCP list_cards archived = %v %s", isErr, text)
	}
	if _, isErr := boardCall(t, app, "get_card", map[string]any{"id": review.ID}); isErr {
		t.Errorf("get_card refuses an archived card")
	}

	// Restore puts it back on the board.
	if code := sendJSON(t, "POST", url(review.ID, "unarchive"), nil, nil); code != 200 {
		t.Fatalf("unarchive = %d", code)
	}
	if ids := cardIDs(t, app); !ids[review.ID] {
		t.Errorf("restored card is not on the board")
	}
	if left, _ := app.store.ListArchivedCards(); len(left) != 0 {
		t.Errorf("trash still has %d cards", len(left))
	}

	// The MCP tools do the same.
	if text, isErr := boardCall(t, app, "archive_card", map[string]any{"id": todo.ID}); isErr || !strings.Contains(text, `"archived": true`) {
		t.Errorf("archive_card = %v %s", isErr, text)
	}
	if text, isErr := boardCall(t, app, "unarchive_card", map[string]any{"id": todo.ID}); isErr || !strings.Contains(text, `"archived": false`) {
		t.Errorf("unarchive_card = %v %s", isErr, text)
	}
}

func TestDeleteArchivedCard(t *testing.T) {
	dir := testenv.Dir(t)
	app := newTestApp(t, dir)
	c := cardWithStatus(t, app, dir, model.StatusDone)
	if err := app.archiveCard(c.ID, false); err != nil {
		t.Fatal(err)
	}
	// Deleting from the trash removes it for good.
	if code := sendJSON(t, "DELETE", fmt.Sprintf("%s/api/cards/%d", app.baseURL, c.ID), nil, nil); code != 204 {
		t.Fatalf("delete archived = %d, want 204", code)
	}
	if left, _ := app.store.ListArchivedCards(); len(left) != 0 {
		t.Errorf("archived card survived delete")
	}
	if _, err := app.store.GetCard(c.ID); err == nil {
		t.Errorf("card still stored after delete")
	}
}

func TestArchiveAllDone(t *testing.T) {
	dir := testenv.Dir(t)
	app := newTestApp(t, dir)
	a, b := mkProject(t, app, "A", dir), mkProject(t, app, "B", dir)
	mk := func(status string, p *model.Project) *model.Card {
		c := cardWithStatus(t, app, dir, status)
		inProject(t, app, c, p)
		return c
	}
	doneA1, doneA2 := mk(model.StatusDone, a), mk(model.StatusDone, a)
	todoA, doneB := mk(model.StatusTodo, a), mk(model.StatusDone, b)
	url := app.baseURL + "/api/cards/archive"

	// Only status=done is accepted; nothing is archived otherwise.
	for _, q := range []string{"?status=todo", "", "?status=review", "?status=Done"} {
		var e map[string]string
		if code := sendJSON(t, "POST", url+q, nil, &e); code != 400 || e["error"] == "" {
			t.Errorf("POST /api/cards/archive%s = %d %v, want 400", q, code, e)
		}
	}
	if n := len(cardIDs(t, app)); n != 4 {
		t.Fatalf("rejected requests archived cards: %d left on the board", n)
	}

	var got map[string]int
	if code := sendJSON(t, "POST", url+"?status=done&project="+itoa(a.ID), nil, &got); code != 200 || got["archived"] != 2 {
		t.Fatalf("archive all = %d %v, want 200 {archived:2}", code, got)
	}
	ids := cardIDs(t, app)
	if len(ids) != 2 || !ids[todoA.ID] || !ids[doneB.ID] {
		t.Errorf("board cards = %v, want #%d and #%d", ids, todoA.ID, doneB.ID)
	}
	var trash []model.Card
	if code := sendJSON(t, "GET", app.baseURL+"/api/cards?archived=1", nil, &trash); code != 200 || len(trash) != 2 {
		t.Fatalf("trash = %d %+v", code, trash)
	}
	for _, c := range trash {
		if c.ID != doneA1.ID && c.ID != doneA2.ID || !c.Archived {
			t.Errorf("unexpected trash card %+v", c)
		}
	}
	// Archived cards keep their events, and a second run finds nothing left in A.
	if ev, _ := app.store.Events(doneA1.ID, 100); len(ev) == 0 {
		t.Errorf("archived card lost its events")
	}
	got = nil
	if code := sendJSON(t, "POST", url+"?status=done&project="+itoa(a.ID), nil, &got); code != 200 || got["archived"] != 0 {
		t.Errorf("repeat = %d %v, want {archived:0}", code, got)
	}
	// Without a project it archives the rest.
	if code := sendJSON(t, "POST", url+"?status=done", nil, &got); code != 200 || got["archived"] != 1 {
		t.Errorf("all projects = %d %v, want {archived:1}", code, got)
	}
}
