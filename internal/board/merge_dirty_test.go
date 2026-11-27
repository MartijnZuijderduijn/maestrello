package board

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"maestrello/internal/model"
	"maestrello/internal/settings"
	"maestrello/internal/testenv"
)

func setMergeDirty(t *testing.T, app *App, strategy string) {
	t.Helper()
	st, err := app.store.Settings()
	if err != nil {
		t.Fatal(err)
	}
	st.MergeDirty = strategy
	if err := app.store.SaveSettings(st); err != nil {
		t.Fatal(err)
	}
}

func write(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// approveWith approves the card's only repo with the merge action and an optional strategy override.
func approveWith(t *testing.T, app *App, c *model.Card, strategy string) error {
	t.Helper()
	r := mustRepos(t, app, c.ID)[0]
	var strategies map[int64]string
	if strategy != "" {
		strategies = map[int64]string{r.ID: strategy}
	}
	_, _, err := app.approveSelected(c.ID, map[int64]string{r.ID: "merge"}, strategies, nil)
	return err
}

// editInWorktree makes the card branch also change README.md.
func editInWorktree(t *testing.T, app *App, c *model.Card, text string) {
	t.Helper()
	write(t, filepath.Join(mustRepos(t, app, c.ID)[0].WorktreePath, "README.md"), text)
}

func TestMergeDirtyDefaultIsWorktree(t *testing.T) {
	app, c, _, _ := mergeCard(t)
	if got := app.mergeDirtyDefault(c); got != model.MergeWorktree {
		t.Errorf("default = %q", got)
	}
}

func TestMergeDirtyWorktreeFastForward(t *testing.T) {
	app, c, repo, branch := mergeCard(t)
	write(t, filepath.Join(repo, "README.md"), "my edit\n")
	if err := approveWith(t, app, c, ""); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, app, c.ID, model.StatusDone)
	if r := mustRepos(t, app, c.ID)[0]; r.Result != "merged into main" {
		t.Errorf("result = %q", r.Result)
	}
	if read(t, filepath.Join(repo, "README.md")) != "my edit\n" {
		t.Error("the user's edit was lost")
	}
	if _, err := os.Stat(filepath.Join(repo, "CHANGED.txt")); err != nil {
		t.Errorf("working tree lacks the merged file: %v", err)
	}
	if st := testenv.Git(t, repo, "status", "--porcelain"); st != "M README.md" && st != " M README.md" {
		t.Errorf("status = %q, want only the user's edit", st)
	}
	if n := testenv.Git(t, repo, "rev-list", "--merges", "--count", "main"); n != "0" {
		t.Errorf("fast-forward made %s merge commits", n)
	}
	if hasBranch(t, repo, branch) {
		t.Error("merged branch not deleted")
	}
}

func TestMergeDirtyWorktreeMergeCommit(t *testing.T) {
	app, c, repo, _ := mergeCard(t)
	write(t, filepath.Join(repo, "other.txt"), "x\n")
	testenv.Git(t, repo, "add", "other.txt")
	testenv.Git(t, repo, "commit", "-qm", "diverge")
	write(t, filepath.Join(repo, "README.md"), "my edit\n")
	testenv.Git(t, repo, "add", "README.md") // staged edits count as the user's too
	if err := approveWith(t, app, c, ""); err != nil {
		t.Fatal(err)
	}
	if msg := testenv.Git(t, repo, "log", "-1", "--format=%s", "main"); !strings.HasPrefix(msg, "maestrello: merge #") {
		t.Errorf("merge commit subject = %q", msg)
	}
	if st := testenv.Git(t, repo, "status", "--porcelain"); st != "M  README.md" {
		t.Errorf("status = %q, want only the staged edit", st)
	}
	if read(t, filepath.Join(repo, "CHANGED.txt")) == "" {
		t.Error("working tree lacks the merged file")
	}
}

