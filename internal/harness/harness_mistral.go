package harness

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"maestrello/internal/model"
	"maestrello/internal/textutil"
)

// mistralHarness drives Mistral Vibe (`vibe`, the Mistral coding-agent CLI) in its
// programmatic mode: `vibe -p --output streaming`.
//
// What this adapter relies on (from docs.mistral.ai/vibe; the CLI was NOT installed
// while building it, so nothing here was run against the real binary, see SPEC.md):
//   - `-p` without text reads the prompt from stdin; `--output streaming` prints one
//     JSON object per message, `--output text` just the final answer.
//   - `--model <alias>` picks the model for one run, `--agent <name>` the agent,
//     `--enabled-tools <name>` (repeatable) restricts the tools to the given names
//     (exact, glob or `re:`), `--trust` trusts the working folder for this run.
//   - VIBE_HOME moves Vibe's home, and with it config.toml, agents/*.toml and .env.
//     The adapter makes a per-card home `<MCPConfigPath>.vibe` holding a config.toml
//     (board MCP server as [[mcp_servers]] with transport "streamable-http", and
//     `permission = "always"` per enabled tool) and agents/maestrello.toml (the board
//     instructions, which Vibe appends after its own system prompt). The user's
//     `.env` (API key) is linked in; their own config.toml, models and providers are
//     not (a TOML file cannot be merged safely), so models are Vibe's built-in
//     aliases/IDs or the MISTRAL_API_KEY-backed defaults.
//   - MCP tools are named `<server>_<tool>`, so the board's tools are
//     `maestrello_<tool>` (as in opencode); the instructions are rewritten to match.
//   - Programmatic runs deny every tool call that needs approval, so the adapter
//     grants exactly the tools of the card kind and never asks (no permission callback).
//
// Limits: the stream has no cost, no result line and no documented session id, so
// the adapter reports no cost and no session id: Resume is false (a later run starts
// fresh with the refined prompt plus the message). There is no per-card toolbox
// support (the docs name no web tools), no refinement turn cap is passed and
// the user's `[[mcp_servers]]` are not loaded (separate home), so Strict needs nothing.
type mistralHarness struct {
	bin string // path to the vibe CLI
}

func newMistralHarness(bin string) mistralHarness {
	if bin == "" {
		bin = "vibe"
	}
	return mistralHarness{bin: bin}
}

func (mistralHarness) Name() string { return "mistral" }

// Account reads what Vibe leaves on disk, since the CLI has no auth-status command
// (checked against vibe 3.1.2: only `mcp`, `config` and `update` subcommands). It is
// logged in when a key is configured (MISTRAL_API_KEY in the environment or in
// `<vibe home>/.env`) or Vibe has cached its account lookup (`whoami_cache.json`),
// which also holds the plan name. The key itself is never kept or returned, and the
// cache has no email, so none is shown.
func (mistralHarness) Account(context.Context) (*Account, error) {
	home := mvUserHome()
	key := strings.TrimSpace(os.Getenv("MISTRAL_API_KEY")) != "" || mvEnvHasKey(filepath.Join(home, ".env"))
	plan, cached := mvWhoamiPlan(filepath.Join(home, "whoami_cache.json"))
	if !key && !cached {
		return &Account{LoggedIn: false}, nil
	}
	acct := &Account{LoggedIn: true, APIProvider: "mistral", SubscriptionType: plan}
	if key {
		acct.AuthMethod = "api key"
	}
	return acct, nil
}

// ParseUsage: the stream has no usage-limit events, so Caps.Usage is false.
func (mistralHarness) ParseUsage([]byte) []UsageLimit { return nil }

// mvUserHome is the user's Vibe home: $VIBE_HOME or ~/.vibe.
func mvUserHome() string {
	if h := os.Getenv("VIBE_HOME"); h != "" {
		return h
	}
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".vibe")
	}
	return ""
}

// mvEnvHasKey reports whether the .env file sets a non-empty MISTRAL_API_KEY.
func mvEnvHasKey(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "export "))
		if v, ok := strings.CutPrefix(line, "MISTRAL_API_KEY="); ok {
			return strings.Trim(strings.TrimSpace(v), `"'`) != ""
		}
	}
	return false
}

// mvWhoamiPlan reads the plan name (e.g. "Free") from Vibe's cached account lookup.
func mvWhoamiPlan(path string) (plan string, ok bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	var m map[string]struct {
		Payload struct {
			PlanName string `json:"plan_name"`
			PlanType string `json:"plan_type"`
		} `json:"payload"`
	}
	if json.Unmarshal(data, &m) != nil || len(m) == 0 {
		return "", false
	}
	for _, e := range m {
		if p := e.Payload.PlanName; p != "" {
			return strings.ToUpper(p[:1]) + strings.ToLower(p[1:]), true
		}
		if p := e.Payload.PlanType; p != "" {
			return p, true
		}
	}
	return "", true
}

func (mistralHarness) Caps() Caps {
	return Caps{
		Resume: false, PermissionCallback: false, StructuredOutput: false, PerTurnEvents: true,
		Account: true, // from the key / whoami cache on disk; no usage events, so Usage stays false
		Efforts:         []string{}, // Vibe has `thinking` per model in config, not per run
		PermissionModes: []string{"auto", "plan"},
	}
}

