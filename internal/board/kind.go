package board

import (
	"errors"
	"fmt"
	"strings"

	"maestrello/internal/model"
)

// checkPlanTasks validates and normalizes the tasks of report_plan against the plan card's root folder.
func (a *App) checkPlanTasks(root string, tasks []model.PlanTask) ([]model.PlanTask, error) {
	if len(tasks) == 0 {
		return nil, errors.New("tasks must hold at least one task")
	}
	out := make([]model.PlanTask, 0, len(tasks))
	for i, t := range tasks {
		t.Title, t.Description = strings.TrimSpace(t.Title), strings.TrimSpace(t.Description)
		if t.Title == "" {
			return nil, fmt.Errorf("task %d has no title", i+1)
		}
		k, err := model.NormalizeKind(t.Kind)
		if err != nil || k == model.KindChat {
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
func (a *App) createPlanCards(plan *model.Card, selected []int) ([]*model.Card, error) {
	if selected == nil {
		for i := range plan.PlanTasks {
			selected = append(selected, i)
		}
	}
	seen := map[int]bool{}
	var tasks []model.PlanTask
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
	var created []*model.Card
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
	if kind == model.KindPlan {
		return "report_plan or report_failed"
	}
	return "report_done or report_failed"
}
