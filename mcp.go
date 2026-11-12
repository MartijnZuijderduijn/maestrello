package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// A minimal MCP server (streamable HTTP, JSON responses only), one endpoint per card.
// Agents use it to report back to the board.

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

var mcpTools = []map[string]any{
	tool("report_done", "Report that the task is complete. Moves the card to Review.",
		prop("summary", "string", "What changed, in which repos, and how you verified it")),
	tool("report_failed", "Report that you cannot complete the task. Moves the card to Failed.",
		prop("reason", "string", "Why the task cannot be completed")),
	tool("ask_user", "Ask the user a question and wait for the answer. Use instead of guessing.",
		prop("question", "string", "The question, with enough context to answer it")),
	tool("add_repo", "Add a repo or folder to your workspace. A git repo is checked out as a worktree on the card branch; a folder that is not a git repo is local work you edit in place (\".\" is the root folder itself). Returns where to work.",
		prop("path", "string", "Repo path relative to the root folder")),
	{
		"name":        "permission",
		"description": "Permission prompt handler (used by Claude Code itself).",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
			"tool_name":   map[string]any{"type": "string"},
			"input":       map[string]any{"type": "object"},
			"tool_use_id": map[string]any{"type": "string"},
		}, "required": []string{"tool_name", "input"}},
	},
}

// reportPlanTool is offered to plan cards only.
var reportPlanTool = tool("report_plan", "Report your plan and the tasks it splits into. Moves the card to Review; the user picks which tasks become cards. You never create cards yourself.",
	prop("plan", "string", "The plan: scope, approach, order, risks"),
	[2]any{"tasks", map[string]any{"type": "array", "description": "The tasks, in the order they should be done", "items": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"title":       map[string]any{"type": "string", "description": "Short task title"},
			"description": map[string]any{"type": "string", "description": "Self-contained description of the task: goal, files involved, definition of done"},
			"kind":        map[string]any{"type": "string", "description": "code (default), research or plan"},
			"repos":       map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Repos (relative to the root folder, or \".\") the task works in"},
		},
		"required": []string{"title", "description"},
	}}})

// mcpToolsFor is the tool list a card's agent sees. Every kind has ask_user and
// report_failed. Only code cards report_done to hand in changes and add_repo; research cards
// report_done with a written report; only plan cards report_plan; chat has no report.
func mcpToolsFor(kind string) []map[string]any {
	var out []map[string]any
	for _, t := range mcpTools {
		name := t["name"].(string)
		switch {
		case kind == KindCode, name == "report_failed", name == "ask_user", name == "permission":
			out = append(out, t)
		case kind == KindResearch && name == "report_done":
			out = append(out, t)
		}
	}
	if kind == KindPlan {
		out = append(out, reportPlanTool)
	}
	return out
}

// tool builds a tool schema. Every prop is required unless made with optProp.
func tool(name, desc string, props ...[2]any) map[string]any {
	p := map[string]any{}
	req := []string{}
	for _, kv := range props {
		n := kv[0].(string)
		if strings.HasPrefix(n, "?") {
			p[n[1:]] = kv[1]
			continue
		}
		p[n] = kv[1]
		req = append(req, n)
	}
	return map[string]any{"name": name, "description": desc,
		"inputSchema": map[string]any{"type": "object", "properties": p, "required": req}}
}

func prop(name, typ, desc string) [2]any {
	return [2]any{name, map[string]any{"type": typ, "description": desc}}
}

// optProp is a prop the caller may leave out.
func optProp(name, typ, desc string) [2]any {
	p := prop(name, typ, desc)
	p[0] = "?" + name
	return p
}

func (a *App) handleMCP(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("card"), 10, 64)
	if err != nil {
		http.Error(w, "bad card id", http.StatusBadRequest)
		return
	}
	kind := KindCode
	if c, err := a.store.GetCard(id); err == nil {
		kind = c.kind()
	}
	serveMCP(w, r, "maestrello", mcpToolsFor(kind), func(name string, args json.RawMessage) (string, bool) {
		return a.callTool(r, id, name, args)
	})
}

// serveMCP is the JSON-RPC plumbing both MCP endpoints share; each brings its own
// server name, tool list and dispatcher.
func serveMCP(w http.ResponseWriter, r *http.Request, server string, tools []map[string]any,
	call func(name string, args json.RawMessage) (string, bool)) {
	switch r.Method {
	case http.MethodPost:
	case http.MethodDelete:
		w.WriteHeader(http.StatusOK)
		return
	default:
		// No server-initiated stream.
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req rpcRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeRPC(w, nil, nil, &rpcError{-32700, "parse error"})
		return
	}
	if len(req.ID) == 0 { // notification
		w.WriteHeader(http.StatusAccepted)
		return
	}
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		json.Unmarshal(req.Params, &p)
		if p.ProtocolVersion == "" {
			p.ProtocolVersion = "2025-06-18"
		}
		writeRPC(w, req.ID, map[string]any{
			"protocolVersion": p.ProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": server, "version": "0.1"},
		}, nil)
	case "ping":
		writeRPC(w, req.ID, map[string]any{}, nil)
	case "tools/list":
		writeRPC(w, req.ID, map[string]any{"tools": tools}, nil)
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			writeRPC(w, req.ID, nil, &rpcError{-32602, "invalid params"})
			return
		}
		text, isErr := call(p.Name, p.Arguments)
		writeRPC(w, req.ID, map[string]any{
			"content": []map[string]any{{"type": "text", "text": text}},
			"isError": isErr,
		}, nil)
	default:
		writeRPC(w, req.ID, nil, &rpcError{-32601, "method not found: " + req.Method})
	}
}

