package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func intp(i int) *int { return &i }

func patchAgent(t *testing.T, app *App, id int64, body string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest("PATCH", app.baseURL+"/api/cards/"+fmt.Sprint(id)+"/agent", bytes.NewBufferString(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestEditAgentNonRunning(t *testing.T) {
	dir := testDir(t)
	app := newTestApp(t, dir)
	for _, status := range []string{StatusTodo, StatusReady, StatusReview, StatusFailed, StatusDone} {
		c := newCard(t, app, dir, "work")
		c.Status = status
		app.store.SaveCard(c)
		got, err := app.EditAgent(c.ID, AgentParams{Prompt: strp("  new prompt "), Model: strp("opus"), MaxTurns: intp(7), MaxMinutes: intp(3)})
		if err != nil {
			t.Fatalf("%s: %v", status, err)
		}
		saved, _ := app.store.GetCard(c.ID)
		if saved.RefinedPrompt != "new prompt" || saved.Model != "opus" || saved.MaxTurns != 7 || saved.MaxMinutes != 3 || got.Status != status {
			t.Errorf("%s: saved %+v", status, saved)
		}
	}
	// A field left out stays as it is.
	c := newCard(t, app, dir, "work")
	if got, err := app.EditAgent(c.ID, AgentParams{MaxTurns: intp(9)}); err != nil || got.Model != "fake" && got.Model != c.Model || got.MaxMinutes != 10 {
		t.Errorf("partial edit: %+v, %v", got, err)
	}
}

func TestEditAgentRefusedWhileRunning(t *testing.T) {
	dir := testDir(t)
	app := newTestApp(t, dir)
	for _, status := range []string{StatusRunning, StatusRefining, StatusNeedsInput} {
		c := newCard(t, app, dir, "work")
		c.Status, c.RefinedPrompt = status, "old"
		app.store.SaveCard(c)
		if _, err := app.EditAgent(c.ID, AgentParams{Prompt: strp("new")}); err == nil || !strings.Contains(err.Error(), "cannot be edited") {
			t.Errorf("%s: err = %v", status, err)
		}
		if code, body := patchAgent(t, app, c.ID, `{"prompt":"new"}`); code != 400 || body["error"] == nil {
			t.Errorf("%s over HTTP: %d %v", status, code, body)
		}
		if saved, _ := app.store.GetCard(c.ID); saved.RefinedPrompt != "old" {
			t.Errorf("%s: prompt changed to %q", status, saved.RefinedPrompt)
		}
	}
	// A finished card whose session is still winding down counts as running.
	c := newCard(t, app, dir, "work")
	c.Status = StatusReview
	app.store.SaveCard(c)
	app.mu.Lock()
	app.runs[c.ID] = &Run{}
	app.mu.Unlock()
	if _, err := app.EditAgent(c.ID, AgentParams{MaxTurns: intp(5)}); err == nil {
		t.Error("edit of a card with a live run was accepted")
	}
	app.mu.Lock()
	delete(app.runs, c.ID)
	app.mu.Unlock()
}

func TestEditAgentValidation(t *testing.T) {
	dir := testDir(t)
	app := newTestApp(t, dir)
	c := newCard(t, app, dir, "work")
	c.Harness, c.RefinedPrompt = "claude", "old"
	app.store.SaveCard(c)
	bad := map[string]AgentParams{
		"empty prompt":      {Prompt: strp("   ")},
		"unknown model":     {Model: strp("gpt-9")},
		"empty model":       {Model: strp("")},
		"zero turns":        {MaxTurns: intp(0)},
		"negative turns":    {MaxTurns: intp(-3)},
		"huge turns":        {MaxTurns: intp(maxTurnsLimit + 1)},
		"zero minutes":      {MaxMinutes: intp(0)},
		"huge minutes":      {MaxMinutes: intp(maxMinutesLimit + 1)},
		"good with bad one": {Prompt: strp("new"), MaxTurns: intp(0)},
	}
	for name, in := range bad {
		if _, err := app.EditAgent(c.ID, in); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	saved, _ := app.store.GetCard(c.ID)
	if saved.RefinedPrompt != "old" || saved.Model != c.Model || saved.MaxTurns != c.MaxTurns {
		t.Errorf("a refused edit changed the card: %+v", saved)
	}
	if code, _ := patchAgent(t, app, c.ID, `{"model":"sonnet","max_turns":12}`); code != 200 {
		t.Errorf("valid edit over HTTP = %d", code)
	}
}

func TestEditAgentUsedByNextRun(t *testing.T) {
	dir := testDir(t)
	app := newTestApp(t, dir)
	c := readyCard(t, app, dir, "first prompt")
	if _, err := app.EditAgent(c.ID, AgentParams{Prompt: strp("second prompt"), Model: strp("haiku"), MaxTurns: intp(4), MaxMinutes: intp(2)}); err != nil {
		t.Fatal(err)
	}
	if err := app.Run(c.ID); err != nil {
		t.Fatal(err)
	}
	got := waitStatus(t, app, c.ID, StatusReview)
	if got.RefinedPrompt != "second prompt" || got.Model != "haiku" || got.MaxTurns != 4 || got.MaxMinutes != 2 {
		t.Errorf("card after run: %+v", got)
	}
}
