package board

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"maestrello/internal/gitops"
	"maestrello/internal/model"
)

type RepoDiff struct {
	Repo  *model.CardRepo `json:"repo"`
	Log   string          `json:"log"`
	Diff  string          `json:"diff"`
	Error string          `json:"error,omitempty"`
}

func (a *App) Diffs(id int64) ([]RepoDiff, error) {
	repos, err := a.store.Repos(id)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	out := []RepoDiff{}
	for _, r := range repos {
		if r.WorktreePath == "" || r.Result != "" {
			continue
		}
		d := RepoDiff{Repo: r}
		if r.Local() {
			d.Log, d.Diff, err = a.localChanges(r)
			if err != nil {
				d.Error = err.Error()
			}
			out = append(out, d)
			continue
		}
		d.Log, d.Diff, err = a.git.Diff(ctx, r.WorktreePath, r.BaseRef)
		if err != nil {
			d.Error = err.Error()
		}
		out = append(out, d)
	}
	return out, nil
}

// RepoOutcome is what approving did to one repo. OK is false when the action failed: the repo
// then keeps its worktree and branch, and Error says why.
type RepoOutcome struct {
	RepoID int64  `json:"repo_id"`
	Path   string `json:"repo_path"`
	OK     bool   `json:"ok"`
	Result string `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`
	PRURL  string `json:"pr_url,omitempty"`
}

// ApproveError reports an approval that left the card in Review because repo actions failed.
type ApproveError struct{ Repos []RepoOutcome }

func (e *ApproveError) Error() string {
	var failed []string
	for _, o := range e.Repos {
		if !o.OK {
			failed = append(failed, o.Path+": "+o.Error)
		}
	}
	return "the card stays in Review, these repos failed: " + strings.Join(failed, "; ")
}

// Approve applies a per-repo action (merge, local, push, pr, create_pr), removes the worktrees and
// closes the card, unless a repo action fails: then the card stays in Review with that repo
// open, and approving again retries only the repos not closed yet. Repos without changes are
// dropped. A plan card creates all of its proposed tasks as cards.
func (a *App) Approve(id int64, actions map[int64]string) error {
	return a.ApproveSelected(id, actions, nil)
}

// ApproveSelected is Approve for a plan card with a choice of tasks: the indices (from 0) of
// the proposed tasks to create as Todo cards. nil means all of them, an empty list none.
// Cards of other kinds have no tasks, so for them a selection is an error.
func (a *App) ApproveSelected(id int64, actions map[int64]string, tasks []int) error {
	_, _, err := a.approveSelected(id, actions, tasks)
	return err
}

// ApproveAndRefine is ApproveSelected followed by refining every Todo card the plan created.
// Approval errors stop before any refinement; refinement errors are returned after all cards
// were tried (the plan itself is already approved by then).
func (a *App) ApproveAndRefine(id int64, actions map[int64]string, tasks []int) error {
	_, err := a.approveAndRefine(id, actions, tasks, true)
	return err
}

// approveAndRefine also returns the per-repo outcomes (nil when approval stopped before the repos).
func (a *App) approveAndRefine(id int64, actions map[int64]string, tasks []int, refine bool) ([]RepoOutcome, error) {
	made, outcomes, err := a.approveSelected(id, actions, tasks)
	if err != nil || !refine {
		return outcomes, err
	}
	var errs []error
	for _, c := range made {
		if c.EffectiveKind() == model.KindChat {
			continue // a chat is never refined
		}
		if err := a.Refine(c.ID); err != nil {
			errs = append(errs, fmt.Errorf("#%d: %w", c.ID, err))
		}
	}
	return outcomes, errors.Join(errs...)
}

var repoActions = map[string]bool{"local": true, "merge": true, "push": true, "pr": true, "create_pr": true}

