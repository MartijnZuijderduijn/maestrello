package harness

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"maestrello/internal/model"
	"maestrello/internal/textutil"
)

// ompHarness drives the omp CLI (Oh My Pi, a fork of the pi coding agent) in its
// non-interactive JSON mode: `omp -p --mode json`.
//
// Checked against the real binary (omp 18.20.0, `omp --help` and probes with a
// model-less run): `-p/--print` with the prompt piped on stdin, `--mode json`,
// `--model`, `--thinking <level>`, `--tools <names>` (built-in tool allowlist),
// `--append-system-prompt <text or file>`, `-r/--resume <id prefix>`,
// `--add-dir`, `--config <overlay.yml>`, `--approval-mode`, `--no-tools`,
// `--no-session`, `omp usage --json`, and that an MCP server listed in
// `<agent dir>/mcp.json` ({"mcpServers": {name: {type: "http", url, timeout}}}) is
// loaded when `PI_CODING_AGENT_DIR` points at that dir (the log shows the connection
// attempt). The stream shape below is from the pi JSON mode (a `session` header line,
// then `message_end`, `turn_end`, `tool_execution_*`, `agent_end` events) and the
// strings of the binary; no model credentials were available, so NO run reached a
// model and the event shapes, the `--tools` handling of MCP tool names and the
// overlay keys were not exercised end to end (see SPEC.md).
//
// What the adapter does:
//   - The prompt goes on stdin. A per-card agent dir `<MCPConfigPath>.omp` mirrors the
//     user's (every entry symlinked: credentials, models, settings, sessions) except
//     `mcp.json`, which holds only the board server. PI_CODING_AGENT_DIR points at it.
//     An overlay `--config` file raises `mcp.startupTimeoutMs` (default 250 ms is
//     too short for tool discovery) and, in Strict mode, switches project MCP config off.
//   - The tool timeout of the board server is 24 h (`timeout` in mcp.json and
//     OMP_MCP_TIMEOUT_MS), as ask_user blocks until the user answers.
//   - MCP tools are named `mcp__<server>_<tool>`, so the board's are
//     `mcp__maestrello_<tool>`; the instructions are rewritten to match.
//   - `--tools` lists the built-ins and board tools of the card kind, as in mvTools.
//   - The session id comes from the `session` header line; Resume is true and a
//     later run is `-r <id>`.
//   - Cost is read from each `turn_end`.
//
// Limits: Account is read from `omp usage --json` (who is logged in, no keys);
// usage limits are only reachable through that command, not in the stream, so
// Caps.Usage is false. No refinement turn cap exists.
type ompHarness struct {
	bin string // path to the omp CLI
}

func newOmpHarness(bin string) ompHarness {
	if bin == "" {
		bin = "omp"
	}
	return ompHarness{bin: bin}
}

func (ompHarness) Name() string { return "omp" }

func (ompHarness) Caps() Caps {
	return Caps{
		Resume: true, PermissionCallback: false, StructuredOutput: false, PerTurnEvents: true,
		Account:         true, // `omp usage --json` lists the logged-in accounts; usage limits are not in the stream, so Usage stays false
		Efforts:         []string{"minimal", "low", "medium", "high", "xhigh", "max"},
		PermissionModes: []string{"auto", "plan"},
	}
}

func (h ompHarness) ValidateEffort(effort string) error {
	if effort == "" {
		return nil
	}
	return oneOf("effort", effort, h.Caps().Efforts)
}

func (h ompHarness) ValidatePermissionMode(mode string) error {
	return oneOf("permission mode", mode, h.Caps().PermissionModes)
}

// ResumableSession: omp session ids are opaque tokens; anything that is not empty,
// has no whitespace and cannot be read as a flag will do for `-r`.
func (ompHarness) ResumableSession(id string) bool {
	return id != "" && !strings.HasPrefix(id, "-") && !strings.ContainsAny(id, " \t\r\n")
}

// omp's built-in tools by what they do, and the board tools as omp names them.
var (
	ompReadTools  = []string{"read", "grep", "glob"}
	ompWriteTools = []string{"edit", "write", "bash"}
)

func ompBoard(names ...string) []string {
	var out []string
	for _, n := range names {
		out = append(out, "mcp__maestrello_"+n)
	}
	return out
}

