package board

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"maestrello/internal/model"
	"maestrello/internal/testenv"
)

// aheadFixture is repoFixture with an unpushed commit (UNPUSHED.txt) on svc/a's local main.
func aheadFixture(t *testing.T) (dir, root, repo string, app *App) {
	t.Helper()
	dir, root, app = repoFixture(t)
	repo = filepath.Join(root, "svc/a")
	os.WriteFile(filepath.Join(repo, "UNPUSHED.txt"), []byte("x\n"), 0o644)
	testenv.Git(t, repo, "add", "-A")
	testenv.Git(t, repo, "commit", "-qm", "unpushed merge")
	return dir, root, repo, app
}

func answerBase(t *testing.T, app *App, id int64, answer string) {
	t.Helper()
	in := waitOpenInput(t, app, id)
	if in.Kind != "base" {
		t.Fatalf("input kind = %q, want base", in.Kind)
	}
	if err := app.Answer(id, in.ID, answer, false); err != nil {
		t.Fatal(err)
	}
}

func TestBaseAskedWhenLocalIsAhead(t *testing.T) {
	_, root, _, app := aheadFixture(t)
	c := readyCard(t, app, root, "WRITE", "svc/a")
	if err := app.Run(c.ID); err != nil {
		t.Fatal(err)
	}
	in := waitOpenInput(t, app, c.ID)
	if got, _ := app.store.GetCard(c.ID); in.Kind != "base" || got.Status != model.StatusNeedsInput {
		t.Fatalf("input %q, card %s; want a base question in needs_input", in.Kind, got.Status)
	}
	if !strings.Contains(string(in.Payload), "1 ahead") || !strings.Contains(string(in.Payload), `"origin/main"`) {
		t.Errorf("payload = %s", in.Payload)
	}
	// Answers outside the offered options are refused and leave the question open.
	if err := app.Answer(c.ID, in.ID, "bogus", false); err == nil {
		t.Error("bogus answer accepted")
	}
	answerBase(t, app, c.ID, "local")
	c = waitStatus(t, app, c.ID, model.StatusReview)

	r := mustRepos(t, app, c.ID)[0]
	if r.BaseRef != "main" {
		t.Errorf("base = %q, want main", r.BaseRef)
	}
	if _, err := os.Stat(filepath.Join(r.WorktreePath, "UNPUSHED.txt")); err != nil {
		t.Errorf("worktree lacks the unpushed commit: %v", err)
	}
	if d, err := app.Diffs(c.ID); err != nil || d[0].Error != "" || strings.Contains(d[0].Diff, "UNPUSHED") {
		t.Errorf("diff against a local base: %+v, %v", d, err)
	}
	// Merging into a local base works, and a reopened card branches from it again.
	if err := app.Approve(c.ID, map[int64]string{r.ID: "merge"}); err != nil {
		t.Fatal(err)
	}
	if r = mustRepos(t, app, c.ID)[0]; r.Result != "merged into main" {
		t.Errorf("result = %q", r.Result)
	}
	card, _ := app.store.GetCard(c.ID)
	if err := app.reopenRepos(card); err != nil {
		t.Fatal(err)
	}
	r = mustRepos(t, app, c.ID)[0]
	if _, err := os.Stat(filepath.Join(r.WorktreePath, "CHANGED.txt")); err != nil || r.BaseRef != "main" {
		t.Errorf("reopened at %q: %v", r.BaseRef, err)
	}
}

func TestBaseOriginChoice(t *testing.T) {
	_, root, _, app := aheadFixture(t)
	c := readyCard(t, app, root, "WRITE", "svc/a")
	app.Run(c.ID)
	answerBase(t, app, c.ID, "origin")
	waitStatus(t, app, c.ID, model.StatusReview)
	r := mustRepos(t, app, c.ID)[0]
	if r.BaseRef != "origin/main" {
		t.Errorf("base = %q", r.BaseRef)
	}
	if _, err := os.Stat(filepath.Join(r.WorktreePath, "UNPUSHED.txt")); err == nil {
		t.Error("worktree has the unpushed commit")
	}
}

func TestBaseFetchFailureRetry(t *testing.T) {
	dir, root, app := repoFixture(t)
	repo := filepath.Join(root, "svc/a")
	origin := filepath.Join(dir, "origins", "svc/a.git")
	testenv.Git(t, repo, "remote", "set-url", "origin", origin+".gone")
	c := readyCard(t, app, root, "WRITE", "svc/a")
	app.Run(c.ID)
	in := waitOpenInput(t, app, c.ID)
	if !strings.Contains(string(in.Payload), "Fetching origin failed") {
		t.Errorf("payload = %s", in.Payload)
	}
	// With the remote back, a retry finds local and origin equal and goes on unasked.
	testenv.Git(t, repo, "remote", "set-url", "origin", origin)
	app.Answer(c.ID, in.ID, "retry", false)
	waitStatus(t, app, c.ID, model.StatusReview)
	if r := mustRepos(t, app, c.ID)[0]; r.BaseRef != "origin/main" {
		t.Errorf("base = %q", r.BaseRef)
	}
}

func TestBaseAskedInsideAddRepo(t *testing.T) {
	_, root, _, app := aheadFixture(t)
	c := readyCard(t, app, root, "WRITE ADD:svc/a")
	app.Run(c.ID)
	in := waitOpenInput(t, app, c.ID)
	if app.liveRun(c.ID) == nil || in.Kind != "base" {
		t.Fatalf("want a live session waiting on a base question, got %q", in.Kind)
	}
	app.Answer(c.ID, in.ID, "local", false)
	waitStatus(t, app, c.ID, model.StatusReview)
	if r := mustRepos(t, app, c.ID)[0]; r.BaseRef != "main" || r.Branch == "" {
		t.Errorf("repo = %+v", r)
	}
}

func TestGitFailureOffersInPlace(t *testing.T) {
	_, root, app := repoFixture(t)
	repo := filepath.Join(root, "svc/a")
	c := readyCard(t, app, root, "WRITE", "svc/a")
	testenv.Git(t, repo, "branch", branchName(c)) // the card branch exists: worktree add fails
	app.Run(c.ID)
	in := waitOpenInput(t, app, c.ID)
	if !strings.Contains(string(in.Payload), "Checking out svc/a failed") {
		t.Errorf("payload = %s", in.Payload)
	}
	app.Answer(c.ID, in.ID, "inplace", false)
	waitStatus(t, app, c.ID, model.StatusReview)
	r := mustRepos(t, app, c.ID)[0]
	if !r.Local() || r.WorktreePath != repo || r.Branch != "" {
		t.Errorf("repo = %+v, want local work in %s", r, repo)
	}
}

func TestDiscardWhileAskingBase(t *testing.T) {
	_, root, _, app := aheadFixture(t)
	c := readyCard(t, app, root, "WRITE", "svc/a")
	app.Run(c.ID)
	waitOpenInput(t, app, c.ID)
	if err := app.Discard(c.ID); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, app, c.ID, model.StatusDone)
}
