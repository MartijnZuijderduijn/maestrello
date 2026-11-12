package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
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
// validation (ValidateEffort, ValidatePermissionMode), and registers itself in
// harnesses. See "Harness adapters" in SPEC.md.
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
}

// Caps are the things a harness may not be able to do. When one is false the
// board falls back as described in SPEC.md.
type Caps struct {
	Resume             bool // can continue an earlier session by id; else a resume starts fresh with the refined prompt + message
	PermissionCallback bool // asks the board's "permission" MCP tool before acting; else the permission mode is all there is
	StructuredOutput   bool // refinement can return JSON matching a schema; else the board reads the schema JSON from the final text
	PerTurnEvents      bool // the stream reports every turn; else the turn limit can't be enforced and cost/activity update only at the end

	Efforts         []string // allowed non-empty efforts
	PermissionModes []string // allowed permission modes
}

// SessionSpec is everything a harness needs to start an agent session.
type SessionSpec struct {
	Card          *Card         // model, effort, permission mode, session id and kind (code, research, plan, chat), which decides the tools the session gets
	Prompt        string        // first message, sent on stdin
	Instructions  string        // board instructions, added to the system prompt
	MCPURL        string        // where the session reaches the board's MCP endpoint for this card
	MCPConfigPath string        // where the adapter writes the MCP config it needs
	Cwd           string        // working directory
	Root          string        // the card's root folder, readable by the agent
	Resume        bool          // continue Card.SessionID instead of starting it
	Strict        bool          // use only the board's MCP server (the board runs sandboxed)
	Context       []ContextItem // existing read-only reference paths of the card (never for chat)
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

	UsageInfo bool // pass the raw line to recordRateLimit (header usage display only)

	// SessionID is the id the CLI gave the session, for CLIs that cannot take a
	// preset one: the runner saves it on the card so a later resume finds it.
	SessionID string
}

// defaultHarness is what "" means, for old data and callers that don't say.
const defaultHarness = "claude"

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
// its own with the binaries it was started with (see App.harness).
var harnesses = map[string]func(cfg HarnessConfig) Harness{
	"claude":   func(cfg HarnessConfig) Harness { return claudeHarness{bin: cfg.Bin} },
	"opencode": func(cfg HarnessConfig) Harness { return newOpencodeHarness(cfg.Bin) },
}

// harnessFor resolves a harness name for validation and parsing; "" is claude.
// It cannot start processes, as it has no binary path: use App.harness for that.
func harnessFor(name string) (Harness, error) { return buildHarness(name, nil) }

// buildHarness builds an adapter; bins maps harness name to its binary path.
func buildHarness(name string, bins map[string]string) (Harness, error) {
	if name == "" {
		name = defaultHarness
	}
	mk, ok := harnesses[name]
	if !ok {
		return nil, fmt.Errorf("unknown harness")
	}
	return mk(HarnessConfig{Bin: bins[name]}), nil
}

// harnessBins is the binary path each adapter was configured with, read at call time.
func (a *App) harnessBins() map[string]string {
	return map[string]string{"claude": a.claudeBin, "opencode": a.opencodeBin}
}

// harness resolves a harness name to an adapter that starts processes. It reads
// the per-adapter binaries (a.claudeBin, a.opencodeBin) at call time, so changing
// one after construction takes effect.
func (a *App) harness(name string) (Harness, error) { return buildHarness(name, a.harnessBins()) }

// oneOf is the shared check behind the adapters' Validate methods.
func oneOf(what, v string, allowed []string) error {
	for _, a := range allowed {
		if a == v {
			return nil
		}
	}
	return fmt.Errorf("invalid %s", what)
}
