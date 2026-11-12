package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These tests drive whole cards through the board with testdata/fake-claude.sh
// standing in for the claude CLI, against fake repos inside testdata/.

func readyCard(t *testing.T, app *App, root, desc string, repos ...string) *Card {
	t.Helper()
	c := newCard(t, app, root, desc)
	if err := app.Refine(c.ID); err != nil {
		t.Fatal(err)
	}
	c = waitStatus(t, app, c.ID, StatusReady)
	if !strings.Contains(c.RefinedPrompt, desc) {
		t.Fatalf("refined prompt %q lacks card text", c.RefinedPrompt)
	}
	if err := app.store.SetPlannedRepos(c.ID, repos); err != nil {
		t.Fatal(err)
	}
	return c
}

func repoFixture(t *testing.T) (dir, root string, app *App) {
	t.Helper()
	dir = testDir(t)
	isolateGit(t, dir)
	makeRepo(t, dir, "svc/a")
	makeRepo(t, dir, "svc/b")
	return dir, filepath.Join(dir, "project"), newTestApp(t, dir)
}

func TestLifecycleApprove(t *testing.T) {
	dir, root, app := repoFixture(t)
	c := readyCard(t, app, root, "WRITE ADD:svc/b", "svc/a")

	if err := app.Run(c.ID); err != nil {
		t.Fatal(err)
	}
	c = waitStatus(t, app, c.ID, StatusReview)
	if c.Warning != "" {
		t.Errorf("unexpected warning: %s", c.Warning)
	}
	if c.SessionID == "" || c.TurnsUsed == 0 {
		t.Errorf("session not tracked: %+v", c)
	}

	repos, _ := app.store.Repos(c.ID)
	if len(repos) != 2 {
		t.Fatalf("repos = %d, want svc/a plus svc/b from add_repo", len(repos))
	}
	diffs, err := app.Diffs(c.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range diffs {
		if !strings.Contains(d.Diff, "CHANGED.txt") {
			t.Errorf("%s diff lacks agent change: %s %s", d.Repo.RepoPath, d.Diff, d.Error)
		}
	}

	actions := map[int64]string{}
	for _, r := range repos {
		actions[r.ID] = map[string]string{"svc/a": "push", "svc/b": "pr"}[r.RepoPath]
	}
	if err := app.Approve(c.ID, actions); err != nil {
		t.Fatal(err)
	}
	c = waitStatus(t, app, c.ID, StatusDone)

	branch := branchName(c)
	gitT(t, filepath.Join(dir, "origins", "svc/a.git"), "rev-parse", "--verify", branch)
	gitT(t, filepath.Join(dir, "origins", "svc/b.git"), "rev-parse", "--verify", branch)
	gitT(t, filepath.Join(root, "svc/a"), "rev-parse", "--verify", branch) // kept locally too
	for _, r := range mustRepos(t, app, c.ID) {
		if r.RepoPath == "svc/b" && !strings.HasPrefix(r.PRURL, "sandbox://pr/") {
			t.Errorf("svc/b PR url = %q", r.PRURL)
		}
	}
	if _, err := os.Stat(app.workDir(c.ID)); !os.IsNotExist(err) {
		t.Error("workspace not cleaned up")
	}
	for _, r := range []string{"svc/a", "svc/b"} {
		if st := gitT(t, filepath.Join(root, r), "status", "--porcelain"); st != "" {
			t.Errorf("original %s changed: %s", r, st)
		}
	}
}

func mustRepos(t *testing.T, app *App, id int64) []*CardRepo {
	t.Helper()
	repos, err := app.store.Repos(id)
	if err != nil {
		t.Fatal(err)
	}
	return repos
}

func TestLifecycleDiscard(t *testing.T) {
	_, root, app := repoFixture(t)
	c := readyCard(t, app, root, "WRITE", "svc/a")
	app.Run(c.ID)
	c = waitStatus(t, app, c.ID, StatusReview)

	if err := app.Discard(c.ID); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, app, c.ID, StatusDone)
	branches := gitT(t, filepath.Join(root, "svc/a"), "branch", "--list", "maestrello/*")
	if branches != "" {
		t.Errorf("branches left after discard: %s", branches)
	}
	if _, err := os.Stat(app.workDir(c.ID)); !os.IsNotExist(err) {
		t.Error("workspace not cleaned up")
	}
}

func TestApproveDropsUnchangedRepos(t *testing.T) {
	_, root, app := repoFixture(t)
	c := readyCard(t, app, root, "nothing to change", "svc/a")
	app.Run(c.ID)
	waitStatus(t, app, c.ID, StatusReview)
	if err := app.Approve(c.ID, nil); err != nil {
		t.Fatal(err)
	}
	r := mustRepos(t, app, c.ID)[0]
	if !strings.HasPrefix(r.Result, "dropped") {
		t.Errorf("result = %q", r.Result)
	}
	if b := gitT(t, filepath.Join(root, "svc/a"), "branch", "--list", "maestrello/*"); b != "" {
		t.Errorf("branch kept for unchanged repo: %s", b)
	}
}

