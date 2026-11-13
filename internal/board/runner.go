package board

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"maestrello/internal/harness"
	"maestrello/internal/model"
	"maestrello/internal/textutil"
)

// Run is one live claude process working a card.
type Run struct {
	cardID  int64
	cmd     *exec.Cmd
	started time.Time
	done    chan struct{} // closed once the session ended and finish settled the card

	mu         sync.Mutex
	stopReason string // turn_limit, time_limit, stopped, shutdown
	rateLimit  string
	resultErr  string
	turns      map[string]bool
	waitStart  time.Time // non-zero while an MCP tool call waits on me
	waited     time.Duration
	lastEvent  time.Time // last stream event, for the local-folder lease
	stderr     bytes.Buffer
	lastText   string // the agent's latest message text; a chat card shows it as the reply
}

// lastSign is when the session last showed it is alive: now while it waits on
// me, otherwise its last stream event.
func (r *Run) lastSign() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.waitStart.IsZero() {
		return time.Now()
	}
	if r.lastEvent.IsZero() {
		return r.started
	}
	return r.lastEvent
}

func (r *Run) activeTime() time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	d := time.Since(r.started) - r.waited
	if !r.waitStart.IsZero() {
		d -= time.Since(r.waitStart)
	}
	return d
}

func (r *Run) setWaiting(on bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if on && r.waitStart.IsZero() {
		r.waitStart = time.Now()
	} else if !on && !r.waitStart.IsZero() {
		r.waited += time.Since(r.waitStart)
		r.waitStart = time.Time{}
	}
}

func (r *Run) stop(reason string) {
	r.mu.Lock()
	if r.stopReason == "" {
		r.stopReason = reason
	}
	started := r.cmd.Process != nil
	r.mu.Unlock()
	if !started {
		return // startSession stops it right after it starts
	}
	pid := r.cmd.Process.Pid
	syscall.Kill(-pid, syscall.SIGINT)
	go func() {
		time.Sleep(5 * time.Second)
		syscall.Kill(-pid, syscall.SIGKILL)
	}()
}

func (a *App) liveRun(id int64) *Run {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.runs[id]
}

const agentInstructions = `You are an unattended agent working one card of the user's maestrello board.

Workspace: %[1]s
These are the places you may change:
%[3]s
A git repo is checked out in the workspace as a git worktree on branch %[2]s. Commit your work in each worktree on its branch. Do not push: the user decides about pushing.
A folder marked "local work" is not a git repo: edit its files in place, and do not run git there. The user reviews the changed files.
Everything else under %[4]s is READ-ONLY reference material: never edit files there, and never run git commands that change state (commit, checkout, reset, stash, branch, push) there.
If you need to change a repo or folder that is not listed yet, call mcp__maestrello__add_repo with its path relative to %[4]s ("." is the root folder itself); it returns where to work.
%[5]s
If you need a decision or information only the user has, call mcp__maestrello__ask_user instead of guessing. It returns the user's answer.
When the task is complete, call mcp__maestrello__report_done with a short summary of what changed and how you verified it.
If you cannot complete the task, call mcp__maestrello__report_failed with the reason.
After report_done or report_failed, end your turn.`

const readOnlyInstructions = `You are an unattended agent working one %[1]s card of the user's maestrello board.

Root folder: %[2]s
You are working in it. You may only read: use the Read, Grep and Glob tools. You have no way to edit anything, and you must never try to change files, run commands that change state, or create worktrees, branches or commits. There is no workspace to add repos to.
%[4]s
If you need a decision or information only the user has, call mcp__maestrello__ask_user instead of guessing. It returns the user's answer.
%[3]s
If you cannot complete the task, call mcp__maestrello__report_failed with the reason.
After that, end your turn.`

const researchFinish = `When you have your findings, call mcp__maestrello__report_done with the written report as the summary: what you looked at, what you found, and where (file paths and line numbers). The user reads the report in the review panel.`

const planFinish = `When the plan is ready, call mcp__maestrello__report_plan with the plan (scope, approach, order, risks) and its tasks. Each task has a title and a self-contained description a fresh agent can act on (goal, files involved, definition of done); give a task a kind of research or plan only if it is not a coding task. Order the tasks the way they should be done. Never create cards yourself: the user decides which tasks become cards.`

