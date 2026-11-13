package board

import (
	"bytes"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"maestrello/internal/model"
	"maestrello/internal/testenv"
)

func move(t *testing.T, app *App, id int64, to string) *model.Card {
	t.Helper()
	if err := app.Move(id, to); err != nil {
		t.Fatalf("move to %s: %v", to, err)
	}
	return waitStatus(t, app, id, to)
}

// Dropping on Done keeps the work: committed on the card branch, locally, never pushed.
func assertClosedLocally(t *testing.T, dir, root string, app *App, c *model.Card) {
	t.Helper()
	branch := branchName(c)
	repo := filepath.Join(root, "svc/a")
	if files := testenv.Git(t, repo, "show", "--name-only", "--format=", branch); !strings.Contains(files, "CHANGED.txt") {
		t.Errorf("agent change not on local branch %s: %q", branch, files)
	}
	if b := testenv.Git(t, filepath.Join(dir, "origins", "svc/a.git"), "branch", "--list", branch); b != "" {
		t.Errorf("manual move pushed %s", branch)
	}
	for _, r := range mustRepos(t, app, c.ID) {
		if _, err := os.Stat(r.WorktreePath); !os.IsNotExist(err) {
			t.Errorf("worktree %s not removed", r.WorktreePath)
		}
		if !strings.HasPrefix(r.Result, "kept local") {
			t.Errorf("result = %q", r.Result)
		}
	}
	if _, err := os.Stat(app.workDir(c.ID)); !os.IsNotExist(err) {
		t.Error("workspace not cleaned up")
	}
}

func TestMoveNeedsInputToDone(t *testing.T) {
	dir, root, app := repoFixture(t)
	c := readyCard(t, app, root, "WRITE LOOP", "svc/a")
	c.MaxTurns = 3
	app.store.SaveCard(c)
	app.Run(c.ID)
	waitStatus(t, app, c.ID, model.StatusNeedsInput)

	c = move(t, app, c.ID, model.StatusDone)
	if in, _ := app.store.OpenInput(c.ID); in != nil {
		t.Errorf("input still open after move: %+v", in)
	}
	assertClosedLocally(t, dir, root, app, c)
}

func TestMoveReviewToDone(t *testing.T) {
	dir, root, app := repoFixture(t)
	c := readyCard(t, app, root, "WRITE", "svc/a")
	c.PushDefault = "push" // a drag must not push, whatever the card's default
	app.store.SaveCard(c)
	app.Run(c.ID)
	waitStatus(t, app, c.ID, model.StatusReview)

	c = move(t, app, c.ID, model.StatusDone)
	assertClosedLocally(t, dir, root, app, c)
}

func TestMoveIntoEveryColumn(t *testing.T) {
	app := newTestApp(t, testenv.Dir(t))
	c := readyCard(t, app, app.dataDir, "plain")
	for _, to := range []string{model.StatusTodo, model.StatusReview, model.StatusNeedsInput, model.StatusFailed, model.StatusReady, model.StatusRefining} {
		move(t, app, c.ID, to)
	}
	// Refining runs the refinement, which ends in Ready.
	waitStatus(t, app, c.ID, model.StatusReady)
	// Running starts the agent, which reports done.
	if err := app.Move(c.ID, model.StatusRunning); err != nil {
		t.Fatal(err)
	}
	c = waitStatus(t, app, c.ID, model.StatusReview)
	if c.Summary != "fake work done" {
		t.Errorf("summary = %q", c.Summary)
	}
	move(t, app, c.ID, model.StatusDone)
}

