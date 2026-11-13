package board

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"maestrello/internal/harness"
	"maestrello/internal/model"
	"maestrello/internal/settings"
	"maestrello/internal/textutil"
)

// refineMaxTurns caps the one-shot refinement: the structured answer is one turn, the rest is slack for a retry.
const refineMaxTurns = 4

// refineDeepMaxTurns is the cap when the refiner explores the code first: reading takes turns.
const refineDeepMaxTurns = 20

const refineSchema = `{
  "type": "object",
  "properties": {
    "title": {"type": "string", "description": "Short chat-style title for the card, at most 8 words"},
    "refined_prompt": {"type": "string", "description": "Self-contained task prompt for a fresh agent session"},
    "repos": {"type": "array", "items": {"type": "string"}, "description": "Repo paths (relative to the root folder) the task will change"},
    "notes": {"type": "string", "description": "Assumptions made and open questions for the user"}
  },
  "required": ["refined_prompt", "repos", "notes"]
}`

// The refiner never reads code: it only turns the card text into a clear prompt. The
// run agent explores the repos itself, so reading here would be paid for twice.
const refineNoCode = `You have no tools and cannot read any code, and you must not try: the agent that gets your prompt finds the files and functions itself.
Work from the card text and the repo list below only. Do not invent file names, function names or facts about the code that the card does not state.
Put assumptions and open questions in notes.`

const refineInstructions = `You are refining a task card for an autonomous coding agent.
The agent starts a fresh session with no other context than the prompt you write.

Turn the card text into a clear, self-contained prompt that contains, as far as the card text supports them:
- the goal, stated precisely;
- the repos involved, by name;
- constraints (what must not change, conventions to follow);
- a checkable definition of done (tests to run, behaviour to verify).

Also list the repos the task will change, as paths relative to the root folder, chosen from the list below.
"." is the root folder itself: use it when the code to change lives directly in the root folder rather than in one of the repos under it.
Also give the card a short title of at most 8 words, the way a chat names a conversation.
` + refineNoCode

const refineResearchInstructions = `You are refining a research card for an autonomous agent that will only read the codebase under the root folder.
The agent starts a fresh session with no other context than the prompt you write.

Turn the card text into a clear, self-contained prompt that contains, as far as the card text supports them:
- the question to answer, sharpened until it can be answered from the code;
- the repos or areas the question is about, by name;
- what the report should contain (findings with file paths and line numbers, open points).

The agent changes nothing and has no workspace, so "repos" is informational only: list the repos the question is about.
Also give the card a short title of at most 8 words, the way a chat names a conversation.
` + refineNoCode

const refinePlanInstructions = `You are refining a planning card for an autonomous agent that will only read the codebase under the root folder and write a plan.
The agent starts a fresh session with no other context than the prompt you write.

Turn the card text into a clear, self-contained prompt that contains, as far as the card text supports them:
- the scope of the work to plan, stated precisely, and what is out of scope;
- the repos or areas involved, by name;
- the expected breakdown: the kinds of tasks the plan should split into, each small enough for one agent session, and the order they depend on each other.

The agent changes nothing and creates no cards: it proposes tasks and the user decides which become cards. "repos" is informational only: list the repos the work touches.
Also give the card a short title of at most 8 words, the way a chat names a conversation.
` + refineNoCode

// With deep refinement the refiner may read the repos, read-only, before it writes the prompt.
const refineDeepCode = `You may read the code under the root folder with your read-only tools (read files, search) before you answer, and you are encouraged to: look at the code the card is about so the prompt names the real files, functions and conventions.
You cannot change anything. Only state facts about the code that you saw; put assumptions and open questions in notes.`

// refineDeepTools are the read-only tools of a deep refinement.
const refineDeepTools = "Read,Grep,Glob"

func refineInstructionsFor(kind string) string {
	switch kind {
	case model.KindResearch:
		return refineResearchInstructions
	case model.KindPlan:
		return refinePlanInstructions
	}
	return refineInstructions
}

type refineResult struct {
	Title         string   `json:"title"`
	RefinedPrompt string   `json:"refined_prompt"`
	Repos         []string `json:"repos"`
	Notes         string   `json:"notes"`
	modelChoice            // only filled when automatic model selection is on
}