const chatInstructions = `You are chatting with the user through their maestrello board. You have no tools: you cannot read or change any files or run commands. Answer from what you know and from what the user tells you; your reply is shown to the user as it is, and then your turn ends and the board waits for their next message. Do not ask the user to wait, and do not describe work you cannot do.`

// extraToolsNote tells a read-only agent about the toolbox tools it has besides reading.
func extraToolsNote(card *model.Card) string {
	if s := card.ToolsSentence(); s != "" {
		return "\nBesides reading, you can " + s + ". These only fetch information; you still change nothing."
	}
	return ""
}

// sessionInstructions is the system prompt addition for a card's session.
// contextNote lists the card's context paths for the agent; "" when there are none.
func contextNote(card *model.Card) string {
	if len(card.Context) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Additional context: these paths are READ-ONLY reference material, outside the workspace. Read only these listed paths (for a file, only that file, even if its folder is readable), and never edit, create or delete anything there, nor run commands that change state:\n")
	for _, it := range card.Context {
		fmt.Fprintf(&b, "- %s (%s)\n", it.Path, it.Type)
	}
	return b.String()
}

func (a *App) sessionInstructions(card *model.Card, list string) string {
	switch card.EffectiveKind() {
	case model.KindResearch:
		return fmt.Sprintf(readOnlyInstructions, "research", card.RootFolder, researchFinish, contextNote(card)) + extraToolsNote(card)
	case model.KindPlan:
		return fmt.Sprintf(readOnlyInstructions, "plan", card.RootFolder, planFinish, contextNote(card)) + extraToolsNote(card)
	case model.KindChat:
		if s := card.ToolsSentence(); s != "" {
			return strings.Replace(chatInstructions, "You have no tools: you cannot read or change any files or run commands.",
				"You can "+s+"; you cannot read or change any files or run commands.", 1)
		}
		return chatInstructions
	}
	return fmt.Sprintf(agentInstructions, a.workDir(card.ID), branchName(card), list, card.RootFolder, contextNote(card))
}

// Run prepares the workspace for a ready card and starts its first session.
func (a *App) Run(id int64) error {
	// A Todo card that skips refinement needs no refined prompt: its own text is the prompt.
	if c, err := a.store.GetCard(id); err == nil && skippable(c) {
		if _, _, err := a.skipRefinement(id, c.RefinedPrompt); err != nil {
			return err
		}
	}
	card, err := a.update(id, func(c *model.Card) {
		if c.Status == model.StatusReady {
			c.Status = model.StatusRunning
			c.Activity = "preparing workspace"
			c.FailReason = ""
		}
	})
	if err != nil {
		return err
	}
	if card.Status != model.StatusRunning || card.Activity != "preparing workspace" {
		return fmt.Errorf("card is %s; only ready cards can be run", card.Status)
	}
	go func() {
		if !card.UsesRepos() {
			// Research and plan cards only read the root folder, and chat touches nothing:
			// no worktrees, no local-work lease, no automatic plan of ".".
			if card.EffectiveKind() == model.KindChat {
				if err := os.MkdirAll(a.workDir(id), 0o755); err != nil { // the chat's (empty) working directory
					a.fail(id, err.Error())
					return
				}
			}
			a.beginSession(id)
			return
		}
		repos, err := a.store.Repos(id)
		if err != nil {
			a.fail(id, err.Error())
			return
		}
		if err := os.MkdirAll(a.workDir(id), 0o755); err != nil {
			a.fail(id, err.Error())
			return
		}
		if len(repos) == 0 && a.plainFolder(card.RootFolder) {
			a.note(id, "no repos planned and %s holds no git repos: working there in place", card.RootFolder)
			repos = []*model.CardRepo{{RepoPath: "."}}
		}
		// Local work starts only once the card holds its folders, so LocalSince
		// (and the review of changed files) does not include an earlier card's edits.
		if err := a.claimLocal(card, a.localFolders(card, repos), "preparing workspace"); err != nil {
			a.claimFailed(id, err)
			return
		}
		for _, r := range repos {
			if r.WorktreePath != "" {
				continue
			}
			if _, err := a.checkoutRepo(card, r.RepoPath); err != nil {
				a.fail(id, fmt.Sprintf("checkout %s: %v", r.RepoPath, err))
				return
			}
		}
		a.beginSession(id)
	}()
	return nil
}

