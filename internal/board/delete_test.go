package board

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"maestrello/internal/model"
	"maestrello/internal/testenv"
)

// cardWithStatus creates a card in the given status that has an event and an input.
func cardWithStatus(t *testing.T, app *App, root, status string) *model.Card {
	t.Helper()
	c := newCard(t, app, root, status)
	c.Status = status
	if err := app.store.SaveCard(c); err != nil {
		t.Fatal(err)
	}
	if _, err := app.store.AddEvent(c.ID, "status", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := app.store.CreateInput(c.ID, "question", map[string]string{"q": "?"}); err != nil {
		t.Fatal(err)
	}
	return c
}

func cardIDs(t *testing.T, app *App) map[int64]bool {
	t.Helper()
	all, err := app.store.ListCards()
	if err != nil {
		t.Fatal(err)
	}
	ids := map[int64]bool{}
	for _, c := range all {
		ids[c.ID] = true
	}
	return ids
}

func TestClearDoneCards(t *testing.T) {
	dir := testenv.Dir(t)
	app := newTestApp(t, dir)
	done1 := cardWithStatus(t, app, dir, model.StatusDone)
	done2 := cardWithStatus(t, app, dir, model.StatusDone)
	todo := cardWithStatus(t, app, dir, model.StatusTodo)
	review := cardWithStatus(t, app, dir, model.StatusReview)

	// Only status=done is accepted; nothing is deleted otherwise.
	for _, q := range []string{"?status=todo", "", "?status=review", "?status=Done"} {
		var e map[string]string
		if code := sendJSON(t, "DELETE", app.baseURL+"/api/cards"+q, nil, &e); code != 400 || e["error"] == "" {
			t.Errorf("DELETE /api/cards%s = %d %v, want 400", q, code, e)
		}
	}
	if n := len(cardIDs(t, app)); n != 4 {
		t.Fatalf("rejected clears deleted cards: %d left", n)
	}

	var got map[string]int
	if code := sendJSON(t, "DELETE", app.baseURL+"/api/cards?status=done", nil, &got); code != 200 || got["deleted"] != 2 {
		t.Fatalf("clear = %d %v, want 200 {deleted:2}", code, got)
	}
	ids := cardIDs(t, app)
	if len(ids) != 2 || !ids[todo.ID] || !ids[review.ID] {
		t.Errorf("cards left = %v, want #%d and #%d", ids, todo.ID, review.ID)
	}
	for _, c := range []*model.Card{done1, done2} {
		ev, _ := app.store.Events(c.ID, 100)
		in, _ := app.store.Inputs(c.ID)
		if len(ev) != 0 || len(in) != 0 {
			t.Errorf("card %d: %d events, %d inputs left", c.ID, len(ev), len(in))
		}
	}
	if ev, _ := app.store.Events(todo.ID, 100); len(ev) != 1 {
		t.Errorf("todo card lost its events: %d", len(ev))
	}

	// Nothing left in Done.
	got = nil
	if code := sendJSON(t, "DELETE", app.baseURL+"/api/cards?status=done", nil, &got); code != 200 || got["deleted"] != 0 {
		t.Errorf("empty clear = %d %v, want 200 {deleted:0}", code, got)
	}
}

func TestDeleteSingleCard(t *testing.T) {
	dir := testenv.Dir(t)
	app := newTestApp(t, dir)
	done := cardWithStatus(t, app, dir, model.StatusDone)
	review := cardWithStatus(t, app, dir, model.StatusReview)
	failed := cardWithStatus(t, app, dir, model.StatusFailed)
	refining := cardWithStatus(t, app, dir, model.StatusRefining)

	for _, c := range []*model.Card{done, review, failed} {
		if code := sendJSON(t, "DELETE", fmt.Sprintf("%s/api/cards/%d", app.baseURL, c.ID), nil, nil); code != 204 {
			t.Errorf("delete %s card = %d, want 204", c.Status, code)
		}
	}
	var e map[string]string
	if code := sendJSON(t, "DELETE", fmt.Sprintf("%s/api/cards/%d", app.baseURL, refining.ID), nil, &e); code != 400 ||
		!strings.Contains(e["error"], "being refined") {
		t.Errorf("delete refining card = %d %v, want 400", code, e)
	}
	if code := sendJSON(t, "DELETE", app.baseURL+"/api/cards/99999", nil, nil); code != 404 {
		t.Errorf("delete missing card = %d, want 404", code)
	}
	ids := cardIDs(t, app)
	if len(ids) != 1 || !ids[refining.ID] {
		t.Errorf("cards left = %v, want only #%d", ids, refining.ID)
	}
}
