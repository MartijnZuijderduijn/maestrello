package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"maestrello/internal/model"
	"maestrello/internal/textutil"
)

// claudeHarness drives the Claude Code CLI (`claude -p`).
type claudeHarness struct {
	bin string // path to the claude CLI
}

func (claudeHarness) Name() string { return "claude" }

func (claudeHarness) Caps() Caps {
	return Caps{
		Resume: true, PermissionCallback: true, StructuredOutput: true, PerTurnEvents: true,
		Account: true, Usage: true,
		Efforts:         []string{"low", "medium", "high", "xhigh", "max"},
		PermissionModes: []string{"auto", "acceptEdits", "manual", "dontAsk", "plan", "bypassPermissions"},
	}
}

func (h claudeHarness) ValidateEffort(effort string) error {
	if effort == "" {
		return nil
	}
	return oneOf("effort", effort, h.Caps().Efforts)
}

func (h claudeHarness) ValidatePermissionMode(mode string) error {
	return oneOf("permission mode", mode, h.Caps().PermissionModes)
}

// claudeEnv is the environment for child claude processes.
func ClaudeEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "CLAUDECODE=") || strings.HasPrefix(kv, "CLAUDE_CODE_ENTRYPOINT=") {
			continue
		}
		env = append(env, kv)
	}
	// ask_user and permission calls block until I answer.
	return append(env, "MCP_TOOL_TIMEOUT=86400000")
}

func (h claudeHarness) SessionCommand(spec SessionSpec) (*exec.Cmd, error) {
	card := spec.Card
	mcpCfg, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{
		"maestrello": map[string]any{"type": "http", "url": spec.MCPURL},
	}})
	if err := os.WriteFile(spec.MCPConfigPath, mcpCfg, 0o600); err != nil {
		return nil, err
	}

	args := []string{"-p", "--output-format", "stream-json", "--verbose",
		"--model", card.Model, "--permission-mode", card.PermissionMode}
	if card.Effort != "" {
		args = append(args, "--effort", card.Effort)
	}
	if spec.Resume {
		args = append(args, "--resume", card.SessionID)
	} else {
		args = append(args, "--session-id", card.SessionID)
	}
	if spec.Strict {
		args = append(args, "--strict-mcp-config")
	}
	// Which tools a session has depends on the card's kind. Code cards get Claude's full
	// toolset, as ever; research and plan cards only read; chat has no built-in tools.
	// The card's toolbox tools are added to --tools (not for code, which has everything) and to
	// --allowedTools, so they run without a permission prompt. With none, nothing changes.
	extra, _ := card.NativeTools("claude")
	withExtra := func(base ...string) string { return strings.Join(append(base, extra...), ",") }
	allowed := "mcp__maestrello__report_done,mcp__maestrello__report_failed,mcp__maestrello__ask_user,mcp__maestrello__add_repo"
	addDir := true
	switch card.EffectiveKind() {
	case model.KindResearch:
		args = append(args, "--tools", withExtra("Read", "Grep", "Glob"))
		allowed = "mcp__maestrello__report_done,mcp__maestrello__report_failed,mcp__maestrello__ask_user"
	case model.KindPlan:
		args = append(args, "--tools", withExtra("Read", "Grep", "Glob"))
		allowed = "mcp__maestrello__report_plan,mcp__maestrello__report_failed,mcp__maestrello__ask_user"
	case model.KindChat:
		args = append(args, "--tools", withExtra())
		allowed = "mcp__maestrello__report_failed,mcp__maestrello__ask_user"
		addDir = false
	}
	if len(extra) > 0 {
		allowed += "," + strings.Join(extra, ",")
	}
	args = append(args,
		"--mcp-config", spec.MCPConfigPath,
		"--permission-prompt-tool", "mcp__maestrello__permission",
		"--allowedTools", allowed,
		"--disallowedTools", "AskUserQuestion",
	)
	if addDir {
		args = append(args, "--add-dir", spec.Root)
		// Spike finding (claude --help): --add-dir is variadic (<directories...>), so I repeat the
		// flag per dir, which cannot swallow later arguments. There is no flag that grants read of
		// a single file outside the working dirs without prompting. So a context file's parent dir
		// is added, and the prompt tells the agent to read only the listed files (contextNote).
		for _, d := range ContextDirs(spec.Context, spec.Root) {
			args = append(args, "--add-dir", d)
		}
	}
	args = append(args, "--append-system-prompt", spec.Instructions)

	cmd := exec.Command(h.bin, args...)
	cmd.Dir = spec.Cwd
	cmd.Env = ClaudeEnv()
	cmd.Stdin = strings.NewReader(spec.Prompt)
	return cmd, nil
}