// beginSession gives a prepared card a new session id and starts its first session.
func (a *App) beginSession(id int64) {
	sessionID := newUUID()
	card, err := a.update(id, func(c *model.Card) { c.SessionID = sessionID })
	if err != nil {
		a.fail(id, err.Error())
		return
	}
	a.startSession(card, cmp.Or(card.RefinedPrompt, cardText(card)), false)
}

// plainFolder reports whether root is not a git repo and holds none, so a card
// without planned repos can only mean local work in root itself.
func (a *App) plainFolder(root string) bool {
	if a.git.IsRepo(root) || insideDir(root, a.dataDir) {
		return false
	}
	repos, err := a.git.DiscoverRepos(root, a.dataDir)
	return err == nil && len(repos) == 0
}

// checkoutRepo fetches the base, creates the card branch and worktree for one repo.
// A folder that is not a git repo becomes local work: the agent edits it in place.
// It is idempotent per repo; "." is the root folder itself.
func (a *App) checkoutRepo(card *model.Card, rel string) (*model.CardRepo, error) {
	rel, orig, err := a.resolveRepo(card, rel)
	if err != nil {
		return nil, err
	}
	if st, err := os.Stat(orig); err != nil || !st.IsDir() {
		return nil, fmt.Errorf("%s is not a folder", orig)
	}
	if insideDir(orig, a.dataDir) {
		return nil, fmt.Errorf("%s is the board's data folder, not a place to work", orig)
	}
	local := !a.git.IsRepo(orig)
	if local {
		// Claim the folder before recording it, so a refused add_repo leaves no trace.
		if err := a.acquireNow(card, orig); err != nil {
			return nil, err
		}
	}
	r := &model.CardRepo{CardID: card.ID, RepoPath: rel}
	if err := a.store.AddRepo(r); err != nil {
		return nil, err
	}
	repos, _ := a.store.Repos(card.ID)
	for _, existing := range repos {
		if existing.ID == r.ID {
			r = existing
		}
	}
	if r.WorktreePath != "" {
		return r, nil
	}
	if local {
		r.WorktreePath = orig
		r.LocalSince = time.Now().UTC().Format(time.RFC3339Nano)
		if err := a.store.SaveRepo(r); err != nil {
			return nil, err
		}
		a.note(card.ID, "%s is not a git repo: local work, edited in place", orig)
		return r, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	base, err := a.git.FetchBase(ctx, orig)
	if err != nil {
		return nil, err
	}
	r.OrigStatus, _ = a.git.Status(ctx, orig)
	r.Branch = branchName(card)
	r.BaseRef = base
	wt := filepath.Join(a.workDir(card.ID), rel)
	if rel == "." {
		wt = filepath.Join(a.workDir(card.ID), filepath.Base(card.RootFolder))
	}
	if err := a.git.AddWorktree(ctx, orig, wt, r.Branch, base); err != nil {
		return nil, err
	}
	r.WorktreePath = wt
	if err := a.store.SaveRepo(r); err != nil {
		return nil, err
	}
	a.note(card.ID, "checked out %s at %s on %s", rel, base, r.Branch)
	return r, nil
}

// resolveRepo checks a repo path relative to the card's root folder and returns
// it cleaned ("." is the root itself) along with the absolute path.
func (a *App) resolveRepo(card *model.Card, rel string) (string, string, error) {
	rel = filepath.Clean(strings.Trim(strings.TrimSpace(rel), "/"))
	if rel == "" {
		rel = "."
	}
	if rel == ".." || strings.HasPrefix(rel, "../") || filepath.IsAbs(rel) {
		return "", "", fmt.Errorf("invalid repo path %q", rel)
	}
	orig := filepath.Join(card.RootFolder, rel)
	if err := a.guard.Check(orig); err != nil {
		return "", "", err
	}
	return rel, orig, nil
}

// claimFailed handles a card that stopped waiting for its folders without getting them.
func (a *App) claimFailed(id int64, err error) {
	if errors.Is(err, errQueueCancelled) {
		return // whoever took it out of the queue has set its status
	}
	a.fail(id, err.Error())
}

func (a *App) mcpConfigPath(id int64) string {
	return filepath.Join(a.dataDir, "work", fmt.Sprintf("%d.mcp.json", id))
}

func (a *App) startSession(card *model.Card, prompt string, resume bool) {
	var repos []*model.CardRepo
	if card.UsesRepos() {
		repos, _ = a.store.Repos(card.ID)
	}
	var list strings.Builder
	cwd := a.workDir(card.ID)
	if card.ReadOnly() {
		cwd = card.RootFolder
	}
	for _, r := range repos {
		switch {
		case r.Local():
			fmt.Fprintf(&list, "- %s → %s (local work)\n", r.RepoPath, r.WorktreePath)
			if r.RepoPath == "." {
				cwd = r.WorktreePath
			}
		case r.WorktreePath != "":
			fmt.Fprintf(&list, "- %s → %s\n", r.RepoPath, r.WorktreePath)
		}
	}
	if list.Len() == 0 {
		list.WriteString("(none yet)\n")
	}
	// Only existing context paths go to the agent; chat sessions get none.
	var ctxItems []model.ContextItem
	if card.EffectiveKind() != model.KindChat {
		for _, it := range card.Context {
			if _, err := os.Stat(it.Path); err != nil {
				a.note(card.ID, "context path %s not found; skipped", it.Path)
				continue
			}
			ctxItems = append(ctxItems, it)
		}
	}
	withCtx := *card
	withCtx.Context = ctxItems
	instructions := a.sessionInstructions(&withCtx, list.String())

	if err := a.guard.Check(card.RootFolder); err != nil {
		a.fail(card.ID, err.Error())
		return
	}
	h, err := a.harness(card.Harness)
	if err == nil {
		err = a.requireHarness(card.Harness)
	}
	if err != nil {
		a.fail(card.ID, err.Error())
		return
	}
	if _, unsupported := card.NativeTools(h.Name()); len(unsupported) > 0 {
		for _, id := range unsupported {
			a.note(card.ID, "tool %s not supported by %s; skipped", id, h.Name())
		}
	}
	cmd, err := h.SessionCommand(harness.SessionSpec{
		Card: card, Prompt: prompt, Instructions: instructions,
		MCPURL:        fmt.Sprintf("%s/mcp/%d", a.baseURL, card.ID),
		MCPConfigPath: a.mcpConfigPath(card.ID),
		Cwd:           cwd, Root: card.RootFolder,
		Resume: resume && h.Caps().Resume, Strict: a.guard.Enabled(),
		Context: ctxItems,
	})
	if err != nil {
		a.fail(card.ID, err.Error())
		return
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	run := &Run{cardID: card.ID, cmd: cmd, turns: map[string]bool{}, done: make(chan struct{})}
	cmd.Stderr = &limitedWriter{buf: &run.stderr, max: 64 << 10}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		a.fail(card.ID, err.Error())
		return
	}

	a.mu.Lock()
	if a.runs[card.ID] != nil {
		a.mu.Unlock()
		a.note(card.ID, "a session is already running")
		return
	}
	a.runs[card.ID] = run
	a.busy.Add(1)
	a.mu.Unlock()

	a.update(card.ID, func(c *model.Card) {
		c.Status = model.StatusRunning
		c.Activity = "starting session"
		c.FailReason = ""
		c.WaitingOn = 0
	})
	a.note(card.ID, "%s session %s (%s, effort %s, %s)", map[bool]string{true: "resuming", false: "starting"}[resume],
		card.SessionID, card.Model, card.Effort, card.PermissionMode)
	if card.MaxTurns > 0 && !h.Caps().PerTurnEvents {
		a.note(card.ID, "%s reports no turns, so the turn limit of %d is not enforced (the time limit is)", h.Name(), card.MaxTurns)
	}

	run.mu.Lock()
	run.started = time.Now()
	err = cmd.Start()
	pendingStop := run.stopReason
	run.mu.Unlock()
	if err != nil {
		a.mu.Lock()
		delete(a.runs, card.ID)
		a.mu.Unlock()
		a.fail(card.ID, "start claude: "+err.Error())
		a.busy.Done()
		return
	}
	if pendingStop != "" {
		run.stop(pendingStop)
	}

	done := make(chan struct{})
	a.renewLeases(card.ID, card.SessionID, time.Now().Add(a.leaseTTLOrDefault()))
	go a.watchLimits(card, run, done)
	go a.heartbeat(card.ID, card.SessionID, run, done)
	go func() {
		a.readStream(card.ID, run, h, stdout)
		err := cmd.Wait()
		close(done)
		a.finish(card.ID, run, err)
		a.busy.Done()
	}()
}

func (a *App) watchLimits(card *model.Card, run *Run, done chan struct{}) {
	if card.MaxMinutes <= 0 {
		return
	}
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-done:
			return
		case <-t.C:
			if run.activeTime() > time.Duration(card.MaxMinutes)*time.Minute {
				a.note(card.ID, "time limit of %d minutes reached; stopping", card.MaxMinutes)
				run.stop("time_limit")
				return
			}
		}
	}
}