func TestOriginalCheckoutChangeIsFlagged(t *testing.T) {
	_, root, app := repoFixture(t)
	c := readyCard(t, app, root, "TOUCH", "svc/a")
	app.Run(c.ID)
	c = waitStatus(t, app, c.ID, StatusReview)
	if !strings.Contains(c.Warning, "svc/a") {
		t.Errorf("warning = %q, want a flag on svc/a", c.Warning)
	}
}

func TestRunRefusesRepoOutsideRoot(t *testing.T) {
	_, root, app := repoFixture(t)
	c := readyCard(t, app, root, "WRITE", "../origins")
	app.Run(c.ID)
	c = waitStatus(t, app, c.ID, StatusFailed)
	if !strings.Contains(c.FailReason, "invalid repo path") {
		t.Errorf("fail reason = %q", c.FailReason)
	}
}

func TestFailedThenSendBack(t *testing.T) {
	app := newTestApp(t, testDir(t))
	c := readyCard(t, app, app.dataDir, "FAIL")
	app.Run(c.ID)
	c = waitStatus(t, app, c.ID, StatusFailed)
	if !strings.Contains(c.FailReason, "exit status 3") || !strings.Contains(c.FailReason, "fake failure") {
		t.Errorf("fail reason = %q", c.FailReason)
	}
	sessionID := c.SessionID

	if err := app.SendBack(c.ID, "try again"); err != nil {
		t.Fatal(err)
	}
	c = waitStatus(t, app, c.ID, StatusReview)
	if c.SessionID != sessionID {
		t.Errorf("send back started a new session: %s -> %s", sessionID, c.SessionID)
	}
}

func TestTurnLimitThenDiscard(t *testing.T) {
	app := newTestApp(t, testDir(t))
	c := readyCard(t, app, app.dataDir, "LOOP")
	c.MaxTurns = 3
	app.store.SaveCard(c)

	app.Run(c.ID)
	c = waitStatus(t, app, c.ID, StatusNeedsInput)
	in, _ := app.store.OpenInput(c.ID)
	if in == nil || in.Kind != "limit" || !strings.Contains(string(in.Payload), "turn_limit") {
		t.Fatalf("open input = %+v", in)
	}
	if c.TurnsUsed < 3 {
		t.Errorf("turns used = %d", c.TurnsUsed)
	}
	if err := app.Answer(c.ID, in.ID, "discard", false); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, app, c.ID, StatusDone)
}

func TestAskUserLifecycle(t *testing.T) {
	app := newTestApp(t, testDir(t))
	c := readyCard(t, app, app.dataDir, "ASK")
	app.Run(c.ID)
	in := waitOpenInput(t, app, c.ID)
	if app.liveRun(c.ID) == nil {
		t.Fatal("session should stay live while waiting")
	}
	app.Answer(c.ID, in.ID, "purple", false)
	c = waitStatus(t, app, c.ID, StatusReview)
	if c.Summary != "done with answer purple" {
		t.Errorf("summary = %q", c.Summary)
	}
}

func TestStopFailsCard(t *testing.T) {
	app := newTestApp(t, testDir(t))
	c := readyCard(t, app, app.dataDir, "LOOP")
	app.Run(c.ID)
	for app.liveRun(c.ID) == nil {
	}
	if err := app.Stop(c.ID); err != nil {
		t.Fatal(err)
	}
	c = waitStatus(t, app, c.ID, StatusFailed)
	if c.FailReason != "stopped by you" {
		t.Errorf("fail reason = %q", c.FailReason)
	}
}

func TestRecoverAfterRestart(t *testing.T) {
	app := newTestApp(t, testDir(t))
	set := func(status string) int64 {
		c := newCard(t, app, app.dataDir, "")
		c.Status = status
		app.store.SaveCard(c)
		return c.ID
	}
	running, refining, waiting := set(StatusRunning), set(StatusRefining), set(StatusNeedsInput)
	if err := app.recover(); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[int64]string{running: StatusFailed, refining: StatusFailed, waiting: StatusNeedsInput} {
		if c, _ := app.store.GetCard(id); c.Status != want {
			t.Errorf("card %d: %s, want %s", id, c.Status, want)
		}
	}
}

func TestRunOnlyFromReady(t *testing.T) {
	app := newTestApp(t, testDir(t))
	c := newCard(t, app, app.dataDir, "")
	if err := app.Run(c.ID); err == nil {
		t.Error("running a todo card should fail")
	}
	if err := app.Approve(c.ID, nil); err == nil {
		t.Error("approving a todo card should fail")
	}
}
