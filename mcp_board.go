package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
)

// The board-level MCP endpoint (/mcp). It is for me and my own harnesses (Claude Desktop,
// Claude Code, ...), never for card agents: the runner's --mcp-config only names /mcp/<card>.
// It only maps arguments and errors onto the same methods the browser uses.

const viaMCP = "you via MCP" // origin named in the notes of changes made here

// editFields are the card fields create_card and update_card accept (the cardInput keys).
func editFields() [][2]any {
	return [][2]any{
		optProp("title", "string", "Short title; derived from the description when left out"),
		optProp("description", "string", "What the card should achieve"),
		optProp("project_id", "integer", "Project (board) the card belongs to, see list_projects. On create it also supplies the root folder unless root_folder is given; "+
			"without either, the project for the default root folder is used (and created if needed)"),
		optProp("root_folder", "string", "Absolute root folder; defaults to the project's root, then the board's default. Must be inside the sandbox when the board runs sandboxed"),
		optProp("model", "string", "Model for the agent, e.g. sonnet, opus"),
		optProp("effort", "string", "low, medium, high, xhigh or max (empty for the model default)"),
		optProp("permission_mode", "string", "auto, acceptEdits, manual, dontAsk, plan or bypassPermissions"),
		optProp("kind", "string", "code (default: edits repos), research (read-only report), plan (read-only plan whose approval creates cards) or chat (conversation, no tools, never refined)"),
		{"?tools", map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": toolIDs()},
			"description": "Optional agent tools switched on for the card (toolbox ids): web_search, web_fetch, skills. Left out on create, the board's defaults for the kind apply; an unknown id is an error"}},
		{"?context", map[string]any{"type": "array", "items": map[string]any{"type": "string"},
			"description": "Extra absolute folder or file paths the agent may read (max 20). Each must exist and be inside the sandbox; replaces the current list, [] clears it"}},
		optProp("harness", "string", "Coding-agent CLI that runs the card; claude is the only one (empty for the board's default)"),
		optProp("max_turns", "integer", "Turn limit (0 for none)"),
		optProp("max_minutes", "integer", "Wall-clock limit in minutes (0 for none)"),
		optProp("push_default", "string", "local, push or pr; what approval does by default (empty to ask)"),
		optProp("auto_model", "boolean", "Let the refiner pick the smallest capable model and adapter for this card, ignoring model (which stays the fallback); defaults to the board setting"),
		optProp("auto_run", "boolean", "Run as soon as refinement succeeds (with skip_refinement: run at once)"),
		optProp("skip_refinement", "boolean", "Skip the refinement session, which costs tokens: the card goes straight to Ready with the title and description as its prompt (the description alone when the title was derived) "+
			"(or the refined_prompt you give). On update_card it only takes effect on a Todo card"),
		optProp("restrict_refinement", "boolean", "Ignored: refinement never reads code (kept so old clients keep working)"),
	}
}

// promptFields are the finished prompt and repo plan an MCP client can hand over instead of having the board refine the card.
func promptFields() [][2]any {
	return [][2]any{
		optProp("refined_prompt", "string", "The self-contained prompt the agent runs"),
		{"?repos", map[string]any{"type": "array", "items": map[string]any{"type": "string"},
			"description": "Repos (paths relative to the root folder, or \".\" for the root folder itself) the card plans to work in"}},
	}
}

func withID(extra ...[2]any) [][2]any {
	return append([][2]any{prop("id", "integer", "Card id")}, extra...)
}