// readStream turns the harness's events into run bookkeeping: events stored,
// turns counted against the turn limit, cost and activity on the card, and the
// error and rate-limit outcome finish settles the card with.
func (a *App) readStream(id int64, run *Run, h harness.Harness, r io.Reader) {
	card, _ := a.store.GetCard(id)
	usage := h.Caps().Usage
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		run.mu.Lock()
		run.lastEvent = time.Now()
		run.mu.Unlock()
		ev, err := h.ParseEvent(line)
		if err != nil {
			a.note(id, "unparsed output: %s", textutil.Tail(string(line), 300))
			continue
		}
		a.event(id, ev.Kind, append(json.RawMessage(nil), line...))
		if ev.SessionID != "" {
			// A CLI that picks its own session ids: keep the one it made, for resume.
			a.update(id, func(c *model.Card) {
				if c.SessionID != ev.SessionID {
					c.SessionID = ev.SessionID
				}
			})
		}
		if usage {
			// Header usage display only; never changes the card or stops the run.
			if ups := h.ParseUsage(line); len(ups) > 0 {
				a.recordUsage(card.Harness, ups) // "" is the default harness
			}
		}

		switch {
		case ev.Assistant:
			run.mu.Lock()
			if ev.Text != "" {
				run.lastText = ev.Text
			}
			newTurn := ev.TurnID != "" && !run.turns[ev.TurnID]
			if newTurn {
				run.turns[ev.TurnID] = true
			}
			turns := len(run.turns)
			run.mu.Unlock()
			a.update(id, func(c *model.Card) {
				if newTurn {
					c.TurnsUsed++
				}
				if ev.Activity != "" {
					c.Activity = ev.Activity
				}
			})
			if newTurn && card.MaxTurns > 0 && turns >= card.MaxTurns {
				a.note(id, "turn limit of %d reached; stopping", card.MaxTurns)
				run.stop("turn_limit")
			}
		case ev.Result:
			a.update(id, func(c *model.Card) { c.CostUSD += ev.CostUSD })
			if ev.IsError {
				run.mu.Lock()
				run.resultErr = ev.ResultError
				if ev.RateLimited {
					run.rateLimit = ev.RateLimitMsg
				}
				run.mu.Unlock()
			}
		case ev.RateLimited:
			run.mu.Lock()
			run.rateLimit = ev.RateLimitMsg
			run.mu.Unlock()
		}
	}
}