func TestMergeDirtyWorktreeRefusesOverlap(t *testing.T) {
	app, c, repo, branch := mergeCard(t)
	editInWorktree(t, app, c, "from the card\n")
	write(t, filepath.Join(repo, "README.md"), "my edit\n")
	main := testenv.Git(t, repo, "rev-parse", "main")
	err := approveWith(t, app, c, "")
	if err == nil || !strings.Contains(err.Error(), "touches files you changed") || !strings.Contains(err.Error(), "README.md") {
		t.Fatalf("err = %v", err)
	}
	if testenv.Git(t, repo, "rev-parse", "main") != main || read(t, filepath.Join(repo, "README.md")) != "my edit\n" {
		t.Error("the checkout or main changed")
	}
	if !hasBranch(t, repo, branch) {
		t.Error("branch was not kept")
	}
	if got, _ := app.store.GetCard(c.ID); got.Status != model.StatusReview {
		t.Errorf("card is %s, want review", got.Status)
	}
}

func TestMergeDirtyWorktreeRefusesUntrackedInTheWay(t *testing.T) {
	app, c, repo, _ := mergeCard(t)
	write(t, filepath.Join(repo, "README.md"), "my edit\n")
	write(t, filepath.Join(repo, "CHANGED.txt"), "mine\n") // the branch adds this file
	if err := approveWith(t, app, c, ""); err == nil || !strings.Contains(err.Error(), "CHANGED.txt") {
		t.Fatalf("err = %v", err)
	}
	if read(t, filepath.Join(repo, "CHANGED.txt")) != "mine\n" {
		t.Error("untracked file overwritten")
	}
}

func TestMergeDirtyWorktreeOtherBranch(t *testing.T) {
	app, c, repo, _ := mergeCard(t)
	testenv.Git(t, repo, "checkout", "-q", "-b", "elsewhere")
	write(t, filepath.Join(repo, "README.md"), "my edit\n")
	if err := approveWith(t, app, c, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(repo, "CHANGED.txt")); err == nil {
		t.Error("the checkout on another branch was touched")
	}
	if testenv.Git(t, repo, "show", "main:CHANGED.txt") == "" {
		t.Error("main lacks the change")
	}
	if testenv.Git(t, repo, "branch", "--show-current") != "elsewhere" || read(t, filepath.Join(repo, "README.md")) != "my edit\n" {
		t.Error("checkout changed")
	}
}

func TestMergeDirtyWorktreeConflict(t *testing.T) {
	app, c, repo, _ := mergeCard(t)
	write(t, filepath.Join(repo, "CHANGED.txt"), "conflicting\n")
	testenv.Git(t, repo, "add", "CHANGED.txt")
	testenv.Git(t, repo, "commit", "-qm", "conflicting change")
	write(t, filepath.Join(repo, "README.md"), "my edit\n")
	main := testenv.Git(t, repo, "rev-parse", "main")
	if err := approveWith(t, app, c, ""); err == nil || !strings.Contains(err.Error(), "merge conflict") {
		t.Fatalf("err = %v", err)
	}
	if testenv.Git(t, repo, "rev-parse", "main") != main || testenv.Git(t, repo, "status", "--porcelain") != "M README.md" {
		t.Errorf("checkout changed: %s", testenv.Git(t, repo, "status", "--porcelain"))
	}
}

func TestMergeDirtyStash(t *testing.T) {
	app, c, repo, _ := mergeCard(t)
	setMergeDirty(t, app, model.MergeStash)
	write(t, filepath.Join(repo, "README.md"), "my edit\n")
	if err := approveWith(t, app, c, ""); err != nil {
		t.Fatal(err)
	}
	if r := mustRepos(t, app, c.ID)[0]; r.Result != "merged into main" {
		t.Errorf("result = %q", r.Result)
	}
	if read(t, filepath.Join(repo, "README.md")) != "my edit\n" {
		t.Error("the user's edit was not restored")
	}
	if got := testenv.Git(t, repo, "stash", "list"); got != "" {
		t.Errorf("stash left behind: %s", got)
	}
}

