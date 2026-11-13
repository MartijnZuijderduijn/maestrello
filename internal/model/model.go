package model

import (
	"encoding/json"
	"time"
)

// Tier scale: how capable a model is, 1 (small, fast, for simple edits) to 5 (the most capable).
const (
	MinTier = 1
	MaxTier = 5
)

// ModelMeta is what the board knows about one model. Both numbers are relative, not prices:
// Cost is in arbitrary units (0 = free, e.g. a local Ollama model), and only the ordering matters.
type ModelMeta struct {
	Tier int     `json:"tier"` // capability, 1 (small) to 5 (most capable)
	Cost float64 `json:"cost"` // relative price per task, 0 = free
}

// Overrides are the settings a layer may set. Field names match the JSON of Settings
// without the "default_" prefix where the global setting has one.
type Overrides struct {
	Harness        *string   `json:"harness,omitempty"`
	Model          *string   `json:"model,omitempty"`
	Effort         *string   `json:"effort,omitempty"`
	PermissionMode *string   `json:"permission_mode,omitempty"`
	MaxTurns       *int      `json:"max_turns,omitempty"`
	MaxMinutes     *int      `json:"max_minutes,omitempty"`
	PushDefault    *string   `json:"push_default,omitempty"`
	RefineModel    *string   `json:"refine_model,omitempty"`
	RefineEffort   *string   `json:"refine_effort,omitempty"`
	AutoRun        *bool     `json:"auto_run,omitempty"`
	SkipRefinement *bool     `json:"skip_refinement,omitempty"`
	AutoModel      *bool     `json:"auto_model,omitempty"`
	Tools          *[]string `json:"tools,omitempty"` // toolbox ids new cards of the kind start with
}

// LayerSettings is a project's own settings: overrides for every card in the project,
// and per card kind overrides within the project.
type LayerSettings struct {
	Overrides
	Kinds map[string]Overrides `json:"kinds,omitempty"`
}

// isEmpty reports whether the layer sets nothing.
func (l LayerSettings) IsEmpty() bool {
	b, _ := json.Marshal(l)
	return string(b) == "{}"
}

// A Project groups cards into one board and names the root folder new cards start with.
// It only organises cards: a card keeps its own root_folder, which drives everything else.
type Project struct {
	ID         int64          `json:"id"`
	Name       string         `json:"name"`
	RootFolder string         `json:"root_folder"`
	CreatedAt  time.Time      `json:"created_at"`
	UpdatedAt  time.Time      `json:"updated_at"`
	Counts     map[string]int `json:"counts,omitempty"` // cards per status; only in GET /api/projects
	// Settings override the global settings for this project's cards (see layers.go).
	Settings LayerSettings `json:"settings"`
}

var ValidPush = map[string]bool{"": true, "local": true, "merge": true, "push": true, "pr": true}

// Card statuses, in board order.
const (
	StatusTodo       = "todo"
	StatusRefining   = "refining"
	StatusReady      = "ready"
	StatusRunning    = "running"
	StatusNeedsInput = "needs_input"
	StatusReview     = "review"
	StatusFailed     = "failed"
	StatusDone       = "done"
)

type Card struct {
	ID                 int64         `json:"id"`
	Title              string        `json:"title"`
	Description        string        `json:"description"`
	RootFolder         string        `json:"root_folder"`
	ProjectID          int64         `json:"project_id"` // the project (board) the card belongs to
	RestrictRefinement bool          `json:"restrict_refinement"`
	TitleAuto          bool          `json:"title_auto"`      // the title is derived from the text, so the board may replace it; false once I typed one
	AutoRun            bool          `json:"auto_run"`        // run as soon as refinement succeeds, skipping review in Ready
	SkipRefinement     bool          `json:"skip_refinement"` // no refinement: the card text (or my own prompt) is the prompt, and the card goes straight to Ready
	DeepRefinement     bool          `json:"deep_refinement"` // the refiner may read the repos (read-only) before writing the prompt
	AutoModel          bool          `json:"auto_model"`      // the refiner picks the model and adapter for this card; the manual model is then only the fallback
	Model              string        `json:"model"`
	Effort             string        `json:"effort"`
	PermissionMode     string        `json:"permission_mode"`
	Harness            string        `json:"harness"` // which coding-agent CLI runs the card; see harness.go
	Kind               string        `json:"kind"`    // code, research, plan or chat; see kind.go
	Tools              []string      `json:"tools"`   // toolbox ids switched on for this card; see toolbox.go
	MaxTurns           int           `json:"max_turns"`
	MaxMinutes         int           `json:"max_minutes"`
	PushDefault        string        `json:"push_default"`
	RefinedPrompt      string        `json:"refined_prompt"`
	RefineNotes        string        `json:"refine_notes"`
	Status             string        `json:"status"`
	SessionID          string        `json:"session_id"`
	Summary            string        `json:"summary"`
	Context            []ContextItem `json:"context"`    // extra folders and files the agent may read; see normalizeContext
	PlanTasks          []PlanTask    `json:"plan_tasks"` // the tasks a plan card proposes; approving creates the cards
	FailReason         string        `json:"fail_reason"`
	Warning            string        `json:"warning"`
	Activity           string        `json:"activity"`
	WaitingOn          int64         `json:"waiting_on,omitempty"` // card holding the local folder this card waits for
	TurnsUsed          int           `json:"turns_used"`
	CostUSD            float64       `json:"cost_usd"`
	CreatedAt          time.Time     `json:"created_at"`
	UpdatedAt          time.Time     `json:"updated_at"`
}