// ompTools is the tool allowlist of a card kind, like mvTools: read-only kinds and
// plan mode never get a write tool; chat has no built-ins.
func ompTools(card *model.Card) []string {
	var t []string
	switch card.EffectiveKind() {
	case model.KindResearch:
		t = append(append(t, ompReadTools...), ompBoard("report_done", "report_failed", "ask_user")...)
	case model.KindPlan:
		t = append(append(t, ompReadTools...), ompBoard("report_plan", "report_failed", "ask_user")...)
	case model.KindChat:
		t = ompBoard("report_failed", "ask_user")
	default:
		t = append(append(t, ompReadTools...), ompBoard("report_done", "report_failed", "ask_user", "add_repo")...)
		if card.PermissionMode != "plan" {
			t = append(append([]string{}, ompWriteTools...), t...)
		}
	}
	return t
}

// ompUserDir is the user's omp agent dir: $PI_CODING_AGENT_DIR or ~/.omp/agent.
func ompUserDir() string {
	if d := os.Getenv("PI_CODING_AGENT_DIR"); d != "" {
		return d
	}
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".omp", "agent")
	}
	return ""
}

// ompHome writes a per-card agent dir at dir: the user's entries linked in (so
// credentials, settings and sessions are shared), plus an mcp.json with only the
// board's server (when mcpURL is set).
func ompHome(dir, mcpURL string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if user := ompUserDir(); user != "" && user != dir {
		entries, _ := os.ReadDir(user)
		for _, e := range entries {
			if e.Name() == "mcp.json" {
				continue
			}
			link := filepath.Join(dir, e.Name())
			os.Remove(link)
			if err := os.Symlink(filepath.Join(user, e.Name()), link); err != nil {
				return err
			}
		}
	}
	cfg := map[string]any{"mcpServers": map[string]any{}}
	if mcpURL != "" {
		// ask_user blocks until the user answers: long tool timeout (ms).
		cfg["mcpServers"] = map[string]any{"maestrello": map[string]any{"type": "http", "url": mcpURL, "timeout": 86400000}}
	}
	data, _ := json.Marshal(cfg)
	os.Remove(filepath.Join(dir, "mcp.json"))
	return os.WriteFile(filepath.Join(dir, "mcp.json"), data, 0o600)
}

// ompOverlay is the `--config` overlay: a startup window long enough for the MCP
// tools to be there when the first turn starts, and no project MCP files in Strict mode.
func ompOverlay(strict bool) string {
	s := "mcp:\n  startupTimeoutMs: 30000\n"
	if strict {
		s += "  enableProjectConfig: false\n"
	}
	return s
}

func ompEnv(home string) []string {
	return append(os.Environ(), "PI_CODING_AGENT_DIR="+home, "OMP_MCP_TIMEOUT_MS=86400000")
}

func (h ompHarness) SessionCommand(spec SessionSpec) (*exec.Cmd, error) {
	card := spec.Card
	tools := ompTools(card)
	enabled, _ := card.NativeTools("omp") // none today; a tool omp lacks is skipped by the runner's note
	tools = append(tools, enabled...)
	home := spec.MCPConfigPath + ".omp"
	if err := ompHome(home, spec.MCPURL); err != nil {
		return nil, err
	}
	overlay := spec.MCPConfigPath + ".config.yml"
	if err := os.WriteFile(overlay, []byte(ompOverlay(spec.Strict)), 0o600); err != nil {
		return nil, err
	}
	instrPath := spec.MCPConfigPath + ".instructions.md"
	text := strings.ReplaceAll(spec.Instructions, "mcp__maestrello__", "mcp__maestrello_")
	if err := os.WriteFile(instrPath, []byte(text), 0o600); err != nil {
		return nil, err
	}
	// The MCP config also sits at MCPConfigPath, so the board's cleanup finds a file there.
	data, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{"maestrello": map[string]any{"type": "http", "url": spec.MCPURL}}, "agent_dir": home})
	if err := os.WriteFile(spec.MCPConfigPath, data, 0o600); err != nil {
		return nil, err
	}
	args := []string{"-p", "--mode", "json", "--approval-mode", "yolo", "--config", overlay,
		"--append-system-prompt", instrPath, "--tools", strings.Join(tools, ",")}
	if card.Model != "" {
		args = append(args, "--model", card.Model)
	}
	if card.Effort != "" {
		args = append(args, "--thinking", card.Effort)
	}
	if spec.Resume && card.SessionID != "" {
		args = append(args, "--resume", card.SessionID)
	}
	if spec.Root != "" {
		args = append(args, "--add-dir", spec.Root)
		for _, d := range ContextDirs(spec.Context, spec.Root) {
			args = append(args, "--add-dir", d)
		}
	}
	cmd := exec.Command(h.bin, args...)
	cmd.Dir = spec.Cwd
	cmd.Env = ompEnv(home)
	cmd.Stdin = strings.NewReader(spec.Prompt)
	return cmd, nil
}