func TestMergeDirtyStashRestoreConflictKeepsStash(t *testing.T) {
	app, c, repo, branch := mergeCard(t)
	setMergeDirty(t, app, model.MergeStash)
	editInWorktree(t, app, c, "from the card\n")
	write(t, filepath.Join(repo, "README.md"), "my edit\n")
	err := approveWith(t, app, c, "")
	if err == nil || !strings.Contains(err.Error(), "stash@{0}") || !strings.Contains(err.Error(), "git stash apply") {
		t.Fatalf("err = %v", err)
	}
	if n := testenv.Git(t, repo, "stash", "list"); strings.Count(n, "\n") != 0 || n == "" {
		t.Errorf("stash list = %q, want the one kept stash", n)
	}
	if testenv.Git(t, repo, "status", "--porcelain") != "" {
		t.Error("checkout not restored to a clean state")
	}
	if got := testenv.Git(t, repo, "stash", "show", "-p", "stash@{0}"); !strings.Contains(got, "my edit") {
		t.Errorf("stash lost the edit: %s", got)
	}
	if testenv.Git(t, repo, "show", "main:README.md") != "from the card" {
		t.Error("the merge itself should have happened")
	}
	// The branch is merged and the checkout is clean, so approving again finishes the card.
	if err := approveWith(t, app, c, ""); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, app, c.ID, model.StatusDone)
	if hasBranch(t, repo, branch) || testenv.Git(t, repo, "stash", "list") == "" {
		t.Error("branch should be gone and the stash kept")
	}
}

func TestMergeDirtyStashOtherBranchRefused(t *testing.T) {
	app, c, repo, _ := mergeCard(t)
	setMergeDirty(t, app, model.MergeStash)
	testenv.Git(t, repo, "checkout", "-q", "-b", "elsewhere")
	write(t, filepath.Join(repo, "README.md"), "my edit\n")
	if err := approveWith(t, app, c, ""); err == nil || !strings.Contains(err.Error(), "on elsewhere, not main") {
		t.Fatalf("err = %v", err)
	}
	if testenv.Git(t, repo, "stash", "list") != "" {
		t.Error("stashed although it refused")
	}
}

func TestMergeDirtyRefuse(t *testing.T) {
	app, c, repo, _ := mergeCard(t)
	setMergeDirty(t, app, model.MergeRefuse)
	write(t, filepath.Join(repo, "README.md"), "my edit\n")
	if err := approveWith(t, app, c, ""); err == nil || !strings.Contains(err.Error(), "uncommitted changes") {
		t.Fatalf("err = %v", err)
	}
}

// The approve dialog's choice beats the setting.
func TestMergeDirtyOverrideAtApproval(t *testing.T) {
	app, c, repo, _ := mergeCard(t)
	setMergeDirty(t, app, model.MergeRefuse)
	write(t, filepath.Join(repo, "README.md"), "my edit\n")
	if err := approveWith(t, app, c, model.MergeWorktree); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, app, c.ID, model.StatusDone)
}

func TestMergeDirtyUnknownStrategy(t *testing.T) {
	app, c, _, _ := mergeCard(t)
	if err := approveWith(t, app, c, "force"); err == nil || !strings.Contains(err.Error(), "unknown merge_dirty") {
		t.Errorf("err = %v", err)
	}
}

func TestMergeDirtyQueue(t *testing.T) {
	app, c, repo, branch := mergeCard(t)
	setMergeDirty(t, app, model.MergeQueue)
	write(t, filepath.Join(repo, "README.md"), "my edit\n")
	r := mustRepos(t, app, c.ID)[0]
	_, outcomes, err := app.approveSelected(c.ID, map[int64]string{r.ID: "merge"}, nil, nil)
	if err != nil {
		t.Fatalf("a queued merge is not a failure: %v", err)
	}
	if len(outcomes) != 1 || !outcomes[0].Pending || outcomes[0].OK {
		t.Errorf("outcomes = %+v", outcomes)
	}
	r = mustRepos(t, app, c.ID)[0]
	if !r.MergePending || r.Result != "" || r.Error != "" {
		t.Errorf("repo = %+v", r)
	}
	if got, _ := app.store.GetCard(c.ID); got.Status != model.StatusReview {
		t.Fatalf("card is %s, want review", got.Status)
	}
	if !hasBranch(t, repo, branch) {
		t.Fatal("branch not kept")
	}

	app.retryPendingMerges() // still dirty: nothing happens
	if got, _ := app.store.GetCard(c.ID); got.Status != model.StatusReview || testenv.Git(t, repo, "rev-parse", "main") != testenv.Git(t, repo, "rev-parse", "origin/main") {
		t.Fatal("merged although the checkout is dirty")
	}

	testenv.Git(t, repo, "checkout", "--", "README.md") // the user cleans up
	app.retryPendingMerges()
	waitStatus(t, app, c.ID, model.StatusDone)
	r = mustRepos(t, app, c.ID)[0]
	if r.Result != "merged into main" || r.MergePending {
		t.Errorf("repo = %+v", r)
	}
	if hasBranch(t, repo, branch) {
		t.Error("merged branch not deleted")
	}
	if _, err := os.Stat(filepath.Join(repo, "CHANGED.txt")); err != nil {
		t.Errorf("main lacks the change: %v", err)
	}
}