func (a *App) approveSelected(id int64, actions map[int64]string, tasks []int) ([]*model.Card, []RepoOutcome, error) {
	if a.liveRun(id) != nil {
		return nil, nil, fmt.Errorf("the agent is still running")
	}
	card, err := a.store.GetCard(id)
	if err != nil {
		return nil, nil, err
	}
	if card.Status != model.StatusReview {
		return nil, nil, fmt.Errorf("card is %s; only review cards can be approved", card.Status)
	}
	if tasks != nil && card.EffectiveKind() != model.KindPlan {
		return nil, nil, fmt.Errorf("only a plan card has tasks to select")
	}
	for _, act := range actions {
		if act != "" && !repoActions[act] {
			return nil, nil, fmt.Errorf("unknown action %q", act)
		}
	}
	// Repos first: a failed repo keeps the card in Review, and a retry must not create the plan's cards twice.
	outcomes, err := a.closeRepos(card, actions)
	if err != nil {
		return nil, outcomes, err
	}
	if failed := failedRepos(outcomes); failed > 0 {
		a.note(id, "approval incomplete, %d repo(s) failed and keep their worktree and branch: %s", failed, (&ApproveError{outcomes}).Error())
		a.update(id, func(c *model.Card) { c.Activity = fmt.Sprintf("approval incomplete: %d repo(s) failed", failed) })
		return nil, outcomes, &ApproveError{outcomes}
	}
	var made []*model.Card
	if card.EffectiveKind() == model.KindPlan {
		// The board creates the cards, never the agent, and only now that I approved.
		if made, err = a.createPlanCards(card, tasks); err != nil {
			if len(made) > 0 {
				a.note(id, "plan approval stopped after creating %s: %v", cardRefs(made), err)
			}
			return nil, outcomes, err
		}
		if len(made) > 0 {
			a.note(id, "plan approved: created %s", cardRefs(made))
		} else {
			a.note(id, "plan approved: no tasks selected, no cards created")
		}
	}
	a.cleanupWorkspace(id)
	a.releaseLeases(id)
	a.update(id, func(c *model.Card) { c.Status = model.StatusDone; c.Activity = "approved" })
	return made, outcomes, nil
}

func failedRepos(outcomes []RepoOutcome) (n int) {
	for _, o := range outcomes {
		if !o.OK {
			n++
		}
	}
	return n
}

// cardRefs lists cards as "#3, #4".
func cardRefs(cards []*model.Card) string {
	refs := make([]string, len(cards))
	for i, c := range cards {
		refs[i] = fmt.Sprintf("#%d", c.ID)
	}
	return strings.Join(refs, ", ")
}

// closeRepos commits leftovers, applies the per-repo action (falling back to the card's
// push default, then local), and removes the worktrees of the repos that succeeded. A failed
// repo keeps worktree and branch, with the reason in r.Error. Repos without changes are
// dropped. It returns the outcome of every checked-out repo, earlier closes included.
func (a *App) closeRepos(card *model.Card, actions map[int64]string) ([]RepoOutcome, error) {
	repos, err := a.store.Repos(card.ID)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	var out []RepoOutcome
	for _, r := range repos {
		if r.WorktreePath == "" {
			continue
		}
		if r.Result == "" {
			a.closeRepo(ctx, card, r, actions[r.ID])
			if err := a.store.SaveRepo(r); err != nil {
				return out, err
			}
			if r.Error != "" {
				a.note(card.ID, "%s: failed: %s", r.RepoPath, r.Error)
			} else {
				a.note(card.ID, "%s: %s", r.RepoPath, r.Result)
			}
		}
		out = append(out, RepoOutcome{RepoID: r.ID, Path: r.RepoPath, OK: r.Result != "", Result: r.Result, Error: r.Error, PRURL: r.PRURL})
	}
	return out, nil
}

// closeRepo closes one open repo: on success r.Result is set and r.Error cleared; on failure
// r.Error says why and the worktree, branch and result stay as they were.
func (a *App) closeRepo(ctx context.Context, card *model.Card, r *model.CardRepo, action string) {
	r.Error = ""
	if r.Local() {
		r.Result = "kept in place (local work)"
		return
	}
	fail := func(format string, args ...any) { r.Error = fmt.Sprintf(format, args...) }
	orig := filepath.Join(card.RootFolder, r.RepoPath)
	if _, err := a.git.CommitAll(ctx, r.WorktreePath, "maestrello: uncommitted changes for "+card.Title); err != nil {
		fail("commit leftovers: %v", err)
		return
	}
	ahead, err := a.git.AheadCount(ctx, r.WorktreePath, r.BaseRef)
	if err != nil {
		fail("%v", err)
		return
	}
	if ahead == 0 {
		if err := a.git.RemoveWorktree(ctx, orig, r.WorktreePath); err != nil {
			fail("%v", err)
			return
		}
		a.git.DeleteBranch(ctx, orig, r.Branch)
		r.Result = "dropped (no changes)"
		return
	}

	if action == "" {
		action = card.PushDefault
	}
	if action == "" {
		action = "local"
	}
	merged := false
	switch action {
	case "local":
		r.Result = "kept local on " + r.Branch
	case "merge":
		if err := a.mergeLocal(ctx, card, r, orig); err != nil {
			fail("merge not done, branch %s kept: %v", r.Branch, err)
			return
		}
		merged = true
	case "push", "pr", "create_pr":
		if err := a.pushAndPR(ctx, card, r, orig, action); err != nil {
			fail("%v", err)
			return
		}
	default:
		fail("unknown action %q", action)
		return
	}
	if err := a.git.RemoveWorktree(ctx, orig, r.WorktreePath); err != nil {
		r.Result = "" // the action is done, but the repo is not closed until the worktree is gone
		fail("%v", err)
		return
	}
	if merged { // the branch is free once its worktree is gone
		if err := a.git.DeleteBranch(ctx, orig, r.Branch); err != nil {
			r.Result += "; could not delete " + r.Branch + ": " + err.Error()
		}
	}
}