func (h claudeHarness) RefineCommand(ctx context.Context, spec RefineSpec) (*exec.Cmd, error) {
	args := []string{"-p", "--output-format", "json",
		"--model", spec.Model, "--effort", spec.Effort,
		"--permission-mode", "plan", "--tools", spec.Tools,
		"--strict-mcp-config", "--json-schema", spec.Schema}
	for _, d := range spec.ExtraDirs {
		args = append(args, "--add-dir", d)
	}
	if spec.MaxTurns > 0 {
		args = append(args, "--max-turns", strconv.Itoa(spec.MaxTurns))
	}
	cmd := exec.CommandContext(ctx, h.bin, args...)
	cmd.Dir = spec.Cwd
	cmd.Env = ClaudeEnv()
	cmd.Stdin = strings.NewReader(spec.Prompt)
	return cmd, nil
}

// ParseRefineResult reads `claude -p --output-format json`: the answer is in
// structured_output, or as JSON text in result when the CLI did not fill it.
func (claudeHarness) ParseRefineResult(out []byte) (RefineOutput, error) {
	var res struct {
		IsError          bool            `json:"is_error"`
		Result           string          `json:"result"`
		StructuredOutput json.RawMessage `json:"structured_output"`
		TotalCostUSD     float64         `json:"total_cost_usd"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		return RefineOutput{}, err
	}
	raw := res.StructuredOutput
	if len(raw) == 0 {
		raw = json.RawMessage(res.Result)
	}
	return RefineOutput{Structured: raw, Text: res.Result, CostUSD: res.TotalCostUSD, IsError: res.IsError}, nil
}

var rateLimitRe = regexp.MustCompile(`(?i)usage limit|rate limit|limit reached|too many requests|\b429\b`)

// streamEvent is the part of a `--output-format stream-json` line I read.
type streamEvent struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
	Message struct {
		ID      string `json:"id"`
		Content []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
	} `json:"message"`
	IsError        bool    `json:"is_error"`
	Result         string  `json:"result"`
	TotalCostUSD   float64 `json:"total_cost_usd"`
	APIErrorStatus any     `json:"api_error_status"`
}

func (claudeHarness) ParseEvent(line []byte) (HarnessEvent, error) {
	var ev streamEvent
	if err := json.Unmarshal(line, &ev); err != nil {
		return HarnessEvent{}, err
	}
	out := HarnessEvent{Kind: ev.Type}
	switch {
	case ev.Type == "assistant":
		out.Assistant = true
		out.TurnID = ev.Message.ID
		for _, c := range ev.Message.Content {
			switch c.Type {
			case "tool_use":
				out.Activity = toolActivity(c.Name, c.Input)
			case "text":
				if t := strings.TrimSpace(c.Text); t != "" {
					out.Text = t
					out.Activity = textutil.FirstLine(t, 120)
				}
			}
		}
	case ev.Type == "result":
		out.Result = true
		out.CostUSD = ev.TotalCostUSD
		if ev.IsError {
			out.IsError = true
			out.ResultError = ev.Result
			if rateLimitRe.MatchString(ev.Result) || fmt.Sprint(ev.APIErrorStatus) == "429" {
				out.RateLimited = true
				out.RateLimitMsg = ev.Result
			}
		}
	case strings.Contains(ev.Type, "rate_limit") && rateLimitRejected(line):
		out.RateLimited = true
		out.RateLimitMsg = string(line)
	}
	return out, nil
}

// rateLimitRejected reports whether a rate_limit_event says the request was
// refused. Other fields (such as overageStatus) can say "rejected" while the
// request itself was allowed.
func rateLimitRejected(line []byte) bool {
	var ev struct {
		RateLimitInfo struct {
			Status string `json:"status"`
		} `json:"rate_limit_info"`
	}
	return json.Unmarshal(line, &ev) == nil && ev.RateLimitInfo.Status == "rejected"
}

// toolActivity is a short "Tool: what it does" line for the card's activity.
func toolActivity(name string, input json.RawMessage) string {
	var m map[string]any
	json.Unmarshal(input, &m)
	for _, k := range []string{"command", "file_path", "pattern", "path", "description", "question", "summary"} {
		if v, ok := m[k].(string); ok && v != "" {
			return name + ": " + textutil.FirstLine(v, 100)
		}
	}
	return name
}

// Account runs `claude auth status --json` and parses it. A logged-out CLI may
// exit non-zero but still print its JSON status, which counts as an answer.
func (h claudeHarness) Account(ctx context.Context) (*Account, error) {
	bin := h.bin
	if bin == "" {
		bin = "claude"
	}
	cmd := exec.CommandContext(ctx, bin, "auth", "status", "--json")
	cmd.Env = ClaudeEnv()
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	runErr := cmd.Run()
	if acct, ok := parseClaudeAccount(out.Bytes()); ok {
		return acct, nil
	}
	if ctx.Err() != nil {
		return nil, errors.New("claude auth status timed out")
	}
	if runErr != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = strings.TrimSpace(out.String())
		}
		if msg != "" {
			return nil, fmt.Errorf("claude auth status: %v: %s", runErr, textutil.Tail(msg, 300))
		}
		return nil, fmt.Errorf("claude auth status: %v", runErr)
	}
	return nil, errors.New("claude auth status: unrecognised output")
}

// parseClaudeAccount reads `claude auth status` JSON leniently: unknown fields are
// ignored, and a missing or mistyped field just stays empty.
func parseClaudeAccount(data []byte) (*Account, bool) {
	var m map[string]any
	if json.Unmarshal(bytes.TrimSpace(data), &m) != nil || m == nil {
		return nil, false
	}
	str := func(k string) string { s, _ := m[k].(string); return s }
	logged, hasLogged := m["loggedIn"].(bool)
	acct := &Account{
		LoggedIn: logged, Email: str("email"), OrgName: str("orgName"), AuthMethod: str("authMethod"),
		APIProvider: str("apiProvider"), SubscriptionType: str("subscriptionType"),
	}
	if acct.SubscriptionType == "" {
		acct.SubscriptionType = str("planTier")
	}
	if !hasLogged && acct.Email == "" && acct.AuthMethod == "" {
		return nil, false // some other JSON, not an auth status
	}
	if !hasLogged {
		acct.LoggedIn = true
	}
	if !acct.LoggedIn {
		return &Account{LoggedIn: false}, true
	}
	return acct, true
}

// ParseUsage reads a rate_limit_event's rate_limit_info: the per-window detail in
// unifiedWindows when the CLI reports it, then the top-level info, which describes
// the window named by rateLimitType (later entries win when the board merges).
func (claudeHarness) ParseUsage(line []byte) []UsageLimit {
	if !bytes.Contains(line, []byte("rate_limit_info")) {
		return nil // cheap check first: almost every line carries no usage
	}
	var ev struct {
		Info map[string]any `json:"rate_limit_info"`
	}
	if json.Unmarshal(line, &ev) != nil || ev.Info == nil {
		return nil
	}
	info := ev.Info
	var out []UsageLimit
	if wins, ok := info["unifiedWindows"].(map[string]any); ok {
		types := make([]string, 0, len(wins))
		for typ := range wins {
			types = append(types, typ)
		}
		sort.Strings(types)
		for _, typ := range types {
			wm, ok := wins[typ].(map[string]any)
			if !ok || typ == "" {
				continue
			}
			l := UsageLimit{Type: typ, ResetsAt: unixSecs(wm["resetsAt"])}
			l.Status, _ = wm["status"].(string)
			if f, ok := wm["utilization"].(float64); ok {
				l.Utilization = &f
			}
			if f, ok := wm["surpassedThreshold"].(float64); ok {
				l.SurpassedThreshold = &f
			}
			out = append(out, l)
		}
	}
	typ, _ := info["rateLimitType"].(string)
	if typ == "" {
		typ = "unknown"
	}
	status, _ := info["status"].(string)
	if status != "" || typ != "unknown" {
		l := UsageLimit{Type: typ, Status: status, ResetsAt: unixSecs(info["resetsAt"]), OverageResetsAt: unixSecs(info["overageResetsAt"])}
		if f, ok := info["utilization"].(float64); ok {
			l.Utilization = &f
		}
		if f, ok := info["surpassedThreshold"].(float64); ok {
			l.SurpassedThreshold = &f
		}
		l.OverageStatus, _ = info["overageStatus"].(string)
		if b, ok := info["isUsingOverage"].(bool); ok {
			l.IsUsingOverage = &b
		}
		out = append(out, l)
	}
	return out
}

// unixSecs reads a reset time; milliseconds are converted to seconds.
func unixSecs(v any) int64 {
	f, ok := v.(float64)
	if !ok || f <= 0 {
		return 0
	}
	if f > 1e12 {
		f /= 1000
	}
	return int64(f)
}
