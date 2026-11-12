package main

import (
	"fmt"
	"strings"
)

// Skipping refinement: a card with skip_refinement does not need a refinement session.
// Its prompt is the card text (or a prompt I handed over), no repos are planned unless
// I name them, and it goes straight to Ready.

// cardText is the prompt of a card that skips refinement: the title, a blank line,
// then the description. A derived title only repeats the description, so then it is left out.
func cardText(c *Card) string {
	if c.TitleAuto {
		return c.Description
	}
	if c.Description == "" {
		return c.Title
	}
	return c.Title + "\n\n" + c.Description
}

// skippable reports whether the board can send the card to Ready without refining it.
func skippable(c *Card) bool { return c.SkipRefinement && c.Status == StatusTodo }

// skipRefinement sends a Todo card with skip_refinement to Ready. The prompt is the one
// given, or the card text when that is empty. It reports whether the card moved; a card
// that is not skippable is left alone. It never starts the card: callers that honour
// auto_run call autoRun afterwards.
func (a *App) skipRefinement(id int64, prompt string) (*Card, bool, error) {
	moved := false
	card, err := a.update(id, func(c *Card) {
		if !skippable(c) {
			return
		}
		c.RefinedPrompt = strings.TrimSpace(prompt)
		if c.RefinedPrompt == "" {
			c.RefinedPrompt = cardText(c)
		}
		c.Status = StatusReady
		c.Activity = ""
		moved = true
	})
	if err != nil {
		return nil, false, err
	}
	if moved {
		a.note(id, "refinement skipped; the card's own text is the prompt")
		if card.AutoModel {
			a.note(id, "automatic model selection: no refiner ran, so the manual model %s/%s is used", card.Harness, card.Model)
		}
	}
	return card, moved, nil
}

// planRepos checks repo paths the way refinement does: relative to the root folder,
// chosen from the repos under it, with "." for the root folder itself. Unlike refinement,
// which drops what it does not know, an unknown path is an error so the caller can fix it.
func (a *App) planRepos(root string, paths []string) ([]string, error) {
	known, err := a.git.DiscoverRepos(root, a.dataDir)
	if err != nil {
		return nil, err
	}
	known = append(known, ".")
	var out []string
	for _, p := range paths {
		p = strings.Trim(strings.TrimSpace(p), "/")
		if p == "" {
			p = "."
		}
		if !contains(known, p) {
			return nil, fmt.Errorf("unknown repo %q: use a path relative to the root folder, or \".\" for the root folder itself", p)
		}
		if !contains(out, p) {
			out = append(out, p)
		}
	}
	return out, nil
}