func (a *App) Refine(id int64) error {
	if c, err := a.store.GetCard(id); err == nil && c.EffectiveKind() == model.KindChat {
		return fmt.Errorf("chat cards are never refined: the card text is the first message")
	}
	card, err := a.update(id, func(c *model.Card) {
		if c.Status == model.StatusTodo || c.Status == model.StatusReady || c.Status == model.StatusFailed && c.SessionID == "" {
			c.Status = model.StatusRefining
			c.SkipRefinement = false // I asked for refinement after all
			c.FailReason = ""
			c.Activity = "refining"
		}
	})
	if err != nil {
		return err
	}
	if card.Status != model.StatusRefining {
		return fmt.Errorf("card is %s; only todo or ready cards can be refined", card.Status)
	}
	if err := a.guard.Check(card.RootFolder); err != nil {
		a.fail(id, err.Error())
		return err
	}
	a.busy.Add(1)
	go func() {
		defer a.busy.Done()
		a.refine(card)
	}()
	return nil
}

func (a *App) refine(card *model.Card) {
	// The refinement model and effort come from the settings resolved for the card's project
	// and kind, so that harness runs it; the card's own harness only runs the card's sessions.
	settings, _, _ := settings.Resolve(a.store, card.ProjectID, card.EffectiveKind())
	h, err := a.harness(settings.Harness)
	if err == nil {
		err = a.requireHarness(settings.Harness)
	}
	if err != nil {
		a.fail(card.ID, "refinement: "+err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	go func() {
		select {
		case <-a.quit:
			cancel()
		case <-ctx.Done():
		}
	}()

	var prompt strings.Builder
	instructions := refineInstructionsFor(card.EffectiveKind())
	// No code access: I run from the data dir with no tools, and the repo list alone lets it plan repos by name.
	// Deep refinement instead runs from the root folder with read-only tools.
	cwd, tools, maxTurns := a.dataDir, "", refineMaxTurns
	if card.DeepRefinement {
		instructions = strings.Replace(instructions, refineNoCode, refineDeepCode, 1)
		cwd, tools, maxTurns = card.RootFolder, refineDeepTools, refineDeepMaxTurns
	}
	prompt.WriteString(instructions)
	known, _ := a.git.DiscoverRepos(card.RootFolder, a.dataDir)
	fmt.Fprintf(&prompt, "\n\nRoot folder: %s\nRepos under it:\n", card.RootFolder)
	if a.git.IsRepo(card.RootFolder) {
		prompt.WriteString("- . (the root folder, a git repo)\n")
	} else {
		prompt.WriteString("- . (the root folder; not a git repo, so the agent edits it in place)\n")
	}
	for _, r := range known {
		fmt.Fprintf(&prompt, "- %s\n", r)
	}
	known = append(known, ".")
	var extraDirs []string
	if len(card.Context) > 0 {
		prompt.WriteString("\nAdditional context (read-only reference):\n")
		for _, it := range card.Context {
			fmt.Fprintf(&prompt, "- %s (%s)\n", it.Path, it.Type)
		}
		if card.DeepRefinement {
			extraDirs = harness.ContextDirs(card.Context, card.RootFolder)
		}
	}
	var cands []Candidate
	if card.AutoModel {
		if cands = a.modelCandidates(settings); len(cands) > 0 {
			prompt.WriteString(modelSelectionPrompt(cands))
		}
	}
	if card.TitleAuto {
		fmt.Fprintf(&prompt, "\n\n--- CARD ---\n%s\n", card.Description) // the title is only a copy of the text
	} else {
		fmt.Fprintf(&prompt, "\n\n--- CARD ---\nTitle: %s\n\n%s\n", card.Title, card.Description)
	}

	if err := os.MkdirAll(cwd, 0o755); err != nil {
		a.fail(card.ID, err.Error())
		return
	}
	cmd, err := h.RefineCommand(ctx, harness.RefineSpec{
		Model: settings.RefineModel, Effort: settings.RefineEffort,
		Prompt: prompt.String(), Cwd: cwd, Tools: tools, MaxTurns: maxTurns, Schema: refineSchemaFor(len(cands) > 0), ExtraDirs: extraDirs,
	})
	if err != nil {
		a.fail(card.ID, "refinement: "+err.Error())
		return
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if card.DeepRefinement {
		a.note(card.ID, "refining with %s (deep: read-only code access)", settings.RefineModel)
	} else {
		a.note(card.ID, "refining with %s (card text only, no code access)", settings.RefineModel)
	}
	runErr := cmd.Run()

	res, err := h.ParseRefineResult(out.Bytes())
	if err != nil {
		a.fail(card.ID, fmt.Sprintf("refinement: %v %s %s", runErr, textutil.Tail(errb.String(), 500), textutil.Tail(out.String(), 500)))
		return
	}
	a.event(card.ID, "refine_result", out.Bytes())
	if res.IsError {
		a.fail(card.ID, "refinement: "+res.Text)
		return
	}
	var r refineResult
	if err := json.Unmarshal(res.Structured, &r); err != nil || strings.TrimSpace(r.RefinedPrompt) == "" {
		a.fail(card.ID, "refinement returned no usable prompt: "+textutil.Tail(res.Text, 500))
		return
	}

	var repos []string
	for _, p := range r.Repos {
		p = strings.Trim(strings.TrimSpace(p), "/")
		if p == "" {
			p = "."
		}
		if slices.Contains(known, p) {
			repos = append(repos, p)
		} else {
			r.Notes += fmt.Sprintf("\n(Dropped unknown repo %q.)", p)
		}
	}
	if err := a.store.SetPlannedRepos(card.ID, repos); err != nil {
		a.fail(card.ID, err.Error())
		return
	}
	var pick *Candidate
	if card.AutoModel {
		pick = a.applyModelChoice(card, settings, cands, r.modelChoice)
	}
	refined, err := a.update(card.ID, func(c *model.Card) {
		if pick != nil {
			a.useModel(c, settings, *pick)
		}
		if t := strings.TrimSpace(r.Title); c.TitleAuto && t != "" {
			c.Title = string([]rune(t)[:min(len([]rune(t)), 80)]) // a better title than the derived one; a title I typed stays
		}
		c.RefinedPrompt = r.RefinedPrompt
		c.RefineNotes = strings.TrimSpace(r.Notes)
		c.CostUSD += res.CostUSD
		c.Status = model.StatusReady
		c.Activity = ""
	})
	if err == nil {
		a.autoRun(refined)
	}
}

// autoRun starts a freshly refined card (or one that skipped refinement) when I opted into auto_run for it, so it
// goes Refining → Running without waiting in Ready for review. It reads the card
// as saved after refinement, so a PATCH made while refining (turning auto_run off,
// say) wins. A refused Run leaves the card in Ready with a note rather than
// failing it: the refinement itself succeeded.
func (a *App) autoRun(card *model.Card) {
	if !card.AutoRun || card.Status != model.StatusReady {
		return
	}
	select {
	case <-a.quit:
		return // shutting down: don't start sessions that would be killed at once
	default:
	}
	if card.SkipRefinement {
		a.note(card.ID, "refinement skipped; starting automatically (auto-run)")
	} else {
		a.note(card.ID, "refined; starting automatically (auto-run)")
	}
	if err := a.Run(card.ID); err != nil {
		a.note(card.ID, "auto-run did not start, card stays in Ready: %v", err)
	}
}

// applyModelChoice resolves the refiner's model choice and logs what was decided and why. It returns the
// candidate to run with, or nil to keep the card's manual model and harness (the safe fallback).
func (a *App) applyModelChoice(card *model.Card, settings model.Settings, cands []Candidate, ch modelChoice) *Candidate {
	if len(cands) == 0 {
		a.note(card.ID, "automatic model selection: no available model has capability/cost metadata; keeping %s/%s (manual setting)", card.Harness, card.Model)
		return nil
	}
	c, why, ok := resolveModel(cands, ch)
	if !ok {
		a.note(card.ID, "automatic model selection: %s; keeping %s/%s (manual setting)", why, card.Harness, card.Model)
		return nil
	}
	a.note(card.ID, "automatic model selection: %s/%s (tier %d, cost %g) for complexity %d; %s", c.Harness, c.Model, c.Tier, c.Cost, ch.Complexity, why)
	return &c
}

// useModel points the card at the chosen model and adapter. A different adapter has its own efforts and
// permission modes, so those fall back to the default (or to the adapter's own default) when the card's do not fit.
func (a *App) useModel(c *model.Card, settings model.Settings, pick Candidate) {
	if h, err := a.harness(pick.Harness); err == nil && pick.Harness != c.Harness {
		if h.ValidateEffort(c.Effort) != nil {
			c.Effort = settings.DefaultEffort
			if h.ValidateEffort(c.Effort) != nil {
				c.Effort = ""
			}
		}
		if h.ValidatePermissionMode(c.PermissionMode) != nil {
			c.PermissionMode = settings.DefaultPermissionMode
			if h.ValidatePermissionMode(c.PermissionMode) != nil {
				c.PermissionMode = "auto"
			}
		}
	}
	c.Harness, c.Model = pick.Harness, pick.Model
}
