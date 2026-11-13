package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	"maestrello/internal/model"
	"maestrello/internal/textutil"
)

// opencodeHarness drives the opencode CLI (`opencode run --format json`).
//
// What this adapter relies on (opencode 1.2.x):
//   - `opencode run` takes the message on stdin, `-m provider/model` (Ollama models
//     are `ollama/<name>`), `-s <id>` to continue a session, `--agent <name>` and
//     `--format json` for one JSON event per line.
//   - It cannot start a session under a preset id: the CLI makes the id ("ses_...")
//     and puts it in every event. The adapter reports it as HarnessEvent.SessionID,
//     and the runner saves it on the card, so Resume works from the second run on.
//   - OPENCODE_CONFIG names a JSON config file that is merged over the user's own
//     config (providers such as Ollama stay). It carries the board's MCP server (type
//     "remote"), an agent "maestrello" with the card kind's tools and permissions, and
//     the board instructions (as an instructions file, so opencode's own system
//     prompt stays). OPENCODE_CONFIG_CONTENT does the same inline for refinements.
//   - Board MCP tools are named `maestrello_<tool>` in opencode, not
//     `mcp__maestrello__<tool>`; the instructions are rewritten to match.
//   - Non-interactive runs cannot answer a permission question, so every permission
//     is set to allow or deny and never ask (no permission callback).
//
// Limits: opencode has no "only this MCP server" switch, so MCP servers from the
// user's own opencode config stay configured in Strict mode (built-in tools are still
// restricted, and webfetch is denied). The stream carries no session-level "result"
// line; each finished step reports its cost.
type opencodeHarness struct {
	bin string // path to the opencode CLI
}

func newOpencodeHarness(bin string) opencodeHarness {
	if bin == "" {
		bin = "opencode"
	}
	return opencodeHarness{bin: bin}
}

func (opencodeHarness) Name() string { return "opencode" }

func (opencodeHarness) Caps() Caps {
	return Caps{
		Resume: true, PermissionCallback: false, StructuredOutput: false, PerTurnEvents: true,
		Efforts:         []string{}, // `--variant` is provider-specific, so the board offers no effort
		PermissionModes: []string{"auto", "plan"},
	}
}

func (h opencodeHarness) ValidateEffort(effort string) error {
	if effort == "" {
		return nil
	}
	return oneOf("effort", effort, h.Caps().Efforts)
}

func (h opencodeHarness) ValidatePermissionMode(mode string) error {
	return oneOf("permission mode", mode, h.Caps().PermissionModes)
}

// ResumableSession: only ids opencode made itself can be continued.
func (opencodeHarness) ResumableSession(id string) bool { return strings.HasPrefix(id, "ses_") }

// opencode's built-in tools by what they do.
var (
	ocReadTools  = []string{"read", "grep", "glob", "list"}
	ocWriteTools = []string{"write", "edit", "patch", "bash", "webfetch", "websearch", "todowrite", "todoread", "task"}
)

// ocTools is the agent's tool switchboard: everything off ("*"), then the given
// built-ins on, then the board tools that do not belong to this kind off.
func ocTools(enabled []string, off ...string) map[string]bool {
	t := map[string]bool{"*": false}
	for _, n := range ocReadTools {
		t[n] = false
	}
	for _, n := range ocWriteTools {
		t[n] = false
	}
	for _, n := range enabled {
		t[n] = true
	}
	for _, n := range off {
		t[n] = false
	}
	return t
}

func ocSessionConfig(spec SessionSpec, instructionsPath string) map[string]any {
	card := spec.Card
	perm := map[string]string{"edit": "allow", "bash": "allow", "webfetch": "allow", "external_directory": "allow"}
	var tools map[string]bool
	board := func(names ...string) []string { // the board tools of this kind, as opencode names them
		var out []string
		for _, n := range names {
			out = append(out, "maestrello_"+n)
		}
		return out
	}
	allBoard := []string{"report_done", "report_failed", "ask_user", "report_plan", "add_repo", "permission"}
	keep := func(names ...string) []string { // board tools to switch off: all but names
		var off []string
		for _, n := range allBoard {
			if !slices.Contains(names, n) {
				off = append(off, "maestrello_"+n)
			}
		}
		return off
	}
	switch card.EffectiveKind() {
	case model.KindResearch:
		tools = ocTools(append(append([]string{}, ocReadTools...), board("report_done", "report_failed", "ask_user")...), keep("report_done", "report_failed", "ask_user")...)
	case model.KindPlan:
		tools = ocTools(append(append([]string{}, ocReadTools...), board("report_plan", "report_failed", "ask_user")...), keep("report_plan", "report_failed", "ask_user")...)
	case model.KindChat:
		tools = ocTools(board("report_failed", "ask_user"), keep("report_failed", "ask_user")...)
		perm["external_directory"] = "deny"
	default:
		all := append(append([]string{}, ocReadTools...), ocWriteTools...)
		tools = ocTools(append(all, board("report_done", "report_failed", "ask_user", "add_repo")...), keep("report_done", "report_failed", "ask_user", "add_repo")...)
	}
	if card.EffectiveKind() != model.KindCode || card.PermissionMode == "plan" {
		perm["edit"], perm["bash"] = "deny", "deny" // read-only
		if card.EffectiveKind() == model.KindCode {
			for _, n := range ocWriteTools { // plan mode on a code card: look, don't touch
				tools[n] = false
			}
		}
	}
	if spec.Strict {
		perm["webfetch"] = "deny" // unless the card selected web_fetch, below
	}
	// The card's toolbox tools: switched on and allowed, also in a read-only kind, since they
	// change nothing. A tool opencode lacks is skipped (the runner notes it on the card).
	enabled, _ := card.NativeTools("opencode")
	for _, n := range enabled {
		tools[n] = true
		perm[n] = "allow"
	}
	cfg := map[string]any{
		"$schema": "https://opencode.ai/config.json",
		"mcp": map[string]any{
			"maestrello": map[string]any{"type": "remote", "url": spec.MCPURL, "enabled": true,
				"timeout": 86400000}, // ask_user blocks until I answer
		},
		"agent": map[string]any{
			"maestrello": map[string]any{"mode": "primary", "tools": tools, "permission": perm},
		},
	}
	if instructionsPath != "" {
		cfg["instructions"] = []string{instructionsPath}
	}
	return cfg
}