// No turn cap: omp has no such flag (spec.MaxTurns is ignored). The refiner runs in
// plain text mode without a session, with only the read-only tools asked for.
func (h ompHarness) RefineCommand(ctx context.Context, spec RefineSpec) (*exec.Cmd, error) {
	var tools []string
	for _, t := range strings.Split(spec.Tools, ",") {
		switch n := strings.ToLower(strings.TrimSpace(t)); n {
		case "read", "grep", "glob":
			tools = append(tools, n)
		}
	}
	args := []string{"-p", "--no-session", "--approval-mode", "yolo"}
	if len(tools) == 0 {
		args = append(args, "--no-tools")
	} else {
		args = append(args, "--tools", strings.Join(tools, ","))
	}
	if spec.Model != "" {
		args = append(args, "--model", spec.Model)
	}
	if spec.Effort != "" {
		args = append(args, "--thinking", spec.Effort)
	}
	for _, d := range spec.ExtraDirs {
		args = append(args, "--add-dir", d)
	}
	cmd := exec.CommandContext(ctx, h.bin, args...)
	cmd.Dir = spec.Cwd
	cmd.Cancel = func() error { return cmd.Process.Kill() }
	cmd.WaitDelay = 0
	// No structured output: ask for the JSON in the final text.
	cmd.Stdin = strings.NewReader(spec.Prompt + "\n\nAnswer with ONLY one JSON object that matches this JSON schema, and nothing else:\n" + spec.Schema + "\n")
	return cmd, nil
}

// ompEvent is the part of a JSON-mode line I read (pi's event shapes).
type ompEvent struct {
	Type    string `json:"type"`
	ID      string `json:"id"` // session header
	Message struct {
		Role         string          `json:"role"`
		Content      json.RawMessage `json:"content"`
		ResponseID   string          `json:"responseId"`
		Timestamp    json.RawMessage `json:"timestamp"`
		StopReason   string          `json:"stopReason"`
		ErrorMessage string          `json:"errorMessage"`
		Usage        struct {
			Cost struct {
				Total float64 `json:"total"`
			} `json:"cost"`
		} `json:"usage"`
	} `json:"message"`
}

