package board

import (
	"fmt"
	"time"

	"maestrello/internal/model"
)

// Move is a manual move I make by dragging a card to another column. Any column can be
// the target; agent-driven transitions are untouched. A card with a live session is
// refused: the agent would be killed, so that takes the explicit MoveStopping. Dropping on Refining or Running starts that work; dropping on Done keeps
// the work as local branches and removes the worktrees; other columns just change status.
func (a *App) Move(id int64, to string) error { return a.moveBy(id, to, "you") }

// moveBy is Move with the origin named in the card's notes ("you", or "you via MCP").
func (a *App) moveBy(id int64, to, by string) error {
	if !model.Statuses[to] {
		return fmt.Errorf("unknown column %q", to)
	}
	if a.liveRun(id) != nil {
		return fmt.Errorf("the agent is still running; stop it first")
	}
	card, err := a.store.GetCard(id)
	if err != nil {
		return err
	}
	if card.Status == to {
		return nil
	}
	if card.Status == model.StatusRefining {
		return fmt.Errorf("the card is being refined; wait for it to finish")
	}
	if a.cancelWait(id) {
		// It never started: it rests where it would be without the run.
		rest := model.StatusReady
		if card.SessionID != "" {
			rest = model.StatusFailed
		}
		card, err = a.update(id, func(c *model.Card) {
			c.Status = rest
			c.Activity = ""
			if rest == model.StatusFailed {
				c.FailReason = "taken out of the folder queue by " + by
			}
		})
		if err != nil {
			return err
		}
		if card.Status == to {
			a.note(id, "moved to %s by %s", to, by)
			return nil
		}
	}
	if card.Status == model.StatusDone {
		repos, err := a.store.Repos(id)
		if err != nil {
			return err
		}
		for _, r := range repos {
			if r.WorktreePath != "" && !r.Local() {
				return fmt.Errorf("the card's worktrees were removed when it was closed; create a new card instead")
			}
		}
	}

	if (to == model.StatusReady || to == model.StatusRunning) && skippable(card) {
		// No refinement needed: the card text (or the prompt it already has) is the prompt.
		if card, _, err = a.skipRefinement(id, card.RefinedPrompt); err != nil {
			return err
		}
	}

	if (to == model.StatusReady || to == model.StatusRunning) && card.RefinedPrompt == "" {
		// Refining is optional: without a refined prompt the agent works from the card text.
		if card, err = a.update(id, func(c *model.Card) { c.RefinedPrompt = cardText(c) }); err != nil {
			return err
		}
	}

	switch to {
	case model.StatusRefining:
		return a.Refine(id)
	case model.StatusRunning:
		a.closeInput(id, by)
		a.note(id, "moved to %s by %s", to, by)
		if card.Status == model.StatusTodo || card.Status == model.StatusReady || card.SessionID == "" {
			a.setStatus(id, model.StatusReady) // Run checks out the repos and starts a fresh session
			return a.Run(id)
		}
		if card.EffectiveKind() == model.KindChat {
			return a.Resume(id, "Continue.")
		}
		return a.Resume(id, "Continue where you left off. Call "+finishTools(card.EffectiveKind())+" when finished.")
	case model.StatusReady:
	case model.StatusDone:
		repos, err := a.store.Repos(id)
		if err != nil {
			return err
		}
		keep := map[int64]string{}
		for _, r := range repos {
			keep[r.ID] = "local" // a drag never pushes
		}
		if err := a.closeRepos(card, keep); err != nil {
			return err
		}
		a.cleanupWorkspace(id)
	}
	if to != model.StatusNeedsInput {
		a.closeInput(id, by)
	}
	a.note(id, "moved to %s by %s", to, by)
	a.releaseLeases(id) // none while no session runs; release is idempotent
	_, err = a.update(id, func(c *model.Card) {
		c.Status = to
		c.Activity = "moved by " + by
		c.WaitingOn = 0
		if to != model.StatusFailed {
			c.FailReason = ""
		}
	})
	return err
}

// MoveStopping is Move for a card whose session may be live, after I confirmed that the
// agent may be halted. It stops the session, waits until the run has finished settling the
// card (which sends it to Failed), and only then applies the move, so the move wins and the
// stop's Failed status cannot overwrite it. Moves that would be refused or are no-ops are
// checked first, so they never kill the agent. Without a live session it is a plain Move.
func (a *App) MoveStopping(id int64, to string) error { return a.moveStoppingBy(id, to, "you") }

func (a *App) moveStoppingBy(id int64, to, by string) error {
	if !model.Statuses[to] {
		return fmt.Errorf("unknown column %q", to)
	}
	card, err := a.store.GetCard(id)
	if err != nil {
		return err
	}
	if card.Status == to {
		return nil
	}
	if card.Status == model.StatusRefining {
		return fmt.Errorf("the card is being refined; wait for it to finish")
	}
	if run := a.liveRun(id); run != nil {
		a.note(id, "agent halted by %s to move the card to %s", by, to)
		a.Stop(id) // an error only means the run ended meanwhile
		select {
		case <-run.done:
		case <-time.After(30 * time.Second): // Stop escalates to SIGKILL after 5 seconds
			return fmt.Errorf("the agent did not stop in time; try again")
		}
	}
	return a.moveBy(id, to, by)
}

// closeInput answers a card's open input so it no longer counts as waiting on me.
func (a *App) closeInput(id int64, by string) {
	if in, _ := a.store.OpenInput(id); in != nil {
		a.store.AnswerInput(in.ID, "moved by "+by)
	}
}
