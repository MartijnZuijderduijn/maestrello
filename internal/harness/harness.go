package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"maestrello/internal/model"
)

// A Harness is the coding-agent CLI the board drives. Everything that is specific
// to one CLI lives behind this interface: which arguments and environment start a
// session, what its output stream looks like, how a refinement is run and read,
// and which efforts and permission modes it knows. The board keeps the run
// bookkeeping (turns, limits, cost, card transitions) and only asks the harness
// to translate. The Claude Code adapter is in harness_claude.go.
//
// A new adapter provides: command building (SessionCommand, RefineCommand),
// event parsing (ParseEvent, ParseRefineResult), its capabilities (Caps) and
// validation (ValidateEffort, ValidatePermissionMode), account and usage reporting
// (Account, ParseUsage), and registers itself in harnesses. See "Harness adapters" in SPEC.md.
type Harness interface {
	// Name is the registry key, as stored in Settings.Harness and Card.Harness.
	Name() string
	// Caps says what the CLI can do; the board falls back where it can't.
	Caps() Caps
	// SessionCommand builds the process for one agent session. It sets Dir, Env and
	// Stdin (the prompt) and writes whatever config file it needs (at
	// spec.MCPConfigPath). The board sets the process group and stderr and starts it.
	SessionCommand(spec SessionSpec) (*exec.Cmd, error)
	// ParseEvent reads one line of the session's stdout. An error means the line is
	// not something this harness understands; the board notes it and moves on.
	ParseEvent(line []byte) (HarnessEvent, error)
	// RefineCommand builds the one-shot, read-only refinement process, bound to ctx.
	RefineCommand(ctx context.Context, spec RefineSpec) (*exec.Cmd, error)
	// ParseRefineResult reads the refinement process's stdout.
	ParseRefineResult(out []byte) (RefineOutput, error)
	// ValidateEffort accepts "" (the harness default) and the efforts it knows.
	ValidateEffort(effort string) error
	// ValidatePermissionMode accepts the permission modes it knows, not "".
	ValidatePermissionMode(mode string) error
	// Account asks the CLI who it is logged in as, for the header. It runs the CLI
	// and gives up when ctx ends; the board sets the timeout and never calls it
	// per request. A logged-out CLI is an Account with LoggedIn false, not an
	// error. A harness with Caps.Account false returns ErrNoAccount.
	Account(ctx context.Context) (*Account, error)
	// ParseUsage reads usage-limit updates (one per window) out of one stdout line,
	// for the header. nil for a line that carries none, and always nil when
	// Caps.Usage is false. It must not decide anything about the run: whether a
	// run stops is HarnessEvent.RateLimited alone.
	ParseUsage(line []byte) []UsageLimit
}

// Caps are the things a harness may not be able to do. When one is false the
// board falls back as described in SPEC.md.
type Caps struct {
	Resume             bool // can continue an earlier session by id; else a resume starts fresh with the refined prompt + message
	PermissionCallback bool // asks the board's "permission" MCP tool before acting; else the permission mode is all there is
	StructuredOutput   bool // refinement can return JSON matching a schema; else the board reads the schema JSON from the final text
	PerTurnEvents      bool // the stream reports every turn; else the turn limit can't be enforced and cost/activity update only at the end
	Account            bool // Account can say who the CLI is logged in as; else the header shows "account unknown"
	Usage              bool // ParseUsage reads usage limits from the stream; else the header has no usage chips

	Efforts         []string // allowed non-empty efforts
	PermissionModes []string // allowed permission modes
}

// SessionSpec is everything a harness needs to start an agent session.
type SessionSpec struct {
	Card          *model.Card         // model, effort, permission mode, session id and kind (code, research, plan, chat), which decides the tools the session gets
	Prompt        string              // first message, sent on stdin
	Instructions  string              // board instructions, added to the system prompt
	MCPURL        string              // where the session reaches the board's MCP endpoint for this card
	MCPConfigPath string              // where the adapter writes the MCP config it needs
	Cwd           string              // working directory
	Root          string              // the card's root folder, readable by the agent
	Resume        bool                // continue Card.SessionID instead of starting it
	Strict        bool                // use only the board's MCP server (the board runs sandboxed)
	Context       []model.ContextItem // existing read-only reference paths of the card (never for chat)
}

// RefineSpec is everything a harness needs to run a refinement.
type RefineSpec struct {
	Model, Effort string
	Prompt        string // sent on stdin
	Cwd           string
	Tools         string   // read-only tools the refiner may use; "" for none
	MaxTurns      int      // turn cap where the CLI supports one; 0 for none
	Schema        string   // JSON schema of the answer
	ExtraDirs     []string // extra read-only folders the refiner may read (deep refinement with context)
}