func (h mistralHarness) ValidateEffort(effort string) error {
	if effort == "" {
		return nil
	}
	return oneOf("effort", effort, h.Caps().Efforts)
}

func (h mistralHarness) ValidatePermissionMode(mode string) error {
	return oneOf("permission mode", mode, h.Caps().PermissionModes)
}

// Vibe's built-in tools by what they do.
var (
	mvReadTools  = []string{"read_file", "grep"}
	mvWriteTools = []string{"write_file", "edit", "search_replace", "bash"}
	mvAllBoard   = []string{"report_done", "report_failed", "ask_user", "report_plan", "add_repo"}
)

func mvBoard(names ...string) []string {
	var out []string
	for _, n := range names {
		out = append(out, "maestrello_"+n)
	}
	return out
}

// mvTools is the tool allowlist of a card kind. Read-only kinds (research, plan,
// chat) and plan mode never get a write tool, so they cannot write whatever the
// permission is.
func mvTools(card *model.Card) []string {
	var t []string
	switch card.EffectiveKind() {
	case model.KindResearch:
		t = append(append(t, mvReadTools...), mvBoard("report_done", "report_failed", "ask_user")...)
	case model.KindPlan:
		t = append(append(t, mvReadTools...), mvBoard("report_plan", "report_failed", "ask_user")...)
	case model.KindChat:
		t = mvBoard("report_failed", "ask_user")
	default:
		t = append(append(t, mvReadTools...), mvBoard("report_done", "report_failed", "ask_user", "add_repo")...)
		if card.PermissionMode != "plan" {
			t = append(append([]string{}, mvWriteTools...), t...)
		}
	}
	return t
}

func tomlStr(s string) string { b, _ := json.Marshal(s); return string(b) } // JSON strings are TOML basic strings

func tomlList(items []string) string {
	q := make([]string, len(items))
	for i, s := range items {
		q[i] = tomlStr(s)
	}
	return "[" + strings.Join(q, ", ") + "]"
}

// mvConfig is the per-run config.toml: top-level keys first, then the tables.
func mvConfig(tools []string, mcpURL string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "default_agent = %s\nenabled_tools = %s\n\n", tomlStr("maestrello"), tomlList(tools))
	if mcpURL != "" {
		// ask_user blocks until the user answers: long tool timeout.
		fmt.Fprintf(&b, "[[mcp_servers]]\nname = %s\ntransport = \"streamable-http\"\nurl = %s\nstartup_timeout_sec = 30\ntool_timeout_sec = 86400\n\n", tomlStr("maestrello"), tomlStr(mcpURL))
	}
	for _, n := range tools {
		fmt.Fprintf(&b, "[tools.%s]\npermission = \"always\"\n\n", n)
	}
	return b.String()
}

func mvAgent(instructions string) string {
	return fmt.Sprintf("agent_type = \"agent\"\ndisplay_name = \"maestrello\"\ndescription = \"Agent run by the maestrello board\"\ninstructions = %s\n", tomlStr(instructions))
}

// mvHome writes a Vibe home at dir and returns it. The user's .env (API key) is linked in.
func mvHome(dir string, tools []string, mcpURL, instructions string) error {
	if err := os.MkdirAll(filepath.Join(dir, "agents"), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(mvConfig(tools, mcpURL)), 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "agents", "maestrello.toml"), []byte(mvAgent(instructions)), 0o600); err != nil {
		return err
	}
	userHome := mvUserHome()
	if userHome != "" && userHome != dir {
		env := filepath.Join(userHome, ".env")
		if _, err := os.Stat(env); err == nil {
			os.Remove(filepath.Join(dir, ".env"))
			return os.Symlink(env, filepath.Join(dir, ".env"))
		}
	}
	return nil
}

func mvToolFlags(tools []string) []string {
	var a []string
	for _, t := range tools {
		a = append(a, "--enabled-tools", t)
	}
	return a
}

func (h mistralHarness) SessionCommand(spec SessionSpec) (*exec.Cmd, error) {
	card := spec.Card
	tools := mvTools(card)
	enabled, _ := card.NativeTools("mistral") // none today; a tool Vibe lacks is skipped by the runner's note
	tools = append(tools, enabled...)
	home := spec.MCPConfigPath + ".vibe"
	text := strings.ReplaceAll(spec.Instructions, "mcp__maestrello__", "maestrello_")
	if err := mvHome(home, tools, spec.MCPURL, text); err != nil {
		return nil, err
	}
	// The MCP config also sits at MCPConfigPath, so the board's cleanup finds a file there.
	data, _ := json.Marshal(map[string]any{"mcp_servers": []map[string]string{{"name": "maestrello", "transport": "streamable-http", "url": spec.MCPURL}}, "vibe_home": home})
	if err := os.WriteFile(spec.MCPConfigPath, data, 0o600); err != nil {
		return nil, err
	}
	args := []string{"-p", "--output", "streaming", "--agent", "maestrello", "--trust"}
	if card.Model != "" {
		args = append(args, "--model", card.Model)
	}
	args = append(args, mvToolFlags(tools)...)
	// No --resume: the stream reports no session id, so Caps.Resume is false.
	cmd := exec.Command(h.bin, args...)
	cmd.Dir = spec.Cwd
	cmd.Env = append(os.Environ(), "VIBE_HOME="+home)
	cmd.Stdin = strings.NewReader(spec.Prompt)
	return cmd, nil
}

