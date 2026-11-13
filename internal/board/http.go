package board

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"maestrello/frontend"
	"maestrello/internal/harness"
	"maestrello/internal/model"
	"maestrello/internal/settings"
	"maestrello/internal/store"
)

// GET /api/settings/resolved?project=<id>&kind=<kind> shows what a new card of that kind in that
// project would start with, and which layer each overridden value came from.
func (a *App) handleResolvedSettings(w http.ResponseWriter, r *http.Request) {
	project, err := projectParam(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	kind, err := model.NormalizeKind(r.URL.Query().Get("kind"))
	if err != nil {
		writeErr(w, err)
		return
	}
	if project != 0 {
		if _, err := a.store.GetProject(project); err != nil {
			writeErr(w, err)
			return
		}
	}
	st, src, err := settings.Resolve(a.store, project, kind)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"project": project, "kind": kind, "settings": st, "sources": src})
}

func (a *App) routes() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServerFS(frontend.FS))

	mux.HandleFunc("GET /api/events", a.handleSSE)
	mux.HandleFunc("GET /api/info", a.handleInfo)
	mux.HandleFunc("GET /api/account", a.handleAccount)
	mux.HandleFunc("POST /api/shutdown", a.handleShutdown)
	mux.HandleFunc("GET /api/settings", a.handleGetSettings)
	mux.HandleFunc("PUT /api/settings", a.handlePutSettings)
	mux.HandleFunc("GET /api/settings/resolved", a.handleResolvedSettings)
	mux.HandleFunc("GET /api/repos", a.handleRepos)
	mux.HandleFunc("GET /api/fs/list", a.handleFSList)
	mux.HandleFunc("GET /api/models", a.handleModels)
	mux.HandleFunc("GET /api/harnesses", a.handleHarnesses)
	mux.HandleFunc("GET /api/toolbox", a.handleToolbox)

	mux.HandleFunc("GET /api/projects", a.handleListProjects)
	mux.HandleFunc("POST /api/projects", a.handleCreateProject)
	mux.HandleFunc("PATCH /api/projects/{id}", a.handlePatchProject)
	mux.HandleFunc("DELETE /api/projects/{id}", a.handleDeleteProject)

	mux.HandleFunc("GET /api/cards", a.handleListCards)
	mux.HandleFunc("POST /api/cards", a.handleCreateCard)
	mux.HandleFunc("DELETE /api/cards", a.handleClearCards)
	mux.HandleFunc("POST /api/cards/run", a.handleRunCards)
	mux.HandleFunc("POST /api/cards/refine", a.handleRefineCards)
	mux.HandleFunc("GET /api/cards/{id}", a.handleGetCard)
	mux.HandleFunc("PATCH /api/cards/{id}", a.handlePatchCard)
	mux.HandleFunc("PATCH /api/cards/{id}/agent", a.handlePatchAgent)
	mux.HandleFunc("DELETE /api/cards/{id}", a.handleDeleteCard)
	mux.HandleFunc("GET /api/cards/{id}/diff", a.handleDiff)
	mux.HandleFunc("POST /api/cards/{id}/{action}", a.handleAction)

	for _, m := range []string{"GET", "POST", "DELETE"} {
		mux.HandleFunc(m+" /mcp/{card}", a.handleMCP)
		mux.HandleFunc(m+" /mcp", a.handleBoardMCP) // the whole board, for me and my own harnesses
	}
	return mux
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	if errors.Is(err, store.ErrNotFound) {
		status = http.StatusNotFound
	}
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func pathID(r *http.Request) (int64, error) {
	return strconv.ParseInt(r.PathValue("id"), 10, 64)
}

func (a *App) handleSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	ch := a.hub.Subscribe()
	defer a.hub.Unsubscribe(ch)
	w.Write([]byte(": connected\n\n"))
	flusher.Flush()
	for {
		select {
		case <-r.Context().Done():
			return
		case msg := <-ch:
			w.Write(msg)
			flusher.Flush()
		}
	}
}

func (a *App) handleInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"sandbox_root": a.guard.Root(), "data_dir": a.dataDir})
}

// knownModels are the values the claude CLI accepts for --model. The CLI has no
// command that lists models (running `claude models` would start a session), so
// this static list of aliases and full IDs is the source of truth.
var knownModels = []string{
	"sonnet", "opus", "haiku",
	"claude-fable-5-2", "claude-opus-5-5", "claude-sonnet-5-5", "claude-haiku-5-5",
}