// RefineOutput is what a refinement run returned.
type RefineOutput struct {
	Structured json.RawMessage // the answer as JSON (the schema's object), if there is one
	Text       string          // the final text, for messages
	CostUSD    float64
	IsError    bool // the run itself failed; Text says why
}

// HarnessEvent is one stream line in the board's terms. The flags say which
// bookkeeping the board does; a line may set none of them.
type HarnessEvent struct {
	Kind string // what the event log stores as the event's type

	Assistant bool   // an agent message: TurnID counts turns, Activity is what it's doing
	TurnID    string // "" when the message has no id
	Activity  string // "" to leave the card's activity as it is
	Text      string // the message's text, "" when it has none; a chat card shows the last one as the agent's reply

	Result      bool // the final result: CostUSD is added to the card
	CostUSD     float64
	IsError     bool   // the result is an error: ResultError becomes the failure reason
	ResultError string // text of the error result

	RateLimited  bool   // the run was refused for a usage limit
	RateLimitMsg string // what to show the user about it

	// SessionID is the id the CLI gave the session, for CLIs that cannot take a
	// preset one: the runner saves it on the card so a later resume finds it.
	SessionID string
}

// defaultHarness is what "" means, for old data and callers that don't say.
const Default = model.DefaultHarness

// HarnessConfig is what an adapter is built with: for now the path of its CLI.
type HarnessConfig struct {
	Bin string // path or name of the CLI; "" means the adapter's default name
}

// SessionChecker is an optional interface: an adapter whose session ids come from
// the CLI (not preset by the board) says whether an id can be resumed.
type SessionChecker interface {
	ResumableSession(id string) bool
}

// harnesses are the known adapters, built without a binary path. The App builds
// its own with the binaries it was started with (see App.harness). harnessesMu
// guards it, because Register lets tests add an adapter while boards run.
var (
	harnessesMu sync.RWMutex
	harnesses   = map[string]func(cfg HarnessConfig) Harness{
		"claude":   func(cfg HarnessConfig) Harness { return claudeHarness{bin: cfg.Bin} },
		"opencode": func(cfg HarnessConfig) Harness { return newOpencodeHarness(cfg.Bin) },
		"mistral":  func(cfg HarnessConfig) Harness { return newMistralHarness(cfg.Bin) },
	}
)

// Register adds an adapter to the registry and returns a function that removes it
// again. The built-in adapters are registered above; this is for tests that need
// an adapter with other capabilities.
func Register(name string, mk func(cfg HarnessConfig) Harness) (unregister func()) {
	harnessesMu.Lock()
	defer harnessesMu.Unlock()
	harnesses[name] = mk
	return func() {
		harnessesMu.Lock()
		defer harnessesMu.Unlock()
		delete(harnesses, name)
	}
}

// harnessFor resolves a harness name for validation and parsing; "" is claude.
// It cannot start processes, as it has no binary path: use App.harness for that.
func For(name string) (Harness, error) { return Build(name, nil) }

// buildHarness builds an adapter; bins maps harness name to its binary path.
func Build(name string, bins map[string]string) (Harness, error) {
	if name == "" {
		name = Default
	}
	harnessesMu.RLock()
	mk, ok := harnesses[name]
	harnessesMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unknown harness")
	}
	return mk(HarnessConfig{Bin: bins[name]}), nil
}

// oneOf is the shared check behind the adapters' Validate methods.
func oneOf(what, v string, allowed []string) error {
	for _, a := range allowed {
		if a == v {
			return nil
		}
	}
	return fmt.Errorf("invalid %s", what)
}

// ContextDirs is the folders to grant for context items: each dir, and the parent of each
// file; deduped, and without paths already under root.
func ContextDirs(items []model.ContextItem, root string) []string {
	var out []string
	seen := map[string]bool{}
	for _, it := range items {
		d := it.Path
		if it.Type != "dir" {
			d = filepath.Dir(d)
		}
		d = filepath.Clean(d)
		if seen[d] || (root != "" && (d == root || strings.HasPrefix(d, filepath.Clean(root)+string(filepath.Separator)))) {
			continue
		}
		seen[d] = true
		out = append(out, d)
	}
	return out
}

// Names lists the registered harnesses, sorted.
func Names() []string {
	harnessesMu.RLock()
	defer harnessesMu.RUnlock()
	return slices.Sorted(maps.Keys(harnesses))
}
