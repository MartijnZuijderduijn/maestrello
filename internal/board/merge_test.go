package board

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"maestrello/internal/model"
	"maestrello/internal/testenv"
)

// mergeCard runs a card that adds CHANGED.txt to svc/a and returns it in review, with the
// original checkout and the branch name.
func mergeCard(t *testing.T) (app *App, c *model.Card, repo, branch string) {
	t.Helper()
	_, root, app := repoFixture(t)
	c = readyCard(t, app, root, "WRITE", "svc/a")
	if err := app.Run(c.ID); err != nil {
		t.Fatal(err)
	}
	c = waitStatus(t, app, c.ID, model.StatusReview)
	return app, c, filepath.Join(root, "svc/a"), branchName(c)
}

func approveMerge(t *testing.T, app *App, c *model.Card) *model.CardRepo {
	t.Helper()
	r := mustRepos(t, app, c.ID)[0]
	if err := app.Approve(c.ID, map[int64]string{r.ID: "merge"}); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, app, c.ID, model.StatusDone)
	return mustRepos(t, app, c.ID)[0]
}

func hasBranch(t *testing.T, repo, branch string) bool {
	t.Helper()
	return testenv.Git(t, repo, "branch", "--list", branch) != ""
}

func TestApproveMergeFastForward(t *testing.T) {
	app, c, repo, branch := mergeCard(t)
	before := testenv.Git(t, repo, "rev-parse", "main")
	r := approveMerge(t, app, c)
	if r.Result != "merged into main" {
		t.Errorf("result = %q", r.Result)
	}
	if _, err := os.Stat(filepath.Join(repo, "CHANGED.txt")); err != nil {
		t.Errorf("main lacks the change: %v", err)
	}
	if hasBranch(t, repo, branch) {
		t.Error("merged branch not deleted")
	}
	if testenv.Git(t, repo, "rev-parse", "main") == before {
		t.Error("main did not move")
	}
	if n := testenv.Git(t, repo, "rev-list", "--merges", "--count", "main"); n != "0" {
		t.Errorf("fast-forward made %s merge commits", n)
	}
	if st := testenv.Git(t, repo, "status", "--porcelain"); st != "" {
		t.Errorf("checkout dirty: %s", st)
	}
	// Never pushed.
	origin := filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(repo))), "origins", "svc/a.git")
	if testenv.Git(t, origin, "rev-parse", "main") != before {
		t.Error("origin main moved: merge must not push")
	}
}

func TestApproveMergeCommit(t *testing.T) {
	app, c, repo, _ := mergeCard(t)
	os.WriteFile(filepath.Join(repo, "other.txt"), []byte("x\n"), 0o644)
	testenv.Git(t, repo, "add", "other.txt")
	testenv.Git(t, repo, "commit", "-qm", "diverge")
	r := approveMerge(t, app, c)
	if r.Result != "merged into main" {
		t.Fatalf("result = %q", r.Result)
	}
	if msg := testenv.Git(t, repo, "log", "-1", "--format=%s", "main"); !strings.HasPrefix(msg, "maestrello: merge #") {
		t.Errorf("merge commit subject = %q", msg)
	}
}

func TestApproveMergeRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		setup func(t *testing.T, repo string)
		want  string
	}{
		"dirty": {func(t *testing.T, repo string) {
			os.WriteFile(filepath.Join(repo, "README.md"), []byte("edited\n"), 0o644)
		}, "uncommitted changes"},
		"other branch": {func(t *testing.T, repo string) {
			testenv.Git(t, repo, "checkout", "-q", "-b", "elsewhere")
		}, "on elsewhere, not main"},
	} {
		t.Run(name, func(t *testing.T) {
			app, c, repo, branch := mergeCard(t)
			tc.setup(t, repo)
			head := testenv.Git(t, repo, "rev-parse", "HEAD")
			r := approveMerge(t, app, c)
			if !strings.HasPrefix(r.Result, "kept local on "+branch+": not merged, ") || !strings.Contains(r.Result, tc.want) {
				t.Errorf("result = %q", r.Result)
			}
			if !hasBranch(t, repo, branch) {
				t.Error("branch was not kept")
			}
			if testenv.Git(t, repo, "rev-parse", "HEAD") != head {
				t.Error("checkout moved")
			}
			if testenv.Git(t, repo, "rev-parse", "main") != testenv.Git(t, repo, "rev-parse", "origin/main") {
				t.Error("main changed")
			}
		})
	}
}

func TestApproveMergeConflict(t *testing.T) {
	app, c, repo, branch := mergeCard(t)
	os.WriteFile(filepath.Join(repo, "CHANGED.txt"), []byte("conflicting\n"), 0o644)
	testenv.Git(t, repo, "add", "CHANGED.txt")
	testenv.Git(t, repo, "commit", "-qm", "conflicting change")
	main := testenv.Git(t, repo, "rev-parse", "main")

	r := approveMerge(t, app, c)
	if !strings.Contains(r.Result, "merge conflict") || !strings.HasPrefix(r.Result, "kept local on "+branch) {
		t.Errorf("result = %q", r.Result)
	}
	if testenv.Git(t, repo, "rev-parse", "main") != main {
		t.Error("main changed")
	}
	if !hasBranch(t, repo, branch) {
		t.Error("branch was not kept")
	}
	if got, _ := os.ReadFile(filepath.Join(repo, "CHANGED.txt")); string(got) != "conflicting\n" {
		t.Errorf("CHANGED.txt = %q", got)
	}
	if st := testenv.Git(t, repo, "status", "--porcelain"); st != "" {
		t.Errorf("merge not aborted cleanly: %s", st)
	}
	if _, err := os.Stat(filepath.Join(repo, ".git", "MERGE_HEAD")); err == nil {
		t.Error("MERGE_HEAD left behind")
	}
	found := false
	for _, n := range mustNotes(t, app, c.ID) {
		found = found || strings.Contains(n, "conflicted")
	}
	if !found {
		t.Error("no card note about the conflict")
	}
}

func mustNotes(t *testing.T, app *App, id int64) []string {
	t.Helper()
	evs, err := app.store.Events(id, 1000)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range evs {
		if e.Kind == "board" {
			out = append(out, string(e.Data))
		}
	}
	return out
}
