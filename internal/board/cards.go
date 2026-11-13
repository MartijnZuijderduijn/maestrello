package board

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"maestrello/internal/harness"
	"maestrello/internal/model"
	"maestrello/internal/settings"
)

type cardInput struct {
	Title              *string   `json:"title"`
	Description        *string   `json:"description"`
	RootFolder         *string   `json:"root_folder"`
	ProjectID          *int64    `json:"project_id"`
	RestrictRefinement *bool     `json:"restrict_refinement"`
	AutoRun            *bool     `json:"auto_run"`
	SkipRefinement     *bool     `json:"skip_refinement"`
	DeepRefinement     *bool     `json:"deep_refinement"`
	AutoModel          *bool     `json:"auto_model"`
	Model              *string   `json:"model"`
	Effort             *string   `json:"effort"`
	PermissionMode     *string   `json:"permission_mode"`
	MaxTurns           *int      `json:"max_turns"`
	MaxMinutes         *int      `json:"max_minutes"`
	PushDefault        *string   `json:"push_default"`
	Harness            *string   `json:"harness"`
	Kind               *string   `json:"kind"`  // code (default), research, plan or chat
	Tools              *[]string `json:"tools"` // toolbox ids; see toolbox.go
	RefinedPrompt      *string   `json:"refined_prompt"`
	Repos              *[]string `json:"repos"`
	Context            *[]string `json:"context"` // extra folders and files; [] clears; see normalizeContext

	context []model.ContextItem // Context after normalizeContext, which CreateCard and EditCard run before apply
}

const maxContextItems = 20

// normalizeContext validates paths for a card's context: absolute, inside the sandbox,
// existing, not in the data dir. It returns them cleaned, deduplicated and typed.
func (a *App) normalizeContext(paths []string) ([]model.ContextItem, error) {
	items := []model.ContextItem{}
	seen := map[string]bool{}
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" {
			return nil, errors.New("context path is empty")
		}
		p = filepath.Clean(p)
		if !filepath.IsAbs(p) {
			return nil, fmt.Errorf("context path %q must be absolute", p)
		}
		if seen[p] {
			continue
		}
		if err := a.guard.Check(p); err != nil {
			return nil, err
		}
		info, err := os.Stat(p)
		if err != nil {
			return nil, fmt.Errorf("context path %q: %w", p, err)
		}
		typ := "dir"
		if !info.IsDir() {
			typ = "file"
			if err := a.guard.Check(filepath.Dir(p)); err != nil {
				return nil, err
			}
		}
		if a.dataDir != "" && insideDir(p, a.dataDir) {
			return nil, fmt.Errorf("context path %q is inside the board's data directory", p)
		}
		seen[p] = true
		items = append(items, model.ContextItem{Path: p, Type: typ})
	}
	if len(items) > maxContextItems {
		return nil, fmt.Errorf("at most %d context items", maxContextItems)
	}
	return items, nil
}

// titleMax is how many runes of a prompt's first line a derived title keeps.
const titleMax = 60

// deriveTitle makes a short title from the first non-blank line of text, the way a chat
// names a conversation: markdown markers dropped, whitespace collapsed, cut at a word
// boundary with an ellipsis. It returns "" when nothing usable is left.
func deriveTitle(text string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		line = strings.TrimLeft(line, "#-*> \t")
		if i := strings.IndexAny(line, ".)"); i > 0 && i <= 3 && isDigits(line[:i]) && strings.HasPrefix(line[i+1:], " ") {
			line = line[i+1:] // "1. " or "2) " of a numbered list
		}
		if f := strings.Fields(line); len(f) > 0 {
			return cutRunes(strings.Join(f, " "), titleMax)
		}
	}
	return ""
}

func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