// pushAndPR runs the push and PR steps of an action. A branch that was pushed before is not
// pushed again, so a retry after a failed PR only runs the PR step. create_pr never pushes.
func (a *App) pushAndPR(ctx context.Context, card *model.Card, r *model.CardRepo, orig, action string) error {
	if !r.Pushed {
		if action == "create_pr" {
			return fmt.Errorf("%s is not pushed yet: push it first", r.Branch)
		}
		if err := a.git.Push(ctx, orig, r.Branch); err != nil {
			return fmt.Errorf("push: %w", err)
		}
		r.Pushed = true
		a.store.SaveRepo(r) // remembered even if the PR step fails below
	}
	r.Result = "pushed " + r.Branch
	if action == "push" {
		return nil
	}
	body := card.Summary + "\n\n---\nCreated from maestrello card #" + fmt.Sprint(card.ID)
	url, err := a.git.CreatePR(ctx, orig, r.Branch, r.BaseRef, card.Title, body)
	if err != nil {
		r.Result = ""
		return fmt.Errorf("%s is pushed but the PR failed: %w", r.Branch, err)
	}
	r.PRURL, r.Result = url, "PR opened"
	return nil
}

// reopenRepos undoes closeRepos for a card leaving Done: closed local-work repos are
// claimed again, and every closed git repo gets its worktree back at the stored path, on
// the card branch if it was kept, else on a new branch from the stored base (an origin base is fetched again first, which may fail harmlessly).
func (a *App) reopenRepos(card *model.Card) error {
	repos, err := a.store.Repos(card.ID)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	for _, r := range repos {
		if r.WorktreePath == "" || r.Result == "" {
			continue
		}
		if !r.Local() {
			orig := filepath.Join(card.RootFolder, r.RepoPath)
			if err := os.MkdirAll(a.workDir(card.ID), 0o755); err != nil {
				return err
			}
			if r.Branch == "" {
				r.Branch = branchName(card)
			}
			if a.git.BranchExists(ctx, orig, r.Branch) {
				err = a.git.AddWorktreeExisting(ctx, orig, r.WorktreePath, r.Branch)
			} else {
				err = a.reopenBase(ctx, orig, r)
			}
			if err != nil {
				return fmt.Errorf("%s: reopen worktree: %w", r.RepoPath, err)
			}
			r.OrigStatus, _ = a.git.Status(ctx, orig)
		}
		r.Result, r.PRURL, r.Error, r.Pushed = "", "", "", false
		if err := a.store.SaveRepo(r); err != nil {
			return err
		}
		a.note(card.ID, "%s: reopened", r.RepoPath)
	}
	return nil
}

// reopenBase adds a worktree for r on a new branch from r.BaseRef: a local branch as it is
// now, an origin one after a fetch whose failure is ignored (the last fetched state serves).
func (a *App) reopenBase(ctx context.Context, orig string, r *model.CardRepo) error {
	if r.BaseRef == "" {
		return errors.New("no base recorded to branch from")
	}
	if strings.HasPrefix(r.BaseRef, "origin/") {
		a.git.Fetch(ctx, orig)
	}
	if !a.git.RefExists(ctx, orig, r.BaseRef) {
		return fmt.Errorf("base %s no longer exists", r.BaseRef)
	}
	return a.git.AddWorktree(ctx, orig, r.WorktreePath, r.Branch, r.BaseRef)
}

