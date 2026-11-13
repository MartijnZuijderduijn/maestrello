package board

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"maestrello/internal/model"
	"maestrello/internal/testenv"
)

// waitIdle waits until the card's session has fully finished.
func waitIdle(t *testing.T, app *App, id int64) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); app.liveRun(id) != nil; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("session still live")
		}
	}
}

func plainRoot(t *testing.T) (dir, root string) {
	dir = testenv.Dir(t)
	root = filepath.Join(dir, "plain")
	os.MkdirAll(filepath.Join(root, "src"), 0o755)
	os.WriteFile(filepath.Join(root, "src", "old.txt"), []byte("old\n"), 0o644)
	return dir, root
}

func TestLocalWorkInPlainFolder(t *testing.T) {
	dir, root := plainRoot(t)
	app := newTestApp(t, dir)
	c := readyCard(t, app, root, "LOCAL")
	app.Run(c.ID)
	c = waitStatus(t, app, c.ID, model.StatusReview)
	waitIdle(t, app, c.ID)

	repos := mustRepos(t, app, c.ID)
	if len(repos) != 1 || repos[0].RepoPath != "." || !repos[0].Local() || repos[0].Branch != "" {
		t.Fatalf("repos = %+v, want one local entry for the root", repos)
	}
	if _, err := os.Stat(filepath.Join(root, "LOCAL.txt")); err != nil {
		t.Fatal("agent did not work in the root folder:", err)
	}

	diffs, err := app.Diffs(c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(diffs) != 1 || diffs[0].Diff != "LOCAL.txt" || !strings.Contains(diffs[0].Log, "local work") {
		t.Errorf("diffs = %+v", diffs)
	}

	if err := app.Approve(c.ID, nil); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, app, c.ID, model.StatusDone)
	if r := mustRepos(t, app, c.ID)[0]; !strings.Contains(r.Result, "kept in place") {
		t.Errorf("result = %q", r.Result)
	}
	if _, err := os.Stat(filepath.Join(root, "LOCAL.txt")); err != nil {
		t.Error("approve removed the local work")
	}
	// local work does not block moving a closed card back
	if err := app.Move(c.ID, model.StatusReview); err != nil {
		t.Errorf("move back from done: %v", err)
	}
}

func TestLocalWorkDiscardLeavesFiles(t *testing.T) {
	dir, root := plainRoot(t)
	app := newTestApp(t, dir)
	c := readyCard(t, app, root, "LOCAL")
	app.Run(c.ID)
	waitStatus(t, app, c.ID, model.StatusReview)
	waitIdle(t, app, c.ID)
	if err := app.Discard(c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "LOCAL.txt")); err != nil {
		t.Error("discard deleted local work")
	}
	if r := mustRepos(t, app, c.ID)[0]; !strings.Contains(r.Result, "cannot be undone") {
		t.Errorf("result = %q", r.Result)
	}
}

func TestAddRepoDotMixesLocalAndWorktrees(t *testing.T) {
	_, root, app := repoFixture(t)
	c := readyCard(t, app, root, "WRITE ADD:.", "svc/a")
	app.Run(c.ID)
	c = waitStatus(t, app, c.ID, model.StatusReview)
	waitIdle(t, app, c.ID)

	got := map[string]*model.CardRepo{}
	for _, r := range mustRepos(t, app, c.ID) {
		got[r.RepoPath] = r
	}
	if r := got["."]; r == nil || !r.Local() || r.WorktreePath != root {
		t.Errorf(". = %+v, want local work in the root", r)
	}
	if r := got["svc/a"]; r == nil || r.Local() || r.Branch == "" {
		t.Errorf("svc/a = %+v, want a worktree", r)
	}
	if c.Warning != "" {
		t.Errorf("warning = %q", c.Warning)
	}
}

func TestNoAutoLocalWhenRootHoldsRepos(t *testing.T) {
	_, root, app := repoFixture(t)
	c := readyCard(t, app, root, "LOCAL")
	app.Run(c.ID)
	waitStatus(t, app, c.ID, model.StatusReview)
	waitIdle(t, app, c.ID)
	if repos := mustRepos(t, app, c.ID); len(repos) != 0 {
		t.Errorf("repos = %+v; a folder of repos must not become local work by default", repos)
	}
	if _, err := os.Stat(filepath.Join(root, "LOCAL.txt")); err == nil {
		t.Error("agent wrote into the root of a folder of repos")
	}
}

func TestDataDirIsNeverLocalWork(t *testing.T) {
	app := newTestApp(t, testenv.Dir(t))
	c := readyCard(t, app, app.dataDir, "nothing")
	if _, err := app.checkoutRepo(c, "."); err == nil || !strings.Contains(err.Error(), "data folder") {
		t.Errorf("checkout of the data dir = %v", err)
	}
	app.Run(c.ID)
	waitStatus(t, app, c.ID, model.StatusReview)
	if repos := mustRepos(t, app, c.ID); len(repos) != 0 {
		t.Errorf("repos = %+v", repos)
	}
}

func TestCheckoutRejectsEscapes(t *testing.T) {
	dir, root := plainRoot(t)
	app := newTestApp(t, dir)
	c := newCard(t, app, root, "")
	for _, p := range []string{"..", "../x", "src/../../x", "/etc", "missing"} {
		if _, err := app.checkoutRepo(c, p); err == nil {
			t.Errorf("checkoutRepo(%q) accepted", p)
		}
	}
}

func TestAllowedRateLimitEventIsIgnored(t *testing.T) {
	app := newTestApp(t, testenv.Dir(t))
	c := readyCard(t, app, app.dataDir, "RATEOK")
	app.Run(c.ID)
	waitStatus(t, app, c.ID, model.StatusReview)
	waitIdle(t, app, c.ID)
	if c, _ := app.store.GetCard(c.ID); c.Status != model.StatusReview {
		t.Errorf("status = %s", c.Status)
	}
	if in, _ := app.store.OpenInput(c.ID); in != nil {
		t.Errorf("open input %+v for an allowed request", in)
	}
}

func TestReportedDoneBeatsLaterRateLimit(t *testing.T) {
	app := newTestApp(t, testenv.Dir(t))
	c := readyCard(t, app, app.dataDir, "RATEAFTER")
	app.Run(c.ID)
	waitStatus(t, app, c.ID, model.StatusReview)
	waitIdle(t, app, c.ID)
	if c, _ := app.store.GetCard(c.ID); c.Status != model.StatusReview {
		t.Errorf("status = %s, want review: the agent already reported done", c.Status)
	}
}