// cutRunes shortens s to at most n runes, at a word boundary when it can, and adds "…" when it cut.
func cutRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	cut := r[:n]
	if r[n] != ' ' { // I cut inside a word: back up to the space before it
		for i := n - 1; i > 0; i-- {
			if cut[i] == ' ' {
				cut = cut[:i]
				break
			}
		}
	}
	return strings.TrimRight(string(cut), " ,;:.-") + "…"
}

func (in *cardInput) apply(c *model.Card) error {
	set := func(dst *string, src *string) {
		if src != nil {
			*dst = strings.TrimSpace(*src)
		}
	}
	prevTitle, prevDesc := c.Title, c.Description
	set(&c.Title, in.Title)
	set(&c.Description, in.Description)
	typed := in.Title != nil && c.Title != "" && c.Title != prevTitle
	switch {
	case typed:
		c.TitleAuto = false // a title I typed is mine; the board never replaces it
	case c.TitleAuto && c.Description != prevDesc:
		c.Title = deriveTitle(c.Description) // the text changed under a derived title, so derive it again
	}
	if c.Title == "" {
		if c.Description == "" {
			return errors.New("a title or description is required")
		}
		c.Title, c.TitleAuto = deriveTitle(c.Description), true
		if c.Title == "" {
			c.Title = "Untitled card" // nothing usable in the text, but the slug and branch name need a title
		}
	}
	set(&c.Model, in.Model)
	set(&c.Effort, in.Effort)
	set(&c.PermissionMode, in.PermissionMode)
	set(&c.Harness, in.Harness)
	set(&c.PushDefault, in.PushDefault)
	if in.RefinedPrompt != nil {
		c.RefinedPrompt = *in.RefinedPrompt
	}
	if in.RootFolder != nil {
		c.RootFolder = filepath.Clean(strings.TrimSpace(*in.RootFolder))
	}
	if in.RestrictRefinement != nil {
		c.RestrictRefinement = *in.RestrictRefinement
	}
	if in.AutoRun != nil {
		c.AutoRun = *in.AutoRun
	}
	if in.SkipRefinement != nil {
		c.SkipRefinement = *in.SkipRefinement
	}
	if in.AutoModel != nil {
		c.AutoModel = *in.AutoModel
	}
	if in.DeepRefinement != nil {
		c.DeepRefinement = *in.DeepRefinement
	}
	if in.MaxTurns != nil {
		c.MaxTurns = *in.MaxTurns
	}
	if in.MaxMinutes != nil {
		c.MaxMinutes = *in.MaxMinutes
	}
	if c.Harness == "" {
		c.Harness = harness.Default
	}
	if in.Kind != nil {
		k, err := model.NormalizeKind(*in.Kind)
		if err != nil {
			return err
		}
		c.Kind = k
	}
	if in.Tools != nil {
		t, err := model.NormalizeTools(*in.Tools)
		if err != nil {
			return err
		}
		c.Tools = t
	}
	if in.context != nil {
		c.Context = in.context
	}
	if c.Kind == "" {
		c.Kind = model.KindCode
	}
	if c.Kind == model.KindChat {
		c.SkipRefinement = true // a chat never refines: the card text is the first message
	}
	h, err := harness.For(c.Harness)
	if err == nil && in.Harness != nil {
		// A harness chosen by the input brings its own defaults for what it does not accept.
		if in.Effort == nil && h.ValidateEffort(c.Effort) != nil {
			c.Effort = ""
		}
		if in.PermissionMode == nil && h.ValidatePermissionMode(c.PermissionMode) != nil {
			c.PermissionMode = h.Caps().PermissionModes[0]
		}
	}
	switch {
	case !filepath.IsAbs(c.RootFolder):
		return errors.New("root folder must be an absolute path")
	case err != nil:
		return err
	case h.ValidatePermissionMode(c.PermissionMode) != nil:
		return errors.New("invalid permission mode")
	case h.ValidateEffort(c.Effort) != nil:
		return errors.New("invalid effort")
	case !model.ValidPush[c.PushDefault]:
		return errors.New("invalid push default")
	}
	return nil
}