func (a *App) finish(id int64, run *Run, waitErr error) {
	defer close(run.done) // after the card is settled: whoever waits on done sees the final status
	// Only a live session holds local folders. The process has exited, so release
	// before the run disappears: whoever sees the card idle sees its folders free.
	a.releaseLeases(id)
	a.mu.Lock()
	delete(a.runs, id)
	a.mu.Unlock()
	a.checkOriginals(id)

	run.mu.Lock()
	stopReason, rateLimit, resultErr, lastText := run.stopReason, run.rateLimit, run.resultErr, run.lastText
	run.mu.Unlock()
	stderr := textutil.Tail(run.stderr.String(), 800)

	card, err := a.store.GetCard(id)
	if err != nil {
		log.Printf("ERROR runner: card %d: could not load card to finish run: %v", id, err)
		return
	}
	switch {
	case card.Status == model.StatusReview || card.Status == model.StatusFailed:
		// The agent reported done or failed; that outcome stands.
		a.update(id, func(c *model.Card) { c.Activity = "" })
	case stopReason == "turn_limit" || stopReason == "time_limit":
		a.store.CreateInput(id, "limit", map[string]any{"reason": stopReason, "max_turns": card.MaxTurns, "max_minutes": card.MaxMinutes})
		a.update(id, func(c *model.Card) { c.Status = model.StatusNeedsInput; c.Activity = "" })
	case stopReason == "lease_expired":
		a.fail(id, fmt.Sprintf("the session showed no sign of life for %s, so its lease on its local folder expired and another card took the folder over", a.leaseTTLOrDefault()))
	case stopReason == "stopped":
		a.fail(id, "stopped by you")
	case stopReason == "shutdown":
		if in, _ := a.store.OpenInput(id); in != nil {
			// The question stays open; answering it after a restart resumes the session.
			a.update(id, func(c *model.Card) { c.Status = model.StatusNeedsInput; c.Activity = "" })
			return
		}
		a.fail(id, "board stopped while the agent was running")
	case rateLimit != "":
		a.store.CreateInput(id, "usage_limit", map[string]any{"message": rateLimit})
		a.update(id, func(c *model.Card) { c.Status = model.StatusNeedsInput; c.Activity = "" })
	default:
		if in, _ := a.store.OpenInput(id); in != nil {
			a.update(id, func(c *model.Card) { c.Status = model.StatusNeedsInput; c.Activity = "" })
			return
		}
		if card.EffectiveKind() == model.KindChat && resultErr == "" && waitErr == nil {
			// A chat turn ends without a report: the reply is for me to read and answer.
			a.store.CreateInput(id, "chat", map[string]any{"text": lastText})
			a.update(id, func(c *model.Card) { c.Status = model.StatusNeedsInput; c.Activity = "" })
			return
		}
		reason := "session ended without reporting done or failed"
		if resultErr != "" {
			reason = resultErr
		} else if waitErr != nil {
			reason = fmt.Sprintf("claude exited: %v", waitErr)
		}
		if stderr != "" {
			reason += "\n" + stderr
		}
		a.fail(id, reason)
	}
}

