package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestStartAllReady covers POST /api/cards/run?status=ready: only that exact
// query starts anything, every Ready card starts, and other columns stay put.
func TestStartAllReady(t *testing.T) {
	dir := testDir(t)
	app := newTestApp(t, dir)
	r1 := readyCard(t, app, dir, "first bulk card")
	r2 := readyCard(t, app, dir, "second bulk card")
	todo := newCard(t, app, dir, "stays in todo")

	for _, q := range []string{"?status=todo", "", "?status=Ready", "?status=done"} {
		var e map[string]string
		if code := sendJSON(t, "POST", app.baseURL+"/api/cards/run"+q, nil, &e); code != 400 || e["error"] == "" {
			t.Errorf("POST /api/cards/run%s = %d %v, want 400", q, code, e)
		}
	}
	for _, c := range []*Card{r1, r2, todo} {
		if got, _ := app.store.GetCard(c.ID); got.Status != c.Status {
			t.Errorf("rejected start changed card %d: %s → %s", c.ID, c.Status, got.Status)
		}
	}

	var got struct {
		Started int               `json:"started"`
		Errors  map[string]string `json:"errors"`
	}
	if code := sendJSON(t, "POST", app.baseURL+"/api/cards/run?status=ready", nil, &got); code != 200 || got.Started != 2 || len(got.Errors) != 0 {
		t.Fatalf("start all = %d %+v, want 200 {started:2}", code, got)
	}
	for _, c := range []*Card{r1, r2} {
		if c := waitStatus(t, app, c.ID, StatusReview); c.SessionID == "" {
			t.Errorf("card %d reached review without a session", c.ID)
		}
	}
	if c, _ := app.store.GetCard(todo.ID); c.Status != StatusTodo {
		t.Errorf("todo card is %s, want todo", c.Status)
	}

	got.Started, got.Errors = -1, nil
	if code := sendJSON(t, "POST", app.baseURL+"/api/cards/run?status=ready", nil, &got); code != 200 || got.Started != 0 {
		t.Errorf("empty start all = %d %+v, want 200 {started:0}", code, got)
	}
}

// TestAutoRunAfterRefine: a card with auto_run goes from refinement straight to
// a run; one without stops in Ready for review.
func TestAutoRunAfterRefine(t *testing.T) {
	dir := testDir(t)
	app := newTestApp(t, dir)

	auto := newCard(t, app, dir, "auto run me")
	auto.AutoRun = true
	if err := app.store.SaveCard(auto); err != nil {
		t.Fatal(err)
	}
	manual := newCard(t, app, dir, "review me first")

	for _, c := range []*Card{auto, manual} {
		if err := app.Refine(c.ID); err != nil {
			t.Fatal(err)
		}
	}
	c := waitStatus(t, app, auto.ID, StatusReview)
	if !strings.Contains(c.RefinedPrompt, "auto run me") || c.SessionID == "" {
		t.Errorf("auto-run card not refined and run: prompt %q session %q", c.RefinedPrompt, c.SessionID)
	}
	if !hasNote(t, app, auto.ID, "auto-run") {
		t.Errorf("auto-run card has no auto-run note in its log")
	}

	waitStatus(t, app, manual.ID, StatusReady)
	time.Sleep(300 * time.Millisecond) // give a wrongful auto-run time to show
	if c, _ := app.store.GetCard(manual.ID); c.Status != StatusReady || c.SessionID != "" {
		t.Errorf("card without auto_run left Ready: %s session %q", c.Status, c.SessionID)
	}
}