func (a *App) handleModels(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, knownModels)
}

// harnessInfo is what the UI needs of a harness to fill its selects and to show
// whether it can be used on this machine.
type harnessInfo struct {
	Name            string   `json:"name"`
	Efforts         []string `json:"efforts"`
	PermissionModes []string `json:"permission_modes"`
	Available       bool     `json:"available"`
	Version         string   `json:"version"`
	Error           string   `json:"error"`
	Models          []string `json:"models"`
}

// handleHarnesses lists the registered harnesses by name, with what each accepts
// and the cached availability check; ?refresh=1 re-checks first.
func (a *App) handleHarnesses(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("refresh") != "" && r.URL.Query().Get("refresh") != "0" {
		a.refreshAvailability()
	}
	out := []harnessInfo{}
	for _, name := range harness.Names() {
		h, err := harness.For(name)
		if err != nil {
			continue
		}
		c := h.Caps()
		st := a.harnessStatus(name)
		models := st.Models
		if name == "claude" {
			models = knownModels
		}
		if models == nil {
			models = []string{}
		}
		out = append(out, harnessInfo{name, c.Efforts, c.PermissionModes, st.Available, st.Version, st.Error, models})
	}
	writeJSON(w, 200, out)
}

func (a *App) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	st, err := a.store.Settings()
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, st)
}

func (a *App) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	var st model.Settings
	if err := json.NewDecoder(r.Body).Decode(&st); err != nil {
		writeErr(w, err)
		return
	}
	if err := settings.Validate(st); err != nil {
		writeErr(w, err)
		return
	}
	if st.Harness == "" {
		st.Harness = harness.Default
	}
	if st.Kinds == nil { // a client that does not know about kinds (the older UI) must not wipe them; {} clears
		if cur, err := a.store.Settings(); err == nil {
			st.Kinds = cur.Kinds
		}
	}
	if st.DefaultRootFolder != "" {
		if err := a.guard.Check(st.DefaultRootFolder); err != nil {
			writeErr(w, err)
			return
		}
	}
	if err := a.store.SaveSettings(st); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, st)
}

func (a *App) handleRepos(w http.ResponseWriter, r *http.Request) {
	repos, err := a.git.DiscoverRepos(r.URL.Query().Get("root"), a.dataDir)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, repos)
}

func (a *App) handleListCards(w http.ResponseWriter, r *http.Request) {
	project, err := projectParam(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	cards, err := a.store.ListCards()
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, cardsInProject(cards, project))
}

func (a *App) handleCreateCard(w http.ResponseWriter, r *http.Request) {
	var in cardInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, err)
		return
	}
	c, err := a.CreateCard(in)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 201, c)
}

func (a *App) handleGetCard(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	c, err := a.store.GetCard(id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, a.cardDetail(c, 500))
}

// cardDetail is what GET /api/cards/{id} returns, with at most the last events events.
func (a *App) cardDetail(c *model.Card, events int) map[string]any {
	repos, _ := a.store.Repos(c.ID)
	inputs, _ := a.store.Inputs(c.ID)
	evs, _ := a.store.Events(c.ID, events)
	return map[string]any{"card": c, "repos": repos, "inputs": inputs, "events": evs, "live": a.liveRun(c.ID) != nil}
}

func (a *App) handlePatchCard(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var in cardInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, err)
		return
	}
	c, err := a.EditCard(id, in)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, c)
}

func (a *App) handleDeleteCard(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := a.deleteCard(id, false, r.URL.Query().Get("stop") == "1"); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(204)
}