func TestMoveEndpoint(t *testing.T) {
	app := newTestApp(t, testenv.Dir(t))
	c := newCard(t, app, app.dataDir, "")
	c.Status = model.StatusNeedsInput
	app.store.SaveCard(c)
	app.store.CreateInput(c.ID, "question", map[string]string{"question": "?"})

	resp, err := http.Post(app.baseURL+"/api/cards/"+fmt.Sprint(c.ID)+"/move", "application/json", bytes.NewBufferString(`{"status":"done"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if c, _ := app.store.GetCard(c.ID); c.Status != model.StatusDone {
		t.Errorf("status = %s, want done", c.Status)
	}
}

func TestMoveRefusals(t *testing.T) {
	_, root, app := repoFixture(t)

	todo := newCard(t, app, root, "")
	if err := app.Move(todo.ID, "bogus"); err == nil {
		t.Error("unknown column accepted")
	}
	if err := app.Move(todo.ID, model.StatusReady); err != nil {
		t.Errorf("todo card without refined prompt not moved to ready: %v", err)
	}
	if got, _ := app.store.GetCard(todo.ID); got.Status != model.StatusReady || got.RefinedPrompt == "" {
		t.Errorf("card = %s %q, want ready with the card text as prompt", got.Status, got.RefinedPrompt)
	}

	c := readyCard(t, app, root, "LOOP", "svc/a")
	app.Run(c.ID)
	for app.liveRun(c.ID) == nil {
	}
	if err := app.Move(c.ID, model.StatusDone); err == nil {
		t.Error("moved a card with a live session")
	}
	app.Stop(c.ID)
	waitStatus(t, app, c.ID, model.StatusFailed)

	move(t, app, c.ID, model.StatusDone)
	if err := app.Move(c.ID, model.StatusReady); err == nil {
		t.Error("moved a closed card whose worktrees are gone")
	}
}

// Without the opt-in a live session is never killed by a move.
func TestMoveLiveRunNeedsOptIn(t *testing.T) {
	_, root, app := repoFixture(t)
	c := readyCard(t, app, root, "LOOP", "svc/a")
	app.Run(c.ID)
	for app.liveRun(c.ID) == nil {
	}
	if err := app.Move(c.ID, model.StatusTodo); err == nil {
		t.Error("moved a card with a live session without the opt-in")
	}
	run := app.liveRun(c.ID)
	if run == nil {
		t.Fatal("the run was killed by a refused move")
	}
	select {
	case <-run.done:
		t.Error("the run ended after a refused move")
	default:
	}
	if c, _ := app.store.GetCard(c.ID); c.Status != model.StatusRunning {
		t.Errorf("status = %s, want running", c.Status)
	}
	app.Stop(c.ID)
	waitStatus(t, app, c.ID, model.StatusFailed)
}

// With the opt-in the run is stopped, and the dragged-to column wins over the stop's Failed.
func TestMoveStoppingLiveRun(t *testing.T) {
	_, root, app := repoFixture(t)
	for _, to := range []string{model.StatusTodo, model.StatusReady, model.StatusReview, model.StatusDone} {
		c := readyCard(t, app, root, "LOOP", "svc/a")
		app.Run(c.ID)
		for app.liveRun(c.ID) == nil {
		}
		if err := app.MoveStopping(c.ID, to); err != nil {
			t.Fatalf("move to %s: %v", to, err)
		}
		if app.liveRun(c.ID) != nil {
			t.Errorf("%s: run still live", to)
		}
		c = waitStatus(t, app, c.ID, to)
		time.Sleep(200 * time.Millisecond) // nothing may overwrite the move afterwards
		if c, _ = app.store.GetCard(c.ID); c.Status != to {
			t.Errorf("status = %s, want %s", c.Status, to)
		}
	}
}

// Moves that would not happen anyway must not kill the agent.
func TestMoveStoppingKeepsRunOnNoOp(t *testing.T) {
	_, root, app := repoFixture(t)
	c := readyCard(t, app, root, "LOOP", "svc/a")
	app.Run(c.ID)
	for app.liveRun(c.ID) == nil {
	}
	if err := app.MoveStopping(c.ID, model.StatusRunning); err != nil {
		t.Errorf("same-column move: %v", err)
	}
	if err := app.MoveStopping(c.ID, "bogus"); err == nil {
		t.Error("unknown column accepted")
	}
	if app.liveRun(c.ID) == nil {
		t.Error("run was stopped by a no-op or invalid move")
	}
	app.Stop(c.ID)
	waitStatus(t, app, c.ID, model.StatusFailed)
}

func TestMoveEndpointStop(t *testing.T) {
	_, root, app := repoFixture(t)
	c := readyCard(t, app, root, "LOOP", "svc/a")
	app.Run(c.ID)
	for app.liveRun(c.ID) == nil {
	}
	post := func(body string) int {
		resp, err := http.Post(app.baseURL+"/api/cards/"+fmt.Sprint(c.ID)+"/move", "application/json", bytes.NewBufferString(body))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := post(`{"status":"todo"}`); code == 200 {
		t.Error("endpoint moved a live card without stop")
	}
	if app.liveRun(c.ID) == nil {
		t.Fatal("run killed without stop flag")
	}
	if code := post(`{"status":"todo","stop":true}`); code != 200 {
		t.Fatalf("status %d", code)
	}
	waitStatus(t, app, c.ID, model.StatusTodo)
}
