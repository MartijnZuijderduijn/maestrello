package main

import (
	"errors"
	"fmt"
	"strings"
)

// Card kinds. Every kind runs through the same lifecycle, MCP endpoint and UI;
// the kind decides what the agent may touch and how its work ends.
const (
	KindCode     = "code"     // edits repos in worktrees (or local work); report_done → Review → Approve
	KindResearch = "research" // read-only; a written report through report_done
	KindPlan     = "plan"     // read-only; report_plan proposes tasks, Approve creates one card per task
	KindChat     = "chat"     // no tools; each reply ends the turn and waits for me in Needs input
)

var validKinds = []string{KindCode, KindResearch, KindPlan, KindChat}

// kind is the card's kind; cards saved before kinds existed are code.
func (c *Card) kind() string {
	if c.Kind == "" {
		return KindCode
	}
	return c.Kind
}

// readOnly reports whether the card's agent only reads the root folder: no worktrees,
// no local work, no leases.
func (c *Card) readOnly() bool { return c.kind() == KindResearch || c.kind() == KindPlan }

// usesRepos reports whether the card works in repos (worktrees or local work).
func (c *Card) usesRepos() bool { return c.kind() == KindCode }

// PlanTask is one task a plan card proposes. Approving the plan creates a card from it.
type PlanTask struct {
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Kind        string   `json:"kind,omitempty"` // code (default), research or plan
	Repos       []string `json:"repos,omitempty"`
}

// normalizeKind validates a kind given as input; "" means code.
func normalizeKind(k string) (string, error) {
	k = strings.TrimSpace(k)
	if k == "" {
		return KindCode, nil
	}
	if !contains(validKinds, k) {
		return "", errors.New("invalid kind")
	}
	return k, nil
}

// checkPlanTasks validates and normalizes the tasks of report_plan against the plan card's root folder.
func (a *App) checkPlanTasks(root string, tasks []PlanTask) ([]PlanTask, error) {
	if len(tasks) == 0 {
		return nil, errors.New("tasks must hold at least one task")
	}
	out := make([]PlanTask, 0, len(tasks))
	for i, t := range tasks {
		t.Title, t.Description = strings.TrimSpace(t.Title), strings.TrimSpace(t.Description)
		if t.Title == "" {
			return nil, fmt.Errorf("task %d has no title", i+1)
		}
		k, err := normalizeKind(t.Kind)
		if err != nil || k == KindChat {
			return nil, fmt.Errorf("task %d: kind must be code, research or plan", i+1)
		}
		t.Kind = k
		if len(t.Repos) > 0 {
			if t.Repos, err = a.planRepos(root, t.Repos); err != nil {
				return nil, fmt.Errorf("task %d: %w", i+1, err)
			}
		}
		out = append(out, t)
	}
	return out, nil
}

// createPlanCards creates one Todo card per selected task of an approved plan, in the plan card's
// project. Nothing else about them comes from the plan: the settings defaults apply, except that
// they never skip refinement or run by themselves, so they all land in Todo.
func (a *App) createPlanCards(plan *Card, selected []int) ([]*Card, error) {
	if selected == nil {
		for i := range plan.PlanTasks {
			selected = append(selected, i)
		}
	}
	seen := map[int]bool{}
	var tasks []PlanTask
	for _, i := range selected {
		if i < 0 || i >= len(plan.PlanTasks) {
			return nil, fmt.Errorf("no task %d: the plan has %d tasks (numbered from 0)", i, len(plan.PlanTasks))
		}
		if !seen[i] {
			seen[i] = true
			tasks = append(tasks, plan.PlanTasks[i])
		}
	}
	// Check everything before creating anything, so a refused approval leaves no stray cards.
	for _, t := range tasks {
		if len(t.Repos) > 0 {
			if _, err := a.planRepos(plan.RootFolder, t.Repos); err != nil {
				return nil, fmt.Errorf("task %q: %w", t.Title, err)
			}
		}
	}
	no := false
	var created []*Card
	for _, t := range tasks {
		in := cardInput{
			Title: &t.Title, Description: &t.Description, Kind: &t.Kind,
			RootFolder: &plan.RootFolder, ProjectID: &plan.ProjectID,
			SkipRefinement: &no, AutoRun: &no,
		}
		if len(t.Repos) > 0 {
			in.Repos = &t.Repos
		}
		if len(plan.Context) > 0 { // created cards inherit the plan's context
			paths := make([]string, len(plan.Context))
			for i, it := range plan.Context {
				paths[i] = it.Path
			}
			in.Context = &paths
		}
		c, err := a.CreateCard(in)
		if err != nil {
			return created, fmt.Errorf("task %q: %w", t.Title, err)
		}
		a.note(c.ID, "created from plan #%d", plan.ID)
		created = append(created, c)
	}
	return created, nil
}

// finishTools names the tools a card of this kind ends its work with, for resume messages.
func finishTools(kind string) string {
	if kind == KindPlan {
		return "report_plan or report_failed"
	}
	return "report_done or report_failed"
}
