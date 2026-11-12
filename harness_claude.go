package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// claudeHarness drives the Claude Code CLI (`claude -p`).
type claudeHarness struct {
	bin string // path to the claude CLI
}

func (claudeHarness) Name() string { return "claude" }

func (claudeHarness) Caps() Caps {
	return Caps{
		Resume: true, PermissionCallback: true, StructuredOutput: true, PerTurnEvents: true,
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
func claudeEnv() []string {
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
	extra, _ := card.nativeTools("claude")
	withExtra := func(base ...string) string { return strings.Join(append(base, extra...), ",") }
	allowed := "mcp__maestrello__report_done,mcp__maestrello__report_failed,mcp__maestrello__ask_user,mcp__maestrello__add_repo"
	addDir := true
	switch card.kind() {
	case KindResearch:
		args = append(args, "--tools", withExtra("Read", "Grep", "Glob"))
		allowed = "mcp__maestrello__report_done,mcp__maestrello__report_failed,mcp__maestrello__ask_user"
	case KindPlan:
		args = append(args, "--tools", withExtra("Read", "Grep", "Glob"))
		allowed = "mcp__maestrello__report_plan,mcp__maestrello__report_failed,mcp__maestrello__ask_user"
	case KindChat:
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
		for _, d := range contextDirs(spec.Context, spec.Root) {
			args = append(args, "--add-dir", d)
		}
	}
	args = append(args, "--append-system-prompt", spec.Instructions)

	cmd := exec.Command(h.bin, args...)
	cmd.Dir = spec.Cwd
	cmd.Env = claudeEnv()
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
	cmd.Env = claudeEnv()
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
	out := HarnessEvent{Kind: ev.Type, UsageInfo: strings.Contains(ev.Type, "rate_limit")}
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
					out.Activity = firstLine(t, 120)
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
	case out.UsageInfo && rateLimitRejected(line):
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
			return name + ": " + firstLine(v, 100)
		}
	}
	return name
}