func writeRPC(w http.ResponseWriter, id json.RawMessage, result any, rerr *rpcError) {
	resp := map[string]any{"jsonrpc": "2.0", "id": id}
	if rerr != nil {
		resp["error"] = rerr
	} else {
		resp["result"] = result
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (a *App) callTool(r *http.Request, id int64, name string, raw json.RawMessage) (string, bool) {
	var args map[string]any
	json.Unmarshal(raw, &args)
	str := func(k string) string { s, _ := args[k].(string); return s }

	card, err := a.store.GetCard(id)
	if err != nil {
		return err.Error(), true
	}
	if msg := kindRefusal(card.kind(), name); msg != "" {
		return msg, true
	}

	switch name {
	case "report_done":
		a.note(id, "agent reported done")
		a.update(id, func(c *Card) {
			c.Status = StatusReview
			c.Summary = str("summary")
		})
		return "Reported. The card is in review. End your turn now.", false

	case "report_failed":
		a.update(id, func(c *Card) {
			c.Status = StatusFailed
			c.FailReason = str("reason")
		})
		return "Reported. End your turn now.", false

	case "report_plan":
		var p struct {
			Plan  string     `json:"plan"`
			Tasks []PlanTask `json:"tasks"`
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return "report_plan: invalid arguments: " + err.Error(), true
		}
		if strings.TrimSpace(p.Plan) == "" {
			return "report_plan: plan is required", true
		}
		tasks, err := a.checkPlanTasks(card.RootFolder, p.Tasks)
		if err != nil {
			return "report_plan: " + err.Error(), true
		}
		a.note(id, "agent reported a plan with %d tasks", len(tasks))
		a.update(id, func(c *Card) {
			c.Status = StatusReview
			c.Summary = p.Plan
			c.PlanTasks = tasks
		})
		return "Reported. The plan is in review; the user decides which tasks become cards. End your turn now.", false

	case "add_repo":
		repo, err := a.checkoutRepo(card, str("path"))
		var busy *LeaseBusyError
		if errors.As(err, &busy) {
			// Never block here: the holder may need minutes or hours.
			return fmt.Sprintf("add_repo failed: %s is local work that card #%d (%s) is editing right now, and only one card may edit a local folder at a time. "+
				"Carry on with other work and call add_repo again later, or use ask_user or report_failed if you cannot continue without it.",
				busy.Folder, busy.Holder, busy.Title), true
		}
		if err != nil {
			return "add_repo failed: " + err.Error(), true
		}
		if repo.Local() {
			return fmt.Sprintf("%s is local work (not a git repo): edit it in place at %s, and do not run git there.", repo.RepoPath, repo.WorktreePath), false
		}
		return fmt.Sprintf("Checked out %s at %s on branch %s (base %s).", repo.RepoPath, repo.WorktreePath, repo.Branch, repo.BaseRef), false

	case "ask_user":
		reply, ok := a.waitForInput(r, id, "question", map[string]any{"question": str("question")})
		if !ok {
			return "No answer: the session is ending.", true
		}
		return reply.Answer, false

	case "permission":
		payload := map[string]any{"tool_name": args["tool_name"], "input": args["input"]}
		reply, ok := a.waitForInput(r, id, "permission", payload)
		var resp map[string]any
		if ok && reply.Allow {
			resp = map[string]any{"behavior": "allow", "updatedInput": args["input"]}
		} else {
			msg := "Denied by the user."
			if ok && reply.Answer != "" {
				msg += " " + reply.Answer
			}
			resp = map[string]any{"behavior": "deny", "message": msg}
		}
		out, _ := json.Marshal(resp)
		return string(out), false
	}
	return "unknown tool " + name, true
}

// waitForInput puts the card in Needs input and blocks until I answer or the agent disconnects.
func (a *App) waitForInput(r *http.Request, id int64, kind string, payload any) (inputReply, bool) {
	in, err := a.store.CreateInput(id, kind, payload)
	if err != nil {
		return inputReply{}, false
	}
	ch := make(chan inputReply, 1)
	a.mu.Lock()
	a.waiters[in.ID] = ch
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		delete(a.waiters, in.ID)
		a.mu.Unlock()
	}()

	run := a.liveRun(id)
	if run != nil {
		run.setWaiting(true)
		defer run.setWaiting(false)
	}
	a.update(id, func(c *Card) { c.Status = StatusNeedsInput; c.Activity = "waiting for you" })

	select {
	case reply := <-ch:
		a.update(id, func(c *Card) {
			if c.Status == StatusNeedsInput {
				c.Status = StatusRunning
				c.Activity = "continuing"
			}
		})
		return reply, true
	case <-r.Context().Done():
		return inputReply{}, false
	}
}

// kindRefusal is the tool error for a tool a card of this kind may not use, or "" when it may.
func kindRefusal(kind, tool string) string {
	switch {
	case tool == "add_repo" && kind != KindCode:
		return "add_repo is not available on " + kind + " cards: they have no workspace to add repos to."
	case tool == "report_plan" && kind != KindPlan:
		return "report_plan is only for plan cards; this is a " + kind + " card."
	case tool == "report_done" && kind == KindPlan:
		return "plan cards finish with report_plan(plan, tasks), not report_done."
	case tool == "report_done" && kind == KindChat:
		return "chat cards have nothing to report: answer the user and end your turn."
	}
	return ""
}