// CardRepo is one repo a card works in. Before Run it is only a plan
// (no worktree yet); after Run it has a worktree and branch.
type CardRepo struct {
	ID           int64  `json:"id"`
	CardID       int64  `json:"card_id"`
	RepoPath     string `json:"repo_path"` // relative to the card's root folder
	WorktreePath string `json:"worktree_path"`
	Branch       string `json:"branch"`
	BaseRef      string `json:"base_ref"`
	OrigStatus   string `json:"-"` // `git status --porcelain` of the original checkout at checkout time
	Result       string `json:"result"`
	PRURL        string `json:"pr_url"`
	// LocalSince is set for local work: a folder that is not a git repo, which the
	// agent edits in place. It holds the RFC 3339 time the card started using it.
	LocalSince string `json:"local_since,omitempty"`
}

func (r *CardRepo) Local() bool { return r.LocalSince != "" }

type Input struct {
	ID         int64           `json:"id"`
	CardID     int64           `json:"card_id"`
	Kind       string          `json:"kind"` // question, permission, limit, usage_limit, chat
	Payload    json.RawMessage `json:"payload"`
	Answer     string          `json:"answer"`
	AnsweredAt *time.Time      `json:"answered_at"`
	CreatedAt  time.Time       `json:"created_at"`
}

type Event struct {
	ID     int64           `json:"id"`
	CardID int64           `json:"card_id"`
	TS     time.Time       `json:"ts"`
	Kind   string          `json:"kind"`
	Data   json.RawMessage `json:"data"`
}

// ContextItem is an extra folder or file a card's agent may read. Type is "dir" or "file".
type ContextItem struct {
	Path string `json:"path"`
	Type string `json:"type"`
}

type Settings struct {
	DefaultRootFolder     string `json:"default_root_folder"`
	DefaultModel          string `json:"default_model"`
	DefaultEffort         string `json:"default_effort"`
	DefaultPermissionMode string `json:"default_permission_mode"`
	DefaultMaxTurns       int    `json:"default_max_turns"`
	DefaultMaxMinutes     int    `json:"default_max_minutes"`
	DefaultPushDefault    string `json:"default_push_default"` // "" = decide at review
	RefineModel           string `json:"refine_model"`
	RefineEffort          string `json:"refine_effort"`
	DefaultAutoRun        bool   `json:"default_auto_run"`        // new cards run straight after refinement
	DefaultSkipRefinement bool   `json:"default_skip_refinement"` // new cards skip refinement and go straight to Ready
	Harness               string `json:"harness"`                 // coding-agent CLI for new cards and refinement; "" is claude
	// DefaultTools seeds a new card's tools by kind (kind → toolbox ids); a missing kind means none.
	DefaultTools map[string][]string `json:"default_tools"`
	// AutoModel is the default of the per-card auto_model option, which replaces the manual model:
	// the refiner picks the model (and adapter) for the task from the available ones. Off by default; see automodel.go.
	AutoModel bool `json:"auto_model"`
	// ModelInfo overrides or extends the built-in capability/cost catalog, keyed "<harness>/<model>".
	ModelInfo map[string]ModelMeta `json:"model_info"`
	// Kinds are global per-card-kind overrides of the settings above; see layers.go.
	Kinds map[string]Overrides `json:"kinds,omitempty"`
}

func DefaultSettings() Settings {
	return Settings{
		DefaultModel:          "sonnet",
		DefaultEffort:         "medium",
		DefaultPermissionMode: "auto",
		DefaultMaxTurns:       50,
		DefaultMaxMinutes:     45,
		RefineModel:           "sonnet",
		RefineEffort:          "low",
		Harness:               DefaultHarness,
	}
}

// DefaultHarness is what a harness name of "" means, for old data and callers that don't say.
const DefaultHarness = "claude"

// Statuses holds every card status.
var Statuses = map[string]bool{
	StatusTodo: true, StatusRefining: true, StatusReady: true, StatusRunning: true,
	StatusNeedsInput: true, StatusReview: true, StatusFailed: true, StatusDone: true,
}

// Skipping refinement and automatic model selection exclude each other: the refiner is what picks
// the model, so a card that skips it has no automatic choice to make. Skipping wins wherever both
// are found (stored cards and settings, layers); a new explicit choice of one over the other is
// resolved by the caller before it normalises.

// NormalizeRunFlags clears AutoModel on a card that skips refinement.
func (c *Card) NormalizeRunFlags() {
	if c.SkipRefinement {
		c.AutoModel = false
	}
}

// NormalizeRunFlags clears the AutoModel default when new cards skip refinement by default.
func (s *Settings) NormalizeRunFlags() {
	if s.DefaultSkipRefinement {
		s.AutoModel = false
	}
}