// mergeLocal merges the card branch into the base branch in the original checkout, only when
// that checkout is on the base branch and has no uncommitted changes to tracked files. It never
// pushes. It sets r.Result on success; otherwise it returns why nothing was merged and the
// branch is kept (a conflict is aborted and also noted on the card). The caller deletes a merged branch.
func (a *App) mergeLocal(ctx context.Context, card *model.Card, r *model.CardRepo, orig string) error {
	base := strings.TrimPrefix(r.BaseRef, "origin/") // the local branch of an origin base; a local base has no prefix
	cur, err := a.git.CurrentBranch(ctx, orig)
	if err != nil {
		return err
	}
	if cur != base {
		if cur == "" {
			cur = "a detached HEAD"
		}
		return fmt.Errorf("the checkout is on %s, not %s", cur, base)
	}
	clean, err := a.git.TrackedClean(ctx, orig)
	if err != nil {
		return err
	}
	if !clean {
		return errors.New("the checkout has uncommitted changes")
	}
	if err := a.git.Merge(ctx, orig, r.Branch, fmt.Sprintf("maestrello: merge #%d %s", card.ID, card.Title)); err != nil {
		if errors.Is(err, gitops.ErrMergeConflict) {
			a.note(card.ID, "%s: merging %s into %s conflicted and was aborted: %v", r.RepoPath, r.Branch, base, err)
			return fmt.Errorf("merge conflict, aborted: %w", err)
		}
		return err
	}
	r.Result = "merged into " + base
	return nil
}

// Discard throws away all of the card's worktrees and branches and closes the card.
func (a *App) Discard(id int64) error {
	if run := a.liveRun(id); run != nil {
		return fmt.Errorf("the agent is still running; stop it first")
	}
	a.cancelWait(id)     // a card waiting for a folder never started
	a.releaseWaiters(id) // nor one still asking how to check out a repo
	card, err := a.store.GetCard(id)
	if err != nil {
		return err
	}
	repos, err := a.store.Repos(id)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	var errs []string
	for _, r := range repos {
		if r.WorktreePath == "" || r.Result != "" {
			continue
		}
		if r.Local() {
			r.Result = "left in place: local work is not under git, so it cannot be undone"
			a.store.SaveRepo(r)
			a.note(id, "%s: %s", r.RepoPath, r.Result)
			continue
		}
		orig := filepath.Join(card.RootFolder, r.RepoPath)
		if err := a.git.RemoveWorktree(ctx, orig, r.WorktreePath); err != nil {
			errs = append(errs, err.Error())
			continue
		}
		if err := a.git.DeleteBranch(ctx, orig, r.Branch); err != nil {
			errs = append(errs, err.Error())
		}
		r.Result = "discarded"
		a.store.SaveRepo(r)
	}
	if len(errs) > 0 {
		return fmt.Errorf("discard: %s", strings.Join(errs, "; "))
	}
	if open, _ := a.store.OpenInput(id); open != nil {
		a.store.AnswerInput(open.ID, "discard")
	}
	a.cleanupWorkspace(id)
	a.releaseLeases(id)
	a.update(id, func(c *model.Card) { c.Status = model.StatusDone; c.Activity = "discarded"; c.WaitingOn = 0 })
	return nil
}

func (a *App) cleanupWorkspace(id int64) {
	dir := a.workDir(id)
	if a.guard.Check(dir) == nil {
		os.RemoveAll(dir)
	}
	os.Remove(a.mcpConfigPath(id))
}

// localChanges lists the files in a local-work folder modified since the card
// started using it. Without git there is no diff; other cards or the user
// editing the same folder show up here too.
func (a *App) localChanges(r *model.CardRepo) (summary, files string, err error) {
	since, err := time.Parse(time.RFC3339Nano, r.LocalSince)
	if err != nil {
		return "", "", err
	}
	if err := a.guard.Check(r.WorktreePath); err != nil {
		return "", "", err
	}
	const max = 500
	var changed []string
	err = filepath.WalkDir(r.WorktreePath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if path != r.WorktreePath && (gitops.SkipDirs[d.Name()] || strings.HasPrefix(d.Name(), ".") || path == a.dataDir || a.git.IsRepo(path)) {
				return filepath.SkipDir
			}
			return nil
		}
		if info, err := d.Info(); err == nil && info.ModTime().After(since) {
			rel, _ := filepath.Rel(r.WorktreePath, path)
			changed = append(changed, rel)
			if len(changed) >= max {
				return fs.SkipAll
			}
		}
		return nil
	})
	summary = fmt.Sprintf("local work in %s (not a git repo): %d file(s) changed in place since %s", r.WorktreePath, len(changed), since.Local().Format("Jan 2 15:04"))
	if len(changed) >= max {
		summary += fmt.Sprintf(" (showing the first %d)", max)
	}
	return summary, strings.Join(changed, "\n"), err
}