// TestAutoRunSkippedOnFailedRefine: a refinement that fails sends the card to
// Failed, auto_run or not, and no session is ever started.
func TestAutoRunSkippedOnFailedRefine(t *testing.T) {
	dir := testDir(t)
	app := newTestApp(t, dir)
	broken := filepath.Join(dir, "broken-claude.sh")
	if err := os.WriteFile(broken, []byte("#!/usr/bin/env bash\necho 'refinement exploded' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	app.claudeBin = broken

	c := newCard(t, app, dir, "never runs")
	c.AutoRun = true
	if err := app.store.SaveCard(c); err != nil {
		t.Fatal(err)
	}
	if err := app.Refine(c.ID); err != nil {
		t.Fatal(err)
	}
	c = waitStatus(t, app, c.ID, StatusFailed)
	time.Sleep(300 * time.Millisecond)
	if c, _ := app.store.GetCard(c.ID); c.Status != StatusFailed || c.SessionID != "" || c.RefinedPrompt != "" {
		t.Errorf("failed refinement ran anyway: %s session %q", c.Status, c.SessionID)
	}
	if hasNote(t, app, c.ID, "auto-run") {
		t.Errorf("failed refinement logged an auto-run")
	}
}

// TestAutoRunUsesSavedCard: turning auto_run off while the card is
// being refined wins, because autoRun reads the card as saved after refinement.
func TestAutoRunUsesSavedCard(t *testing.T) {
	dir := testDir(t)
	app := newTestApp(t, dir)
	c := newCard(t, app, dir, "changed my mind")
	c.AutoRun = true
	c.Status = StatusReady
	c.RefinedPrompt = "do it"
	if err := app.store.SaveCard(c); err != nil {
		t.Fatal(err)
	}
	app.update(c.ID, func(c *Card) { c.AutoRun = false })
	saved, _ := app.store.GetCard(c.ID)
	app.autoRun(saved)
	if got, _ := app.store.GetCard(c.ID); got.Status != StatusReady {
		t.Errorf("auto_run off: card is %s, want ready", got.Status)
	}

	// While the board shuts down nothing is started either.
	saved.AutoRun = true
	close(app.quit)
	app.autoRun(saved)
	if got, _ := app.store.GetCard(c.ID); got.Status != StatusReady {
		t.Errorf("shutting down: card is %s, want ready", got.Status)
	}
}

func TestAutoRunRoundTrip(t *testing.T) {
	dir := testDir(t)
	app := newTestApp(t, dir)

	// Store.
	c := newCard(t, app, dir, "store")
	c.AutoRun = true
	if err := app.store.SaveCard(c); err != nil {
		t.Fatal(err)
	}
	if got, _ := app.store.GetCard(c.ID); !got.AutoRun {
		t.Errorf("auto_run lost by SaveCard/GetCard")
	}

	// Settings.
	st := defaultSettings()
	st.DefaultRootFolder = dir
	st.DefaultAutoRun = true
	if code := sendJSON(t, "PUT", app.baseURL+"/api/settings", st, nil); code != 200 {
		t.Fatalf("PUT settings = %d", code)
	}
	var gotSt Settings
	sendJSON(t, "GET", app.baseURL+"/api/settings", nil, &gotSt)
	if !gotSt.DefaultAutoRun {
		t.Errorf("default_auto_run lost by PUT/GET settings")
	}

	// New cards are seeded from the setting, unless they say otherwise.
	var seeded, explicit Card
	if code := sendJSON(t, "POST", app.baseURL+"/api/cards", map[string]any{"title": "t"}, &seeded); code != 201 || !seeded.AutoRun {
		t.Errorf("POST card = %d auto_run %v, want seeded true", code, seeded.AutoRun)
	}
	if code := sendJSON(t, "POST", app.baseURL+"/api/cards", map[string]any{"title": "t", "auto_run": false}, &explicit); code != 201 || explicit.AutoRun {
		t.Errorf("POST card auto_run:false = %d auto_run %v", code, explicit.AutoRun)
	}

	// PATCH on a todo card.
	var patched Card
	url := fmt.Sprintf("%s/api/cards/%d", app.baseURL, explicit.ID)
	if code := sendJSON(t, "PATCH", url, map[string]any{"auto_run": true}, &patched); code != 200 || !patched.AutoRun {
		t.Errorf("PATCH auto_run = %d %v", code, patched.AutoRun)
	}
	if got, _ := app.store.GetCard(explicit.ID); !got.AutoRun {
		t.Errorf("PATCH auto_run not saved")
	}
}

// hasNote reports whether the card's log has a board note containing s.
func hasNote(t *testing.T, app *App, id int64, s string) bool {
	t.Helper()
	evs, err := app.store.Events(id, 500)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range evs {
		if e.Kind == "board" && strings.Contains(string(e.Data), s) {
			return true
		}
	}
	return false
}

// TestRefineAllTodo covers POST /api/cards/refine?status=todo: only that exact query refines anything, every
// eligible Todo card is refined, and cards that skip refinement, chat cards and other columns stay put.
func TestRefineAllTodo(t *testing.T) {
	dir := testDir(t)
	app := newTestApp(t, dir)
	url := app.baseURL + "/api/cards/refine"

	type result struct {
		Refined int               `json:"refined"`
		Errors  map[string]string `json:"errors"`
	}
	for _, q := range []string{"?status=ready", "", "?status=Todo"} {
		var e map[string]string
		if code := sendJSON(t, "POST", url+q, nil, &e); code != 400 || e["error"] == "" {
			t.Errorf("POST /api/cards/refine%s = %d %v, want 400", q, code, e)
		}
	}
	var got result
	if code := sendJSON(t, "POST", url+"?status=todo", nil, &got); code != 200 || got.Refined != 0 || len(got.Errors) != 0 {
		t.Fatalf("refine all with no cards = %d %+v, want 200 {refined:0}", code, got)
	}

	t1 := newCard(t, app, dir, "first todo")
	t2 := newCard(t, app, dir, "second todo")
	skip := newCard(t, app, dir, "skips refinement")
	skip.SkipRefinement = true
	chat := newCard(t, app, dir, "a chat")
	chat.Kind = KindChat
	ready := readyCard(t, app, dir, "already ready")
	for _, c := range []*Card{skip, chat} {
		if err := app.store.SaveCard(c); err != nil {
			t.Fatal(err)
		}
	}
	prompt := ready.RefinedPrompt

	got = result{}
	if code := sendJSON(t, "POST", url+"?status=todo", nil, &got); code != 200 || got.Refined != 2 || len(got.Errors) != 0 {
		t.Fatalf("refine all = %d %+v, want 200 {refined:2}", code, got)
	}
	for _, c := range []*Card{t1, t2} {
		if c := waitStatus(t, app, c.ID, StatusReady); c.RefinedPrompt == "" {
			t.Errorf("card %d refined without a prompt", c.ID)
		}
	}
	for _, c := range []*Card{skip, chat} {
		if got, _ := app.store.GetCard(c.ID); got.Status != StatusTodo || got.RefinedPrompt != "" {
			t.Errorf("card %d (%s) was changed: %s %q", c.ID, c.Description, got.Status, got.RefinedPrompt)
		}
	}
	if got, _ := app.store.GetCard(ready.ID); got.Status != StatusReady || got.RefinedPrompt != prompt {
		t.Errorf("ready card changed: %s %q", got.Status, got.RefinedPrompt)
	}

	got = result{}
	if code := sendJSON(t, "POST", url+"?status=todo", nil, &got); code != 200 || got.Refined != 0 {
		t.Errorf("second refine all = %d %+v, want 200 {refined:0}", code, got)
	}
}

// TestRefineAllTodoReportsFailure: a card whose refine is refused is reported and does not stop the others.
func TestRefineAllTodoReportsFailure(t *testing.T) {
	dir := testDir(t)
	app := newTestApp(t, dir)
	good := newCard(t, app, dir, "good card")
	bad := newCard(t, app, t.TempDir(), "outside the sandbox")
	app.guard.root = dir // the card whose root is elsewhere is refused
	last := newCard(t, app, dir, "another good card")

	var got struct {
		Refined int               `json:"refined"`
		Errors  map[string]string `json:"errors"`
	}
	if code := sendJSON(t, "POST", app.baseURL+"/api/cards/refine?status=todo", nil, &got); code != 200 || got.Refined != 2 {
		t.Fatalf("refine all = %d %+v, want 200 {refined:2}", code, got)
	}
	if got.Errors[itoa(bad.ID)] == "" || len(got.Errors) != 1 {
		t.Errorf("errors = %v, want only card %d", got.Errors, bad.ID)
	}
	for _, c := range []*Card{good, last} {
		waitStatus(t, app, c.ID, StatusReady)
	}
	if c, _ := app.store.GetCard(bad.ID); c.Status != StatusFailed {
		t.Errorf("refused card is %s, want failed", c.Status)
	}
}