// checkOriginals flags the card if an original checkout changed while the agent ran.
func (a *App) checkOriginals(id int64) {
	card, err := a.store.GetCard(id)
	if err != nil {
		return
	}
	repos, _ := a.store.Repos(id)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	var changed []string
	for _, r := range repos {
		if r.WorktreePath == "" || r.Local() {
			continue
		}
		st, err := a.git.Status(ctx, filepath.Join(card.RootFolder, r.RepoPath))
		if err == nil && st != r.OrigStatus {
			changed = append(changed, r.RepoPath)
		}
	}
	if len(changed) > 0 {
		msg := "original checkout changed while the agent ran: " + strings.Join(changed, ", ")
		a.note(id, "%s", msg)
		a.update(id, func(c *model.Card) { c.Warning = msg })
	}
}

// Stop kills a running session, or takes a card out of the folder queue; the card goes to Failed.
func (a *App) Stop(id int64) error {
	run := a.liveRun(id)
	if run == nil {
		if a.cancelWait(id) {
			a.fail(id, "stopped by you while waiting for a folder")
			return nil
		}
		return fmt.Errorf("no running session")
	}
	run.stop("stopped")
	a.releaseWaiters(id)
	return nil
}

// Resume continues a card's session with a new user message.
func (a *App) Resume(id int64, message string) error {
	if a.liveRun(id) != nil {
		return fmt.Errorf("a session is already running")
	}
	if a.isWaiting(id) {
		return fmt.Errorf("the card is already waiting for a folder")
	}
	card, err := a.update(id, func(c *model.Card) {
		c.Status = model.StatusRunning
		c.Activity = "resuming"
		c.FailReason = ""
	})
	if err != nil {
		return err
	}
	h, err := a.harness(card.Harness)
	if err != nil {
		a.fail(id, err.Error())
		return err
	}
	prompt, resume := message, true
	if sc, ok := h.(harness.SessionChecker); card.SessionID == "" || !h.Caps().Resume || (ok && !sc.ResumableSession(card.SessionID)) {
		// Never got a session (e.g. checkout failed), or the harness can't resume it: start fresh.
		card, _ = a.update(id, func(c *model.Card) { c.SessionID = newUUID() })
		prompt, resume = cmp.Or(card.RefinedPrompt, cardText(card))+"\n\nAdditional instructions from the user:\n"+message, false
	}
	repos, _ := a.store.Repos(id)
	var folders []string
	for _, r := range repos {
		if r.Local() && r.Result == "" {
			folders = append(folders, leaseKey(r.WorktreePath))
		}
	}
	go func() {
		if err := a.claimLocal(card, folders, "resuming"); err != nil {
			a.claimFailed(id, err)
			return
		}
		a.startSession(card, prompt, resume)
	}()
	return nil
}