// handleClearCards deletes every Done card (DELETE /api/cards?status=done).
// Only status=done is accepted, so a typo can't wipe another column.
func (a *App) handleClearCards(w http.ResponseWriter, r *http.Request) {
	if st := r.URL.Query().Get("status"); st != model.StatusDone {
		writeErr(w, errors.New("only status=done can be cleared"))
		return
	}
	project, err := projectParam(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	done, err := a.store.CardsWithStatus(model.StatusDone)
	if err != nil {
		writeErr(w, err)
		return
	}
	done = cardsInProject(done, project)
	n := 0
	for _, c := range done {
		switch err := a.deleteCard(c.ID, true, false); {
		case err == nil:
			n++
		case errors.Is(err, errNotDone), errors.Is(err, store.ErrNotFound):
			// Left Done (or was deleted) since we listed it: leave it alone.
		default:
			writeErr(w, err)
			return
		}
	}
	writeJSON(w, 200, map[string]int{"deleted": n})
}

// handleRunCards starts every Ready card, and every Todo card that skips refinement
// (POST /api/cards/run?status=ready), as if Run had been pressed on each. Only status=ready is accepted, mirroring
// handleClearCards, so a typo can't start another column. Cards run in id order
// (the board's order), which keeps the local-folder queue first in, first out.
func (a *App) handleRunCards(w http.ResponseWriter, r *http.Request) {
	if st := r.URL.Query().Get("status"); st != model.StatusReady {
		writeErr(w, errors.New("only status=ready can be started"))
		return
	}
	project, err := projectParam(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	ready, err := a.store.CardsWithStatus(model.StatusReady, model.StatusTodo)
	if err != nil {
		writeErr(w, err)
		return
	}
	ready = cardsInProject(ready, project)
	n, errs := 0, map[string]string{}
	for _, c := range ready {
		if c.Status == model.StatusTodo && !c.SkipRefinement {
			continue // only Todo cards that skip refinement can be started
		}
		switch err := a.Run(c.ID); {
		case err == nil:
			n++
		case errors.Is(err, store.ErrNotFound) || a.leftReady(c.ID):
			// Moved, run or deleted since we listed it: not ours to start.
		default:
			errs[strconv.FormatInt(c.ID, 10)] = err.Error()
		}
	}
	writeJSON(w, 200, map[string]any{"started": n, "errors": errs})
}

// handleRefineCards refines every Todo card (POST /api/cards/refine?status=todo), as if Refine had been pressed on
// each. Only status=todo is accepted, mirroring handleRunCards. Todo cards that skip refinement and chat cards
// are left alone; a card that failed to start refining is reported in errors and does not stop the others.
func (a *App) handleRefineCards(w http.ResponseWriter, r *http.Request) {
	if st := r.URL.Query().Get("status"); st != model.StatusTodo {
		writeErr(w, errors.New("only status=todo can be refined"))
		return
	}
	project, err := projectParam(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	todo, err := a.store.CardsWithStatus(model.StatusTodo)
	if err != nil {
		writeErr(w, err)
		return
	}
	todo = cardsInProject(todo, project)
	n, errs := 0, map[string]string{}
	for _, c := range todo {
		if c.SkipRefinement || c.EffectiveKind() == model.KindChat {
			continue // needs no refinement
		}
		switch err := a.Refine(c.ID); {
		case err == nil:
			n++
		case errors.Is(err, store.ErrNotFound):
		default:
			if cur, gerr := a.store.GetCard(c.ID); gerr == nil && cur.Status != model.StatusTodo && cur.Status != model.StatusFailed {
				continue // moved on since we listed it: not ours to refine
			}
			errs[strconv.FormatInt(c.ID, 10)] = err.Error()
		}
	}
	writeJSON(w, 200, map[string]any{"refined": n, "errors": errs})
}

func (a *App) handleDiff(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	diffs, err := a.Diffs(id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, diffs)
}

func (a *App) handleAction(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var body struct {
		Text    string            `json:"text"`
		InputID int64             `json:"input_id"`
		Allow   bool              `json:"allow"`
		Actions map[string]string `json:"actions"` // repo id → local|push|pr
		Status  string            `json:"status"`  // target column for move
		Stop    bool              `json:"stop"`    // move: halt a live session first (I confirmed)
		Tasks   []int             `json:"tasks"`   // approve a plan: indices of the tasks to create; absent means all
	}
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, err)
			return
		}
	}
	switch r.PathValue("action") {
	case "refine":
		err = a.Refine(id)
	case "run":
		err = a.Run(id)
	case "stop":
		err = a.Stop(id)
	case "answer":
		err = a.Answer(id, body.InputID, body.Text, body.Allow)
	case "send-back":
		if strings.TrimSpace(body.Text) == "" {
			err = errors.New("feedback is required")
		} else {
			err = a.SendBack(id, body.Text)
		}
	case "approve":
		actions := map[int64]string{}
		for k, v := range body.Actions {
			rid, _ := strconv.ParseInt(k, 10, 64)
			actions[rid] = v
		}
		err = a.ApproveSelected(id, actions, body.Tasks)
	case "discard":
		err = a.Discard(id)
	case "move":
		if body.Stop {
			err = a.MoveStopping(id, body.Status)
		} else {
			err = a.Move(id, body.Status)
		}
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