// CreateCard seeds a card from the settings defaults, applies the input and saves it in Todo.
// A card that skips refinement (skip_refinement, or a refined_prompt handed over without
// saying otherwise) goes straight to Ready with that prompt, and runs at once with auto_run.
func (a *App) CreateCard(in cardInput) (*model.Card, error) {
	// The project and kind pick the layers the defaults come from (see layers.go). The project is the
	// one named, or else the existing one for the root folder the card will have.
	proj, err := a.projectFor(in.ProjectID)
	if err != nil {
		return nil, err
	}
	global, _ := a.store.Settings()
	kind := model.KindCode
	if in.Kind != nil {
		if kind, err = model.NormalizeKind(*in.Kind); err != nil {
			return nil, err
		}
	}
	var projectID int64
	if proj != nil {
		projectID = proj.ID
	} else {
		root := global.DefaultRootFolder
		if in.RootFolder != nil {
			root = filepath.Clean(strings.TrimSpace(*in.RootFolder))
		}
		projectID = a.store.ProjectIDForRoot(root)
	}
	st, _, err := settings.Resolve(a.store, projectID, kind)
	if err != nil {
		st = global // layers unreadable: fall back to the global settings, as before
	}
	c := &model.Card{
		RootFolder: st.DefaultRootFolder, Model: st.DefaultModel, Effort: st.DefaultEffort,
		PermissionMode: st.DefaultPermissionMode, Harness: st.Harness, MaxTurns: st.DefaultMaxTurns, MaxMinutes: st.DefaultMaxMinutes,
		PushDefault: st.DefaultPushDefault, AutoRun: st.DefaultAutoRun, SkipRefinement: st.DefaultSkipRefinement, AutoModel: st.AutoModel,
	}
	// A project that is named seeds the root folder unless one is given.
	if proj != nil && in.RootFolder == nil {
		c.RootFolder = proj.RootFolder
	}
	if in.Context != nil {
		if in.context, err = a.normalizeContext(*in.Context); err != nil {
			return nil, err
		}
	}
	if err := in.apply(c); err != nil {
		return nil, err
	}
	if in.Tools == nil {
		c.Tools = append([]string{}, st.DefaultTools[c.Kind]...) // seeded from the settings for the card's kind
	}
	if in.SkipRefinement == nil && strings.TrimSpace(c.RefinedPrompt) != "" {
		c.SkipRefinement = true // handing over a finished prompt means it needs no refinement
	}
	if err := a.guard.Check(c.RootFolder); err != nil {
		return nil, err
	}
	if proj == nil {
		// No project named: use the one for the card's root folder (the default root when none
		// is given), so clients that know nothing of projects keep working.
		var created bool
		if proj, created, err = a.store.ProjectForRoot(c.RootFolder); err != nil {
			return nil, err
		}
		if created {
			a.hub.Publish("project", proj)
		}
	}
	c.ProjectID = proj.ID
	var repos []string
	if in.Repos != nil {
		var err error
		if repos, err = a.planRepos(c.RootFolder, *in.Repos); err != nil {
			return nil, err
		}
	}
	if err := a.store.CreateCard(c); err != nil {
		return nil, err
	}
	if len(repos) > 0 {
		if err := a.store.SetPlannedRepos(c.ID, repos); err != nil {
			return nil, err
		}
	}
	if !c.SkipRefinement {
		a.hub.Publish("card", c)
		return c, nil
	}
	return a.afterSkip(c.ID, c.RefinedPrompt)
}

// afterSkip skips refinement for a new or edited Todo card and honours auto_run. It returns the card as it is afterwards.
func (a *App) afterSkip(id int64, prompt string) (*model.Card, error) {
	card, moved, err := a.skipRefinement(id, prompt)
	if err != nil {
		return nil, err
	}
	if moved {
		a.autoRun(card)
		return a.store.GetCard(id)
	}
	return card, nil
}