func ocEnv(extra ...string) []string {
	return append(os.Environ(), append([]string{"OPENCODE_DISABLE_AUTOUPDATE=true"}, extra...)...)
}

func (h opencodeHarness) SessionCommand(spec SessionSpec) (*exec.Cmd, error) {
	card := spec.Card
	instrPath := spec.MCPConfigPath + ".instructions.md"
	text := strings.ReplaceAll(spec.Instructions, "mcp__maestrello__", "maestrello_")
	if err := os.WriteFile(instrPath, []byte(text), 0o600); err != nil {
		return nil, err
	}
	data, err := json.Marshal(ocSessionConfig(spec, instrPath))
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(spec.MCPConfigPath, data, 0o600); err != nil {
		return nil, err
	}
	args := []string{"run", "--format", "json", "--agent", "maestrello"}
	if card.Model != "" {
		args = append(args, "-m", card.Model)
	}
	if spec.Resume {
		args = append(args, "-s", card.SessionID)
	}
	cmd := exec.Command(h.bin, args...)
	cmd.Dir = spec.Cwd
	cmd.Env = ocEnv("OPENCODE_CONFIG=" + spec.MCPConfigPath)
	cmd.Stdin = strings.NewReader(spec.Prompt)
	return cmd, nil
}

// No turn cap: opencode run has no such flag (spec.MaxTurns is ignored).
func (h opencodeHarness) RefineCommand(ctx context.Context, spec RefineSpec) (*exec.Cmd, error) {
	var enabled []string
	for _, t := range strings.Split(spec.Tools, ",") {
		switch strings.ToLower(strings.TrimSpace(t)) {
		case "read":
			enabled = append(enabled, "read", "list")
		case "grep":
			enabled = append(enabled, "grep")
		case "glob":
			enabled = append(enabled, "glob")
		}
	}
	extDir := "deny"
	if len(spec.ExtraDirs) > 0 {
		extDir = "allow" // context folders lie outside the working dir; edit, bash and webfetch stay denied
	}
	cfg, err := json.Marshal(map[string]any{
		"$schema": "https://opencode.ai/config.json",
		"agent": map[string]any{"maestrello": map[string]any{
			"mode": "primary", "tools": ocTools(enabled),
			"permission": map[string]string{"edit": "deny", "bash": "deny", "webfetch": "deny", "external_directory": extDir},
		}},
	})
	if err != nil {
		return nil, err
	}
	args := []string{"run", "--format", "json", "--agent", "maestrello"}
	if spec.Model != "" {
		args = append(args, "-m", spec.Model)
	}
	cmd := exec.CommandContext(ctx, h.bin, args...)
	cmd.Dir = spec.Cwd
	cmd.Env = ocEnv("OPENCODE_CONFIG_CONTENT=" + string(cfg))
	// No structured output: ask for the JSON in the final text.
	cmd.Stdin = strings.NewReader(spec.Prompt + "\n\nAnswer with ONLY one JSON object that matches this JSON schema, and nothing else:\n" + spec.Schema + "\n")
	return cmd, nil
}

// ocEvent is the part of an `opencode run --format json` line I read.
type ocEvent struct {
	Type      string `json:"type"`
	SessionID string `json:"sessionID"`
	Part      struct {
		MessageID string `json:"messageID"`
		Text      string `json:"text"`
		Tool      string `json:"tool"`
		State     struct {
			Input json.RawMessage `json:"input"`
		} `json:"state"`
	} `json:"part"`
	Error struct {
		Name string `json:"name"`
		Data struct {
			Message string `json:"message"`
		} `json:"data"`
		Message string `json:"message"`
	} `json:"error"`
}