// Choosing another action for a queued repo cancels the queue.
func TestMergeDirtyQueueCancelledByAnotherAction(t *testing.T) {
	app, c, repo, _ := mergeCard(t)
	setMergeDirty(t, app, model.MergeQueue)
	write(t, filepath.Join(repo, "README.md"), "my edit\n")
	if err := approveWith(t, app, c, ""); err != nil {
		t.Fatal(err)
	}
	r := mustRepos(t, app, c.ID)[0]
	if err := app.Approve(c.ID, map[int64]string{r.ID: "local"}); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, app, c.ID, model.StatusDone)
	if r = mustRepos(t, app, c.ID)[0]; r.MergePending || !strings.HasPrefix(r.Result, "kept local") {
		t.Errorf("repo = %+v", r)
	}
}

func TestMergeDirtyResolvesPerLayer(t *testing.T) {
	app, c, _, _ := mergeCard(t)
	stash := model.MergeStash
	p := &model.Project{Name: "p", RootFolder: c.RootFolder}
	if err := app.store.CreateProject(p); err != nil {
		t.Fatal(err)
	}
	if err := app.store.SaveProjectSettings(p.ID, model.LayerSettings{Overrides: model.Overrides{MergeDirty: &stash}}); err != nil {
		t.Fatal(err)
	}
	st, src, err := settings.Resolve(app.store, p.ID, "code")
	if err != nil || st.MergeDirty != model.MergeStash || src["merge_dirty"] != settings.LayerProject {
		t.Errorf("resolved = %q from %q, err %v", st.MergeDirty, src["merge_dirty"], err)
	}
}

// POST /api/cards/<id>/approve takes the per-repo strategy, and a queued merge answers 200.
func TestApproveHTTPMergeDirty(t *testing.T) {
	app, c, repo, _ := mergeCard(t)
	write(t, filepath.Join(repo, "README.md"), "my edit\n")
	r := mustRepos(t, app, c.ID)[0]
	id := itoa(r.ID)
	var bad map[string]any
	if code := sendJSON(t, "POST", app.baseURL+"/api/cards/"+itoa(c.ID)+"/approve",
		map[string]any{"actions": map[string]string{id: "merge"}, "merge_dirty": map[string]string{id: "force"}}, &bad); code == 200 {
		t.Errorf("unknown strategy accepted: %v", bad)
	}
	var out struct {
		OK    bool          `json:"ok"`
		Repos []RepoOutcome `json:"repos"`
	}
	if code := sendJSON(t, "POST", app.baseURL+"/api/cards/"+itoa(c.ID)+"/approve",
		map[string]any{"actions": map[string]string{id: "merge"}, "merge_dirty": map[string]string{id: "queue"}}, &out); code != 200 ||
		!out.OK || len(out.Repos) != 1 || !out.Repos[0].Pending {
		t.Errorf("approve = %d %+v", code, out)
	}
	if got, _ := app.store.GetCard(c.ID); got.Status != model.StatusReview {
		t.Errorf("card is %s, want review", got.Status)
	}
}

// The diff shows what the approve dialog needs: whether the checkout is dirty and the resolved default.
func TestDiffsReportDirtyCheckout(t *testing.T) {
	app, c, repo, _ := mergeCard(t)
	setMergeDirty(t, app, model.MergeStash)
	if d, err := app.Diffs(c.ID); err != nil || len(d) != 1 || d[0].Dirty || d[0].MergeDirty != model.MergeStash {
		t.Fatalf("clean: %+v %v", d, err)
	}
	write(t, filepath.Join(repo, "README.md"), "my edit\n")
	if d, _ := app.Diffs(c.ID); !d[0].Dirty {
		t.Error("dirty checkout not reported")
	}
}