var boardTools = []map[string]any{
	tool("list_projects", "List the projects (boards) in id order, each with its root folder and the number of cards per status."),
	tool("list_cards", "List the cards on the board in board order (id ascending), as a compact view without the refined prompt.",
		optProp("project", "integer", "Only this project's cards (project id)"),
		optProp("status", "string", "Only this column: todo, refining, ready, running, needs_input, review, failed or done")),
	tool("get_card", "Read one card in full: the card, its repos, open and answered inputs, whether an agent is live, and its last events.",
		prop("id", "integer", "Card id")),
	tool("create_card", "Create a card. The title is optional: give a description and the title is derived from it. Fields left out take the board's defaults. Normally it lands in Todo and the board refines it into a prompt, which costs tokens. "+
		"If you already have a detailed, self-contained prompt, hand it over as refined_prompt (optionally with repos) or set skip_refinement: "+
		"the card then skips refinement and lands in Ready (or runs at once with auto_run). Without refined_prompt, skip_refinement uses the description (with the title in front when you gave one) as the prompt, "+
		"and no repos are planned: a plain folder is worked on in place, otherwise the agent adds repos itself.",
		append(editFields(), promptFields()...)...),
	tool("update_card", "Edit a card. Only cards in todo or ready can be edited. Fields left out stay as they are.",
		append(withID(promptFields()...), editFields()...)...),
	tool("move_card", "Move cards to another column, like dragging them in the browser. "+
		"Moving to refining starts refinement; moving to running starts (or resumes) an agent; moving to done closes the worktrees (work is kept on local branches, nothing is pushed). "+
		"A card whose agent is live is refused unless stop is true, which halts the agent (it can take up to 30s). Set stop only after the user has confirmed. "+
		"Pass id for one card or ids for several; cards move in the order given and each one reports its own result.",
		optProp("id", "integer", "Card id"),
		[2]any{"?ids", map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "Several card ids to move to the same column"}},
		prop("status", "string", "Target column: todo, refining, ready, running, needs_input, review, failed or done"),
		optProp("stop", "boolean", "Halt a live agent first. Only after the user confirmed")),
	tool("answer_input", "Answer the question, permission request or limit prompt a card in needs_input is waiting on. "+
		"For a permission request set allow; for a limit prompt answer \"continue\" or \"discard\".",
		withID(optProp("input_id", "integer", "Input id from get_card; defaults to the open input"),
			optProp("text", "string", "The answer, or a note with the allow/deny decision"),
			optProp("allow", "boolean", "Allow a permission request"))...),
	tool("approve_card", "Approve a card in review: applies the per-repo actions, removes the worktrees and moves it to done. "+
		"Approving a plan card creates one Todo card per selected task. "+
		"Pushing or opening a PR leaves this machine, so only choose push or pr after the user confirmed.",
		withID([2]any{"?actions", map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"},
			"description": "Repo id (from get_card) to local, push or pr; repos left out use the card's push default, then local"}},
			[2]any{"?tasks", map[string]any{"type": "array", "items": map[string]any{"type": "integer"},
				"description": "Plan cards: indices (from 0, into plan_tasks) of the tasks to create as cards; left out creates all of them, an empty list none"}})...),
	tool("discard_card", "Discard a card: removes its worktrees and branches and moves it to done. The work is lost, so only after the user confirmed.",
		withID()...),
	tool("send_back_card", "Send a card in review or failed back to work with feedback; the same agent session resumes.",
		withID(prop("text", "string", "Feedback for the agent"))...),
}

func (a *App) handleBoardMCP(w http.ResponseWriter, r *http.Request) {
	serveMCP(w, r, "maestrello-board", boardTools, a.callBoardTool)
}

// toolJSON returns v as the text of a tool result.
func toolJSON(v any) (string, bool) {
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err.Error(), true
	}
	return string(out), false
}

type boardArgs struct {
	cardInput
	ID      int64             `json:"id"`
	IDs     []int64           `json:"ids"`
	Status  string            `json:"status"`
	Project int64             `json:"project"` // list_cards filter
	Stop    bool              `json:"stop"`
	InputID int64             `json:"input_id"`
	Text    string            `json:"text"`
	Allow   bool              `json:"allow"`
	Actions map[string]string `json:"actions"`
	Tasks   []int             `json:"tasks"` // approve_card: plan tasks to create; absent means all
}

type cardSummary struct {
	ID         int64  `json:"id"`
	Title      string `json:"title"`
	Status     string `json:"status"`
	ProjectID  int64  `json:"project_id"`
	Activity   string `json:"activity"`
	RootFolder string `json:"root_folder"`
	Harness    string `json:"harness"`
	Kind       string `json:"kind"`
	Model      string `json:"model"`
	AutoRun    bool   `json:"auto_run"`
	WaitingOn  int64  `json:"waiting_on,omitempty"`
}