// parts reads the content blocks: the text joined, and the first tool call.
func (e ompEvent) parts() (text, tool string, input json.RawMessage) {
	var s string
	if json.Unmarshal(e.Message.Content, &s) == nil {
		return strings.TrimSpace(s), "", nil
	}
	var blocks []struct {
		Type      string          `json:"type"`
		Text      string          `json:"text"`
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	json.Unmarshal(e.Message.Content, &blocks)
	var b strings.Builder
	for _, bl := range blocks {
		switch bl.Type {
		case "text":
			b.WriteString(bl.Text)
		case "toolCall":
			if tool == "" {
				tool, input = bl.Name, bl.Arguments
			}
		}
	}
	return strings.TrimSpace(b.String()), tool, input
}

func (h ompHarness) ParseEvent(line []byte) (HarnessEvent, error) {
	var ev ompEvent
	if err := json.Unmarshal(line, &ev); err != nil {
		return HarnessEvent{}, err
	}
	out := HarnessEvent{Kind: ev.Type}
	if ev.Type == "" {
		out.Kind = "event"
	}
	switch ev.Type {
	case "session":
		out.SessionID = ev.ID
	case "message_end":
		if ev.Message.Role != "assistant" {
			return out, nil
		}
		out.Assistant = true
		out.TurnID = ev.Message.ResponseID
		if out.TurnID == "" { // each assistant message is one turn
			sum := sha1.Sum(line)
			out.TurnID = hex.EncodeToString(sum[:8])
		}
		text, tool, input := ev.parts()
		if text != "" {
			out.Text, out.Activity = text, textutil.FirstLine(text, 120)
		}
		if tool != "" {
			out.Activity = toolActivity(tool, input)
		}
	case "turn_end":
		out.Result, out.CostUSD = true, ev.Message.Usage.Cost.Total
		if ev.Message.StopReason == "error" {
			msg := ev.Message.ErrorMessage
			if msg == "" {
				msg = "omp reported an error"
			}
			out.IsError, out.ResultError = true, msg
			if rateLimitRe.MatchString(msg) {
				out.RateLimited, out.RateLimitMsg = true, msg
			}
		}
	}
	return out, nil
}

// ParseUsage: usage limits come only from `omp usage`, not from the stream, so
// Caps.Usage is false.
func (ompHarness) ParseUsage([]byte) []UsageLimit { return nil }

// ParseRefineResult: the run used plain print mode, so stdout is the final answer;
// the schema's JSON object is pulled out of it (fences tolerated).
func (ompHarness) ParseRefineResult(out []byte) (RefineOutput, error) {
	text := strings.TrimSpace(string(out))
	if text == "" {
		return RefineOutput{}, fmt.Errorf("no omp output")
	}
	return RefineOutput{Text: text, Structured: extractJSONObject(text)}, nil
}

// ompKeyEnv are the environment variables of the main providers that omp reads an
// API key from. Only whether one is set is looked at, never its value.
var ompKeyEnv = []string{"ANTHROPIC_API_KEY", "ANTHROPIC_OAUTH_TOKEN", "OPENAI_API_KEY", "GEMINI_API_KEY",
	"OPENROUTER_API_KEY", "MISTRAL_API_KEY", "GROQ_API_KEY", "XAI_API_KEY", "COPILOT_GITHUB_TOKEN"}

// Account runs `omp usage --json`, which lists the stored accounts (OAuth logins
// and API keys) with their provider limits. It is logged in when any account is
// listed, or when a provider key is set in the environment. Tokens are never read.
func (h ompHarness) Account(ctx context.Context) (*Account, error) {
	cmd := exec.CommandContext(ctx, h.bin, "usage", "--json")
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	runErr := cmd.Run()
	if acct, ok := parseOmpAccount(out.Bytes()); ok {
		if !acct.LoggedIn {
			for _, k := range ompKeyEnv {
				if strings.TrimSpace(os.Getenv(k)) != "" {
					acct.LoggedIn, acct.AuthMethod = true, "api key"
					break
				}
			}
		}
		return acct, nil
	}
	if ctx.Err() != nil {
		return nil, errors.New("omp usage timed out")
	}
	msg := strings.TrimSpace(errb.String())
	if runErr != nil {
		if msg != "" {
			return nil, fmt.Errorf("omp usage: %v: %s", runErr, textutil.Tail(msg, 300))
		}
		return nil, fmt.Errorf("omp usage: %v", runErr)
	}
	return nil, errors.New("omp usage: unrecognised output")
}

// parseOmpAccount reads the JSON of `omp usage --json`: reports and
// accountsWithoutUsage list the authenticated accounts. The entries' shape is read
// loosely (provider, email, plan under a few likely keys), as only the empty
// shape could be checked here.
func parseOmpAccount(out []byte) (*Account, bool) {
	var doc struct {
		Reports              []map[string]any `json:"reports"`
		AccountsWithoutUsage []map[string]any `json:"accountsWithoutUsage"`
	}
	i := bytes.IndexByte(out, '{')
	if i < 0 || json.Unmarshal(out[i:], &doc) != nil {
		return nil, false
	}
	entries := append(append([]map[string]any{}, doc.Reports...), doc.AccountsWithoutUsage...)
	if len(entries) == 0 {
		return &Account{LoggedIn: false}, true
	}
	acct := &Account{LoggedIn: true, AuthMethod: "login"}
	str := func(m map[string]any, keys ...string) string {
		for _, k := range keys {
			if v, ok := m[k].(string); ok && v != "" {
				return v
			}
		}
		return ""
	}
	for _, e := range entries {
		if acct.APIProvider == "" {
			acct.APIProvider = str(e, "provider", "providerId")
		}
		if acct.Email == "" {
			acct.Email = str(e, "email", "accountEmail")
			if a, ok := e["account"].(map[string]any); ok && acct.Email == "" {
				acct.Email = str(a, "email")
			}
		}
		if acct.SubscriptionType == "" {
			acct.SubscriptionType = str(e, "plan", "planType", "subscriptionType")
		}
	}
	return acct, true
}

// ListModels parses `omp models --json`: the chat models omp has a catalog entry
// for, as `provider/id` selectors, which is what `--model` takes exactly. It reads
// the catalog only, never credentials. Nil on any failure, so a model can still be typed.
func (h ompHarness) ListModels() []string {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, h.bin, "models", "--json").Output()
	if err != nil {
		return nil
	}
	return parseOmpModels(out)
}

func parseOmpModels(out []byte) []string {
	var doc struct {
		Models []struct {
			Kind     string `json:"kind"`
			Selector string `json:"selector"`
		} `json:"models"`
	}
	i := bytes.IndexByte(out, '{')
	if i < 0 || json.Unmarshal(out[i:], &doc) != nil {
		return nil
	}
	var models []string
	seen := map[string]bool{}
	for _, m := range doc.Models {
		if (m.Kind == "" || m.Kind == "chat") && m.Selector != "" && !strings.ContainsAny(m.Selector, " \t") && !seen[m.Selector] {
			seen[m.Selector] = true
			models = append(models, m.Selector)
		}
	}
	return models
}