// No turn cap and no ExtraDirs flag: the refiner gets read_file and grep only, and
// Vibe's own outside_workdir rule ("ask", denied when nobody can answer) applies.
func (h mistralHarness) RefineCommand(ctx context.Context, spec RefineSpec) (*exec.Cmd, error) {
	var tools []string
	for _, t := range strings.Split(spec.Tools, ",") {
		switch strings.ToLower(strings.TrimSpace(t)) {
		case "read":
			tools = append(tools, "read_file")
		case "grep", "glob":
			if !strings.Contains(strings.Join(tools, ","), "grep") {
				tools = append(tools, "grep")
			}
		}
	}
	home, err := os.MkdirTemp("", "maestrello-vibe-*")
	if err != nil {
		return nil, err
	}
	if err := mvHome(home, tools, "", "You refine a task description. Answer only as asked."); err != nil {
		os.RemoveAll(home)
		return nil, err
	}
	args := []string{"-p", "--output", "text", "--agent", "maestrello", "--trust"}
	if spec.Model != "" {
		args = append(args, "--model", spec.Model)
	}
	if len(tools) == 0 {
		args = append(args, "--enabled-tools", "re:^$") // nothing on
	}
	args = append(args, mvToolFlags(tools)...)
	cmd := exec.CommandContext(ctx, h.bin, args...)
	cmd.Dir = spec.Cwd
	cmd.Env = append(os.Environ(), "VIBE_HOME="+home)
	cmd.Cancel = func() error { return cmd.Process.Kill() }
	cmd.WaitDelay = 0
	// The home is a one-shot temp dir; it is tiny, and the OS clears temp.
	// No structured output: ask for the JSON in the final text.
	cmd.Stdin = strings.NewReader(spec.Prompt + "\n\nAnswer with ONLY one JSON object that matches this JSON schema, and nothing else:\n" + spec.Schema + "\n")
	return cmd, nil
}

// mvMessage is the part of a streamed message I read (Vibe's LLM message shape:
// role, content, tool_calls[].function.{name,arguments}).
type mvMessage struct {
	Role      string          `json:"role"`
	Content   json.RawMessage `json:"content"`
	MessageID string          `json:"message_id"`
	ToolCalls []struct {
		Function struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		} `json:"function"`
	} `json:"tool_calls"`
}

// text returns the content when it is a string or a list of {text} parts.
func (m mvMessage) text() string {
	var s string
	if json.Unmarshal(m.Content, &s) == nil {
		return strings.TrimSpace(s)
	}
	var parts []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(m.Content, &parts) == nil {
		var b strings.Builder
		for _, p := range parts {
			b.WriteString(p.Text)
		}
		return strings.TrimSpace(b.String())
	}
	return ""
}

func (mistralHarness) ParseEvent(line []byte) (HarnessEvent, error) {
	var m mvMessage
	if err := json.Unmarshal(line, &m); err != nil {
		return HarnessEvent{}, err
	}
	out := HarnessEvent{Kind: m.Role}
	if m.Role == "" {
		out.Kind = "event"
	}
	if m.Role != "assistant" {
		return out, nil // user, system and tool messages: logged only
	}
	out.Assistant = true
	out.TurnID = m.MessageID
	if out.TurnID == "" { // each streamed assistant message is one turn
		sum := sha1.Sum(line)
		out.TurnID = hex.EncodeToString(sum[:8])
	}
	if t := m.text(); t != "" {
		out.Text, out.Activity = t, textutil.FirstLine(t, 120)
	}
	if len(m.ToolCalls) > 0 {
		f := m.ToolCalls[0].Function
		out.Activity = toolActivity(f.Name, mvToolInput(f.Arguments))
	}
	return out, nil
}

// mvToolInput: tool arguments arrive as a JSON object or as a JSON string holding one;
// "path" is also offered as file_path, which toolActivity looks for.
func mvToolInput(args json.RawMessage) json.RawMessage {
	var s string
	if json.Unmarshal(args, &s) == nil {
		args = json.RawMessage(s)
	}
	var m map[string]any
	if json.Unmarshal(args, &m) != nil {
		return nil
	}
	if v, ok := m["path"]; ok {
		m["file_path"] = v
	}
	b, _ := json.Marshal(m)
	return b
}

// ParseRefineResult: the run used `--output text`, so stdout is the final answer;
// the schema's JSON object is pulled out of it (fences tolerated).
func (mistralHarness) ParseRefineResult(out []byte) (RefineOutput, error) {
	text := strings.TrimSpace(string(out))
	if text == "" {
		return RefineOutput{}, fmt.Errorf("no vibe output")
	}
	return RefineOutput{Text: text, Structured: extractJSONObject(text)}, nil
}
