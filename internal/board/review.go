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

// Approve applies a per-repo action (merge, local, push, pr), removes the worktrees and closes the card.
// Repos without changes are dropped. A plan card creates all of its proposed tasks as cards.
func (a *App) Approve(id int64, actions map[int64]string) error {
	return a.ApproveSelected(id, actions, nil)
}

// ApproveSelected is Approve for a plan card with a choice of tasks: the indices (from 0) of
// the proposed tasks to create as Todo cards. nil means all of them, an empty list none.
// Cards of other kinds have no tasks, so for them a selection is an error.
func (a *App) ApproveSelected(id int64, actions map[int64]string, tasks []int) error {
	_, err := a.approveSelected(id, actions, tasks)
	return err
}

// ApproveAndRefine is ApproveSelected followed by refining every Todo card the plan created.
// Approval errors stop before any refinement; refinement errors are returned after all cards
// were tried (the plan itself is already approved by then).
func (a *App) ApproveAndRefine(id int64, actions map[int64]string, tasks []int) error {
	made, err := a.approveSelected(id, actions, tasks)
	if err != nil {
		return err
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
	return errors.Join(errs...)
}

func (a *App) approveSelected(id int64, actions map[int64]string, tasks []int) ([]*model.Card, error) {
	if a.liveRun(id) != nil {
		return nil, fmt.Errorf("the agent is still running")
	}
	card, err := a.store.GetCard(id)
	if err != nil {
		return nil, err
	}
	if card.Status != model.StatusReview {
		return nil, fmt.Errorf("card is %s; only review cards can be approved", card.Status)
	}
	if tasks != nil && card.EffectiveKind() != model.KindPlan {
		return nil, fmt.Errorf("only a plan card has tasks to select")
	}
	var made []*model.Card
	if card.EffectiveKind() == model.KindPlan {
		// The board creates the cards, never the agent, and only now that I approved.
		if made, err = a.createPlanCards(card, tasks); err != nil {
			if len(made) > 0 {
				a.note(id, "plan approval stopped after creating %s: %v", cardRefs(made), err)
			}
			return nil, err
		}
		if len(made) > 0 {
			a.note(id, "plan approved: created %s", cardRefs(made))
		} else {
			a.note(id, "plan approved: no tasks selected, no cards created")
		}
	}
	if err := a.closeRepos(card, actions); err != nil {
		return nil, err
	}
	a.cleanupWorkspace(id)
	a.releaseLeases(id)
	a.update(id, func(c *model.Card) { c.Status = model.StatusDone; c.Activity = "approved" })
	return made, nil
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
// push default, then local), and removes the worktrees. Repos without changes are dropped.
func (a *App) closeRepos(card *model.Card, actions map[int64]string) error {
	id := card.ID
	repos, err := a.store.Repos(id)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	for _, r := range repos {
		if r.WorktreePath == "" || r.Result != "" {
			continue
		}
		if r.Local() {
			r.Result = "kept in place (local work)"
			a.store.SaveRepo(r)
			a.note(id, "%s: %s", r.RepoPath, r.Result)
			continue
		}
		orig := filepath.Join(card.RootFolder, r.RepoPath)
		if _, err := a.git.CommitAll(ctx, r.WorktreePath, "maestrello: uncommitted changes for "+card.Title); err != nil {
			return fmt.Errorf("%s: commit leftovers: %w", r.RepoPath, err)
		}
		ahead, err := a.git.AheadCount(ctx, r.WorktreePath, r.BaseRef)
		if err != nil {
			return fmt.Errorf("%s: %w", r.RepoPath, err)
		}
		if ahead == 0 {
			if err := a.git.RemoveWorktree(ctx, orig, r.WorktreePath); err != nil {
				return fmt.Errorf("%s: %w", r.RepoPath, err)
			}
			a.git.DeleteBranch(ctx, orig, r.Branch)
			r.Result = "dropped (no changes)"
			a.store.SaveRepo(r)
			continue
		}

		action := actions[r.ID]
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
			merged = a.mergeLocal(ctx, card, r, orig)
		case "push", "pr":
			if err := a.git.Push(ctx, orig, r.Branch); err != nil {
				return fmt.Errorf("%s: push: %w", r.RepoPath, err)
			}
			r.Result = "pushed " + r.Branch
			if action == "pr" {
				body := card.Summary + "\n\n---\nCreated from maestrello card #" + fmt.Sprint(card.ID)
				url, err := a.git.CreatePR(ctx, orig, r.Branch, r.BaseRef, card.Title, body)
				if err != nil {
					r.Result += "; PR failed: " + err.Error()
				} else {
					r.PRURL = url
					r.Result = "PR opened"
				}
			}
		default:
			return fmt.Errorf("unknown action %q", action)
		}
		if err := a.git.RemoveWorktree(ctx, orig, r.WorktreePath); err != nil {
			return fmt.Errorf("%s: %w", r.RepoPath, err)
		}
		if merged { // the branch is free once its worktree is gone
			if err := a.git.DeleteBranch(ctx, orig, r.Branch); err != nil {
				r.Result += "; could not delete " + r.Branch + ": " + err.Error()
			}
		}
		a.store.SaveRepo(r)
		a.note(id, "%s: %s", r.RepoPath, r.Result)
	}
	return nil
}

// reopenRepos undoes closeRepos for a card leaving Done: closed local-work repos are
// claimed again, and every closed git repo gets its worktree back at the stored path, on
// the card branch if it was kept, else on a new branch from the current base.
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
				var base string
				if base, err = a.git.FetchBase(ctx, orig); err == nil {
					if err = a.git.AddWorktree(ctx, orig, r.WorktreePath, r.Branch, base); err == nil {
						r.BaseRef = base
					}
				}
			}
			if err != nil {
				return fmt.Errorf("%s: reopen worktree: %w", r.RepoPath, err)
			}
			r.OrigStatus, _ = a.git.Status(ctx, orig)
		}
		r.Result, r.PRURL = "", ""
		if err := a.store.SaveRepo(r); err != nil {
			return err
		}
		a.note(card.ID, "%s: reopened", r.RepoPath)
	}
	return nil
}