func (e ocEvent) errText() string {
	switch {
	case e.Error.Data.Message != "":
		return e.Error.Data.Message
	case e.Error.Message != "":
		return e.Error.Message
	case e.Error.Name != "":
		return e.Error.Name
	}
	return "opencode reported an error"
}

func (opencodeHarness) ParseEvent(line []byte) (HarnessEvent, error) {
	var ev ocEvent
	if err := json.Unmarshal(line, &ev); err != nil {
		return HarnessEvent{}, err
	}
	// Part.Cost has no JSON tag of its own: read it from the raw part.
	var raw struct {
		Part struct {
			Cost float64 `json:"cost"`
		} `json:"part"`
	}
	json.Unmarshal(line, &raw)
	out := HarnessEvent{Kind: ev.Type, SessionID: ev.SessionID}
	switch ev.Type {
	case "text":
		out.Assistant, out.TurnID = true, ev.Part.MessageID
		if t := strings.TrimSpace(ev.Part.Text); t != "" {
			out.Text, out.Activity = t, textutil.FirstLine(t, 120)
		}
	case "tool_use":
		out.Assistant, out.TurnID = true, ev.Part.MessageID
		out.Activity = toolActivity(ev.Part.Tool, ocToolInput(ev.Part.State.Input))
	case "step_finish":
		out.Result, out.CostUSD = true, raw.Part.Cost
	case "error":
		msg := ev.errText()
		out.Result, out.IsError, out.ResultError = true, true, msg
		if rateLimitRe.MatchString(msg) {
			out.RateLimited, out.RateLimitMsg = true, msg
		}
	}
	return out, nil
}

// ocToolInput renames opencode's camelCase "filePath" so toolActivity finds it.
func ocToolInput(in json.RawMessage) json.RawMessage {
	var m map[string]any
	if json.Unmarshal(in, &m) != nil {
		return in
	}
	if v, ok := m["filePath"]; ok {
		m["file_path"] = v
	}
	b, _ := json.Marshal(m)
	return b
}

// ParseRefineResult reads the JSON lines of a refinement: the text parts joined is
// the answer, and the schema's JSON object is pulled out of it (fences tolerated).
func (opencodeHarness) ParseRefineResult(out []byte) (RefineOutput, error) {
	var text strings.Builder
	var res RefineOutput
	seen := false
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var ev ocEvent
		if json.Unmarshal([]byte(line), &ev) != nil || ev.Type == "" {
			continue
		}
		seen = true
		switch ev.Type {
		case "text":
			text.WriteString(ev.Part.Text)
		case "step_finish":
			var raw struct {
				Part struct {
					Cost float64 `json:"cost"`
				} `json:"part"`
			}
			json.Unmarshal([]byte(line), &raw)
			res.CostUSD += raw.Part.Cost
		case "error":
			res.IsError = true
			res.Text = ev.errText()
		}
	}
	if !seen {
		return RefineOutput{}, fmt.Errorf("no opencode events in output")
	}
	if res.IsError {
		return res, nil
	}
	res.Text = strings.TrimSpace(text.String())
	res.Structured = extractJSONObject(res.Text)
	return res, nil
}

// extractJSONObject finds the JSON object in a model's final text: the whole text,
// a ```json fenced block, or the outermost {...}. nil when there is none.
func extractJSONObject(s string) json.RawMessage {
	s = strings.TrimSpace(s)
	try := func(c string) json.RawMessage {
		c = strings.TrimSpace(c)
		var m map[string]any
		if strings.HasPrefix(c, "{") && json.Unmarshal([]byte(c), &m) == nil {
			return json.RawMessage(c)
		}
		return nil
	}
	if r := try(s); r != nil {
		return r
	}
	for rest := s; ; {
		i := strings.Index(rest, "```")
		if i < 0 {
			break
		}
		rest = rest[i+3:]
		j := strings.Index(rest, "```")
		if j < 0 {
			break
		}
		block := rest[:j]
		if nl := strings.IndexByte(block, '\n'); nl >= 0 && !strings.HasPrefix(strings.TrimSpace(block), "{") {
			block = block[nl+1:] // the language tag
		}
		if r := try(block); r != nil {
			return r
		}
		rest = rest[j+3:]
	}
	if i, j := strings.Index(s, "{"), strings.LastIndex(s, "}"); i >= 0 && j > i {
		return try(s[i : j+1])
	}
	return nil
}

// ModelLister is an optional interface: an adapter that can list the models its
// CLI offers (run only for an available CLI, and cached with the availability).
type ModelLister interface {
	ListModels() []string
}

// ListModels parses `opencode models`: one provider/model per line.
func (h opencodeHarness) ListModels() []string {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, h.bin, "models").Output()
	if err != nil {
		return nil
	}
	return parseModelList(string(out))
}

func parseModelList(out string) []string {
	var models []string
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); l != "" && strings.Contains(l, "/") && !strings.ContainsAny(l, " \t") {
			models = append(models, l)
		}
	}
	return models
}