// EditCard applies the input to a Todo or Ready card; a refused edit leaves it untouched.
// Setting skip_refinement on a Todo card sends it to Ready (see CreateCard); on a Ready
// card the flag cannot change, because the card has been through that choice already.
func (a *App) EditCard(id int64, in cardInput) (*model.Card, error) {
	skip := in.SkipRefinement != nil && *in.SkipRefinement || in.Kind != nil && strings.TrimSpace(*in.Kind) == model.KindChat
	proj, err := a.projectFor(in.ProjectID)
	if err != nil {
		return nil, err
	}
	if cur, err := a.store.GetCard(id); err == nil && skip && cur.Status == model.StatusTodo && in.Repos != nil {
		root := cur.RootFolder
		if in.RootFolder != nil {
			root = filepath.Clean(strings.TrimSpace(*in.RootFolder))
		}
		repos, err := a.planRepos(root, *in.Repos) // checked up front, so a refused edit changes nothing
		if err != nil {
			return nil, err
		}
		in.Repos = &repos
	}
	if in.Context != nil { // checked up front, so a refused edit changes nothing
		if in.context, err = a.normalizeContext(*in.Context); err != nil {
			return nil, err
		}
	}
	var applyErr error
	c, err := a.update(id, func(c *model.Card) {
		if c.Status != model.StatusTodo && c.Status != model.StatusReady {
			applyErr = errors.New("only todo and ready cards can be edited")
			return
		}
		before := *c
		if applyErr = in.apply(c); applyErr == nil {
			applyErr = a.guard.Check(c.RootFolder)
		}
		if applyErr == nil && proj != nil {
			c.ProjectID = proj.ID // the root folder stays the card's own unless the input gives one
		}
		if applyErr == nil && before.Status != model.StatusTodo {
			c.SkipRefinement = before.SkipRefinement
		}
		if applyErr != nil {
			*c = before
		}
	})
	if err == nil {
		err = applyErr
	}
	if err == nil && in.Repos != nil {
		err = a.store.SetPlannedRepos(id, *in.Repos)
	}
	if err == nil && skip && c.Status == model.StatusTodo {
		if in.Repos == nil {
			err = a.store.SetPlannedRepos(id, nil) // no repos are planned: the run uses the board's rules
		}
		prompt := ""
		if in.RefinedPrompt != nil {
			prompt = *in.RefinedPrompt
		}
		if err == nil {
			c, err = a.afterSkip(id, prompt)
		}
	}
	return c, err
}

// leftReady reports whether a card is no longer in Ready (or in Todo, for one that skips
// refinement), which is why Run refused it; such a card is skipped silently by a bulk start.
func (a *App) leftReady(id int64) bool {
	c, err := a.store.GetCard(id)
	return err != nil || c.Status != model.StatusReady && !skippable(c)
}

var errNotDone = errors.New("card is not done")

// deleteCard removes a card's board data (rows only; branches are untouched)
// and tells open boards. With onlyDone set it refuses cards that are not in Done.
// A card in any other column is first moved to Done, which keeps its work as local
// branches and removes its worktrees and leases. A live agent is only halted when stop
// is set (I confirmed); a card being refined cannot be deleted until refinement ends.
func (a *App) deleteCard(id int64, onlyDone, stop bool) error {
	c, err := a.store.GetCard(id)
	if err != nil {
		return err
	}
	if onlyDone && c.Status != model.StatusDone {
		return errNotDone
	}
	if c.Status != model.StatusDone {
		if c.Status == model.StatusRefining {
			return errors.New("the card is being refined; wait for it to finish")
		}
		if stop {
			err = a.MoveStopping(id, model.StatusDone)
		} else {
			err = a.Move(id, model.StatusDone) // refused while an agent is live
		}
		if err != nil {
			return err
		}
	}
	if err := a.store.DeleteCard(id); err != nil {
		return err
	}
	a.hub.Publish("deleted", map[string]int64{"id": id})
	return nil
}