func (a *App) callBoardTool(name string, raw json.RawMessage) (string, bool) {
	var args boardArgs
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return "invalid arguments: " + err.Error(), true
		}
	}
	id := args.ID
	needID := func() error {
		if id == 0 {
			return errors.New("id is required")
		}
		return nil
	}
	// card returns the card after a change, or the error as the tool result.
	card := func(err error) (string, bool) {
		if err != nil {
			return err.Error(), true
		}
		c, err := a.store.GetCard(id)
		if err != nil {
			return err.Error(), true
		}
		return toolJSON(c)
	}

	switch name {
	case "list_projects":
		ps, err := a.ListProjects()
		if err != nil {
			return err.Error(), true
		}
		return toolJSON(ps)

	case "list_cards":
		if args.Status != "" && !statuses[args.Status] {
			return fmt.Sprintf("unknown column %q", args.Status), true
		}
		cards, err := a.store.ListCards()
		if err != nil {
			return err.Error(), true
		}
		out := []cardSummary{}
		for _, c := range cardsInProject(cards, args.Project) {
			if args.Status == "" || c.Status == args.Status {
				out = append(out, cardSummary{c.ID, c.Title, c.Status, c.ProjectID, c.Activity, c.RootFolder, c.Harness, c.kind(), c.Model, c.AutoRun, c.WaitingOn})
			}
		}
		return toolJSON(out)

	case "get_card":
		if err := needID(); err != nil {
			return err.Error(), true
		}
		c, err := a.store.GetCard(id)
		if err != nil {
			return err.Error(), true
		}
		return toolJSON(a.cardDetail(c, 20))

	case "create_card":
		c, err := a.CreateCard(args.cardInput)
		if err != nil {
			return err.Error(), true
		}
		return toolJSON(c)

	case "update_card":
		if err := needID(); err != nil {
			return err.Error(), true
		}
		c, err := a.EditCard(id, args.cardInput)
		if err != nil {
			return err.Error(), true
		}
		return toolJSON(c)

	case "move_card":
		if len(args.IDs) == 0 {
			if err := needID(); err != nil {
				return err.Error(), true
			}
			return card(a.moveOne(id, args.Status, args.Stop))
		}
		type result struct {
			ID    int64  `json:"id"`
			OK    bool   `json:"ok"`
			Error string `json:"error,omitempty"`
			Card  *Card  `json:"card,omitempty"`
		}
		results, failed := []result{}, false
		for _, cid := range args.IDs {
			res := result{ID: cid}
			if err := a.moveOne(cid, args.Status, args.Stop); err != nil {
				res.Error, failed = err.Error(), true
			} else if c, err := a.store.GetCard(cid); err == nil {
				res.OK, res.Card = true, c
			}
			results = append(results, res)
		}
		text, _ := toolJSON(results)
		return text, failed

	case "answer_input":
		if err := needID(); err != nil {
			return err.Error(), true
		}
		inputID := args.InputID
		if inputID == 0 {
			in, err := a.store.OpenInput(id)
			if err != nil {
				return err.Error(), true
			}
			if in == nil {
				return "card has no open input", true
			}
			inputID = in.ID
		}
		return card(a.answerBy(id, inputID, args.Text, args.Allow, viaMCP))

	case "approve_card":
		if err := needID(); err != nil {
			return err.Error(), true
		}
		actions := map[int64]string{}
		for k, v := range args.Actions {
			rid, err := strconv.ParseInt(k, 10, 64)
			if err != nil {
				return fmt.Sprintf("bad repo id %q in actions", k), true
			}
			actions[rid] = v
		}
		err := a.ApproveSelected(id, actions, args.Tasks)
		if err == nil {
			a.note(id, "approved by %s", viaMCP)
		}
		return card(err)

	case "discard_card":
		if err := needID(); err != nil {
			return err.Error(), true
		}
		err := a.Discard(id)
		if err == nil {
			a.note(id, "discarded by %s", viaMCP)
		}
		return card(err)

	case "send_back_card":
		if err := needID(); err != nil {
			return err.Error(), true
		}
		if args.Text == "" {
			return "feedback is required", true
		}
		return card(a.sendBackBy(id, args.Text, viaMCP))
	}
	return "unknown tool " + name, true
}

func (a *App) moveOne(id int64, to string, stop bool) error {
	if stop {
		return a.moveStoppingBy(id, to, viaMCP)
	}
	return a.moveBy(id, to, viaMCP)
}