// mergeLocal merges the card branch into the base branch in the original checkout, only when
// that checkout is on the base branch and has no uncommitted changes to tracked files. It never
// pushes. It sets r.Result and reports whether the merge happened; if not, the branch is kept
// (a conflict is aborted and also noted on the card). The caller deletes a merged branch.
func (a *App) mergeLocal(ctx context.Context, card *model.Card, r *model.CardRepo, orig string) bool {
	base := strings.TrimPrefix(r.BaseRef, "origin/")
	keep := func(reason string) bool {
		r.Result = fmt.Sprintf("kept local on %s: not merged, %s", r.Branch, reason)
		return false
	}
	cur, err := a.git.CurrentBranch(ctx, orig)
	if err != nil {
		return keep(err.Error())
	}
	if cur != base {
		if cur == "" {
			cur = "a detached HEAD"
		}
		return keep(fmt.Sprintf("the checkout is on %s, not %s", cur, base))
	}
	clean, err := a.git.TrackedClean(ctx, orig)
	if err != nil {
		return keep(err.Error())
	}
	if !clean {
		return keep("the checkout has uncommitted changes")
	}
	if err := a.git.Merge(ctx, orig, r.Branch, fmt.Sprintf("maestrello: merge #%d %s", card.ID, card.Title)); err != nil {
		if errors.Is(err, gitops.ErrMergeConflict) {
			a.note(card.ID, "%s: merging %s into %s conflicted and was aborted: %v", r.RepoPath, r.Branch, base, err)
			return keep("merge conflict, aborted: " + err.Error())
		}
		return keep(err.Error())
	}
	r.Result = "merged into " + base
	return true
}

// Discard throws away all of the card's worktrees and branches and closes the card.
func (a *App) Discard(id int64) error {
	if run := a.liveRun(id); run != nil {
		return fmt.Errorf("the agent is still running; stop it first")
	}
	a.cancelWait(id) // a card waiting for a folder never started
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
