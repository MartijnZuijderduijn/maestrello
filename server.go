package main

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

//go:embed web
var webFS embed.FS

func (a *App) routes() http.Handler {
	mux := http.NewServeMux()
	static, _ := fs.Sub(webFS, "web")
	mux.Handle("GET /", http.FileServerFS(static))

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
	if errors.Is(err, ErrNotFound) {
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
	writeJSON(w, 200, map[string]any{"sandbox_root": a.guard.root, "data_dir": a.dataDir})
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
	for _, name := range slices.Sorted(maps.Keys(harnesses)) {
		h, err := harnessFor(name)
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

// validate checks the values a PUT /api/settings may save.
func (st Settings) validate() error {
	h, err := harnessFor(st.Harness) // "" is claude: the web UI does not send it
	if err != nil {
		return err
	}
	switch {
	case strings.TrimSpace(st.DefaultModel) == "":
		return errors.New("default model is required")
	case strings.TrimSpace(st.RefineModel) == "":
		return errors.New("refinement model is required")
	case st.DefaultEffort == "" && len(h.Caps().Efforts) > 0 || h.ValidateEffort(st.DefaultEffort) != nil:
		return errors.New("invalid default effort")
	case st.RefineEffort == "" && len(h.Caps().Efforts) > 0 || h.ValidateEffort(st.RefineEffort) != nil:
		return errors.New("invalid refinement effort")
	case h.ValidatePermissionMode(st.DefaultPermissionMode) != nil:
		return errors.New("invalid default permission mode")
	case !validPush[st.DefaultPushDefault]:
		return errors.New("invalid default push default")
	case st.DefaultMaxTurns < 0 || st.DefaultMaxMinutes < 0:
		return errors.New("limits must not be negative")
	}
	if err := validateModelInfo(st.ModelInfo); err != nil {
		return err
	}
	if err := validateKinds("kinds", st.Kinds); err != nil {
		return err
	}
	return st.validateDefaultTools()
}

func (a *App) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	var st Settings
	if err := json.NewDecoder(r.Body).Decode(&st); err != nil {
		writeErr(w, err)
		return
	}
	if err := st.validate(); err != nil {
		writeErr(w, err)
		return
	}
	if st.Harness == "" {
		st.Harness = defaultHarness
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

	context []ContextItem // Context after normalizeContext, which CreateCard and EditCard run before apply
}

const maxContextItems = 20

// normalizeContext validates paths for a card's context: absolute, inside the sandbox,
// existing, not in the data dir. It returns them cleaned, deduplicated and typed.
func (a *App) normalizeContext(paths []string) ([]ContextItem, error) {
	items := []ContextItem{}
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
		items = append(items, ContextItem{Path: p, Type: typ})
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

var validPush = map[string]bool{"": true, "local": true, "push": true, "pr": true}

func (in *cardInput) apply(c *Card) error {
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
		c.Harness = defaultHarness
	}
	if in.Kind != nil {
		k, err := normalizeKind(*in.Kind)
		if err != nil {
			return err
		}
		c.Kind = k
	}
	if in.Tools != nil {
		t, err := normalizeTools(*in.Tools)
		if err != nil {
			return err
		}
		c.Tools = t
	}
	if in.context != nil {
		c.Context = in.context
	}
	if c.Kind == "" {
		c.Kind = KindCode
	}
	if c.Kind == KindChat {
		c.SkipRefinement = true // a chat never refines: the card text is the first message
	}
	h, err := harnessFor(c.Harness)
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
	case !validPush[c.PushDefault]:
		return errors.New("invalid push default")
	}
	return nil
}

// CreateCard seeds a card from the settings defaults, applies the input and saves it in Todo.
// A card that skips refinement (skip_refinement, or a refined_prompt handed over without
// saying otherwise) goes straight to Ready with that prompt, and runs at once with auto_run.
func (a *App) CreateCard(in cardInput) (*Card, error) {
	// The project and kind pick the layers the defaults come from (see layers.go). The project is the
	// one named, or else the existing one for the root folder the card will have.
	proj, err := a.projectFor(in.ProjectID)
	if err != nil {
		return nil, err
	}
	global, _ := a.store.Settings()
	kind := KindCode
	if in.Kind != nil {
		if kind, err = normalizeKind(*in.Kind); err != nil {
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
		projectID = a.store.projectIDForRoot(root)
	}
	st, _, err := a.store.ResolveSettings(projectID, kind)
	if err != nil {
		st = global // layers unreadable: fall back to the global settings, as before
	}
	c := &Card{
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
func (a *App) afterSkip(id int64, prompt string) (*Card, error) {
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
func (a *App) cardDetail(c *Card, events int) map[string]any {
	repos, _ := a.store.Repos(c.ID)
	inputs, _ := a.store.Inputs(c.ID)
	evs, _ := a.store.Events(c.ID, events)
	return map[string]any{"card": c, "repos": repos, "inputs": inputs, "events": evs, "live": a.liveRun(c.ID) != nil}
}

// EditCard applies the input to a Todo or Ready card; a refused edit leaves it untouched.
// Setting skip_refinement on a Todo card sends it to Ready (see CreateCard); on a Ready
// card the flag cannot change, because the card has been through that choice already.
func (a *App) EditCard(id int64, in cardInput) (*Card, error) {
	skip := in.SkipRefinement != nil && *in.SkipRefinement || in.Kind != nil && strings.TrimSpace(*in.Kind) == KindChat
	proj, err := a.projectFor(in.ProjectID)
	if err != nil {
		return nil, err
	}
	if cur, err := a.store.GetCard(id); err == nil && skip && cur.Status == StatusTodo && in.Repos != nil {
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
	c, err := a.update(id, func(c *Card) {
		if c.Status != StatusTodo && c.Status != StatusReady {
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
		if applyErr == nil && before.Status != StatusTodo {
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
	if err == nil && skip && c.Status == StatusTodo {
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
	if err := a.deleteCard(id, false); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(204)
}

// handleClearCards deletes every Done card (DELETE /api/cards?status=done).
// Only status=done is accepted, so a typo can't wipe another column.
func (a *App) handleClearCards(w http.ResponseWriter, r *http.Request) {
	if st := r.URL.Query().Get("status"); st != StatusDone {
		writeErr(w, errors.New("only status=done can be cleared"))
		return
	}
	project, err := projectParam(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	done, err := a.store.CardsWithStatus(StatusDone)
	if err != nil {
		writeErr(w, err)
		return
	}
	done = cardsInProject(done, project)
	n := 0
	for _, c := range done {
		switch err := a.deleteCard(c.ID, true); {
		case err == nil:
			n++
		case errors.Is(err, errNotDone), errors.Is(err, ErrNotFound):
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
	if st := r.URL.Query().Get("status"); st != StatusReady {
		writeErr(w, errors.New("only status=ready can be started"))
		return
	}
	project, err := projectParam(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	ready, err := a.store.CardsWithStatus(StatusReady, StatusTodo)
	if err != nil {
		writeErr(w, err)
		return
	}
	ready = cardsInProject(ready, project)
	n, errs := 0, map[string]string{}
	for _, c := range ready {
		if c.Status == StatusTodo && !c.SkipRefinement {
			continue // only Todo cards that skip refinement can be started
		}
		switch err := a.Run(c.ID); {
		case err == nil:
			n++
		case errors.Is(err, ErrNotFound) || a.leftReady(c.ID):
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
	if st := r.URL.Query().Get("status"); st != StatusTodo {
		writeErr(w, errors.New("only status=todo can be refined"))
		return
	}
	project, err := projectParam(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	todo, err := a.store.CardsWithStatus(StatusTodo)
	if err != nil {
		writeErr(w, err)
		return
	}
	todo = cardsInProject(todo, project)
	n, errs := 0, map[string]string{}
	for _, c := range todo {
		if c.SkipRefinement || c.kind() == KindChat {
			continue // needs no refinement
		}
		switch err := a.Refine(c.ID); {
		case err == nil:
			n++
		case errors.Is(err, ErrNotFound):
		default:
			if cur, gerr := a.store.GetCard(c.ID); gerr == nil && cur.Status != StatusTodo && cur.Status != StatusFailed {
				continue // moved on since we listed it: not ours to refine
			}
			errs[strconv.FormatInt(c.ID, 10)] = err.Error()
		}
	}
	writeJSON(w, 200, map[string]any{"refined": n, "errors": errs})
}

// leftReady reports whether a card is no longer in Ready (or in Todo, for one that skips
// refinement), which is why Run refused it; such a card is skipped silently by a bulk start.
func (a *App) leftReady(id int64) bool {
	c, err := a.store.GetCard(id)
	return err != nil || c.Status != StatusReady && !skippable(c)
}

var errNotDone = errors.New("card is not done")

// deleteCard removes a card's board data (rows only; git branches are untouched)
// and tells open boards. With onlyDone set it refuses cards that are not in Done.
func (a *App) deleteCard(id int64, onlyDone bool) error {
	c, err := a.store.GetCard(id)
	if err != nil {
		return err
	}
	if onlyDone && c.Status != StatusDone {
		return errNotDone
	}
	if c.Status != StatusTodo && c.Status != StatusReady && c.Status != StatusDone {
		return errors.New("only todo, ready and done cards can be deleted")
	}
	if repos, _ := a.store.Repos(id); c.Status != StatusDone {
		for _, r := range repos {
			if r.WorktreePath != "" {
				return errors.New("card has worktrees; discard it instead")
			}
		}
	}
	if err := a.store.DeleteCard(id); err != nil {
		return err
	}
	a.hub.Publish("deleted", map[string]int64{"id": id})
	return nil
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
