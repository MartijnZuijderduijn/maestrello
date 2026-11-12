package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
)

// Upper bounds for the budget of a card, so a typo cannot hand an agent an unlimited run.
const (
	maxTurnsLimit   = 10000
	maxMinutesLimit = 7 * 24 * 60
)

// AgentParams are the parameters of a card's agent that can be changed after the card exists:
// the prompt the agent runs, its model and its budget (a turn and a time limit).
// A nil field stays as it is.
type AgentParams struct {
	Prompt     *string `json:"prompt"`
	Model      *string `json:"model"`
	MaxTurns   *int    `json:"max_turns"`
	MaxMinutes *int    `json:"max_minutes"`
}

// agentEditable says whether a card in this status may have its agent parameters changed. Running
// (which includes a card queued for a local folder), refining and needs_input all hold a live or
// resumable session that has already been given its parameters, so they are refused.
func agentEditable(status string) bool {
	switch status {
	case StatusTodo, StatusReady, StatusReview, StatusFailed, StatusDone:
		return true
	}
	return false
}

// validateModel checks model against what the card's harness accepts: claude takes the board's
// knownModels; another harness takes what it lists (or anything, when it cannot list its models).
// An empty model means "the harness default", which only a harness that has one may use.
func (a *App) validateModel(harness, model string) error {
	if harness == "" {
		harness = defaultHarness
	}
	if harness == "claude" {
		if !slices.Contains(knownModels, model) {
			return fmt.Errorf("unknown model %q; known models: %s", model, strings.Join(knownModels, ", "))
		}
		return nil
	}
	if _, err := harnessFor(harness); err != nil {
		return err
	}
	if model == "" {
		return nil
	}
	if list := a.harnessStatus(harness).Models; len(list) > 0 && !slices.Contains(list, model) {
		return fmt.Errorf("unknown model %q for %s", model, harness)
	}
	return nil
}

// EditAgent changes the prompt, model and budget of a card that is not running. The check and the
// change happen under the board's lock, so a card cannot start between them; a refused edit leaves
// the card untouched. The next run reads these values from the saved card.
func (a *App) EditAgent(id int64, in AgentParams) (*Card, error) {
	var applyErr error
	c, err := a.update(id, func(c *Card) {
		if !agentEditable(c.Status) {
			applyErr = fmt.Errorf("a %s card cannot be edited; stop it first", c.Status)
			return
		}
		if a.runs[id] != nil { // a.mu is held by update; the session of a finished card may still be winding down
			applyErr = errors.New("the agent is still running; stop it first")
			return
		}
		before := *c
		if applyErr = in.apply(a, c); applyErr != nil {
			*c = before
		}
	})
	if err == nil {
		err = applyErr
	}
	return c, err
}

func (in AgentParams) apply(a *App, c *Card) error {
	if in.Prompt != nil {
		p := strings.TrimSpace(*in.Prompt)
		if p == "" {
			return errors.New("the prompt must not be empty")
		}
		c.RefinedPrompt = p
	}
	if in.Model != nil {
		m := strings.TrimSpace(*in.Model)
		if err := a.validateModel(c.Harness, m); err != nil {
			return err
		}
		c.Model = m
	}
	if in.MaxTurns != nil {
		if *in.MaxTurns < 1 || *in.MaxTurns > maxTurnsLimit {
			return fmt.Errorf("max turns must be between 1 and %d", maxTurnsLimit)
		}
		c.MaxTurns = *in.MaxTurns
	}
	if in.MaxMinutes != nil {
		if *in.MaxMinutes < 1 || *in.MaxMinutes > maxMinutesLimit {
			return fmt.Errorf("max minutes must be between 1 and %d", maxMinutesLimit)
		}
		c.MaxMinutes = *in.MaxMinutes
	}
	return nil
}

func (a *App) handlePatchAgent(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var in AgentParams
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, err)
		return
	}
	c, err := a.EditAgent(id, in)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, c)
}