// SendBack moves a reviewed or failed card back to work with my feedback.
func (a *App) SendBack(id int64, feedback string) error { return a.sendBackBy(id, feedback, "you") }

func (a *App) sendBackBy(id int64, feedback, by string) error {
	card, err := a.store.GetCard(id)
	if err != nil {
		return err
	}
	if card.Status != model.StatusReview && card.Status != model.StatusFailed {
		return fmt.Errorf("card is %s; only review or failed cards can be sent back", card.Status)
	}
	a.note(id, "sent back by %s: %s", by, feedback)
	switch card.EffectiveKind() {
	case model.KindPlan:
		return a.Resume(id, "The user reviewed your plan and sent it back with this feedback:\n\n"+feedback+
			"\n\nRevise the plan and its tasks, and call report_plan again (or report_failed).")
	case model.KindResearch:
		return a.Resume(id, "The user read your report and sent it back with this feedback:\n\n"+feedback+
			"\n\nAddress it and call report_done again with the full revised report (or report_failed).")
	}
	return a.Resume(id, "The user reviewed your work and sent it back with this feedback:\n\n"+feedback+
		"\n\nAddress it, commit, and call report_done again (or report_failed).")
}

// Answer resolves the card's open input.
func (a *App) Answer(id, inputID int64, answer string, allow bool) error {
	return a.answerBy(id, inputID, answer, allow, "you")
}

func (a *App) answerBy(id, inputID int64, answer string, allow bool, by string) error {
	in, err := a.store.OpenInput(id)
	if err != nil {
		return err
	}
	if in == nil || in.ID != inputID {
		return fmt.Errorf("input %d is not open", inputID)
	}
	note := strings.TrimSpace(answer)
	if in.Kind == "permission" {
		answer = map[bool]string{true: "allow", false: "deny"}[allow]
		if note != "" {
			answer += ": " + note
		}
	}
	if err := a.store.AnswerInput(in.ID, answer); err != nil {
		return err
	}
	a.note(id, "%s answered (%s): %s", by, in.Kind, answer)

	a.mu.Lock()
	ch := a.waiters[in.ID]
	a.mu.Unlock()
	if ch != nil {
		ch <- inputReply{Answer: note, Allow: allow}
		return nil
	}

	switch in.Kind {
	case "limit", "usage_limit":
		if answer == "discard" {
			return a.Discard(id)
		}
		return a.Resume(id, "Continue where you left off. Call report_done or report_failed when finished.")
	case "permission":
		return a.Resume(id, fmt.Sprintf("The session was interrupted while waiting for permission. The user's decision: %s. Continue.", answer))
	case "chat":
		if note == "" {
			return fmt.Errorf("a reply is required")
		}
		return a.Resume(id, note)
	default:
		return a.Resume(id, "Your question was answered by the user:\n\n"+answer)
	}
}

// releaseWaiters unblocks any MCP calls waiting on me (used when stopping).
func (a *App) releaseWaiters(id int64) {
	in, _ := a.store.OpenInput(id)
	if in == nil {
		return
	}
	a.mu.Lock()
	ch := a.waiters[in.ID]
	a.mu.Unlock()
	if ch != nil {
		a.store.AnswerInput(in.ID, "stopped")
		select {
		case ch <- inputReply{Answer: "The session is being stopped.", Allow: false}:
		default:
		}
	}
}

type limitedWriter struct {
	mu  sync.Mutex
	buf *bytes.Buffer
	max int
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.buf.Len() < w.max {
		w.buf.Write(p)
	}
	return len(p), nil
}

// insideDir reports whether path is dir or below it.
func insideDir(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, "../")
}
