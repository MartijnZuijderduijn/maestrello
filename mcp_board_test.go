package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func boardRPC(t *testing.T, app *App, method string, params any) map[string]any {
	t.Helper()
	body := map[string]any{"jsonrpc": "2.0", "id": 1, "method": method}
	if params != nil {
		body["params"] = params
	}
	data, _ := json.Marshal(body)
	resp, err := http.Post(app.baseURL+"/mcp", "application/json", bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	return out
}

func boardCall(t *testing.T, app *App, tool string, args map[string]any) (string, bool) {
	t.Helper()
	return toolText(t, boardRPC(t, app, "tools/call", map[string]any{"name": tool, "arguments": args}))
}

func toolNames(out map[string]any) []string {
	var names []string
	for _, tl := range out["result"].(map[string]any)["tools"].([]any) {
		names = append(names, tl.(map[string]any)["name"].(string))
	}
	return names
}

func TestBoardMCPProtocol(t *testing.T) {
	app := newTestApp(t, testDir(t))
	c := newCard(t, app, app.dataDir, "")

	out := boardRPC(t, app, "initialize", map[string]any{"protocolVersion": "2025-03-26"})
	res := out["result"].(map[string]any)
	if res["protocolVersion"] != "2025-03-26" || res["serverInfo"].(map[string]any)["name"] != "maestrello-board" {
		t.Errorf("initialize = %v", res)
	}

	names := toolNames(boardRPC(t, app, "tools/list", nil))
	want := []string{"list_projects", "list_cards", "get_card", "create_card", "update_card", "move_card", "answer_input", "approve_card", "discard_card", "send_back_card"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("board tools = %v", names)
	}
	_, perCard := rpc(t, app, c.ID, "tools/list", nil)
	for _, n := range toolNames(perCard) {
		if contains(want, n) {
			t.Errorf("board tool %s on the per-card endpoint", n)
		}
	}

	resp, _ := http.Post(app.baseURL+"/mcp", "application/json", strings.NewReader(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))
	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("notification status = %d", resp.StatusCode)
	}
	resp, _ = http.Get(app.baseURL + "/mcp")
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET status = %d", resp.StatusCode)
	}
}

func TestBoardMCPCreateAndList(t *testing.T) {
	app := newTestApp(t, testDir(t))
	root := app.dataDir

	text, isErr := boardCall(t, app, "create_card", map[string]any{"title": "From MCP", "description": "hi", "root_folder": root, "max_turns": 7})
	if isErr {
		t.Fatal(text)
	}
	var c Card
	json.Unmarshal([]byte(text), &c)
	if c.Status != StatusTodo || c.Title != "From MCP" || c.MaxTurns != 7 {
		t.Errorf("created %+v", c)
	}
	resp, _ := http.Get(app.baseURL + "/api/cards")
	var all []Card
	json.NewDecoder(resp.Body).Decode(&all)
	if len(all) != 1 || all[0].ID != c.ID {
		t.Errorf("GET /api/cards = %v", all)
	}

	for name, args := range map[string]map[string]any{
		"no title":       {"root_folder": root},
		"outside folder": {"title": "x", "root_folder": "/tmp"},
	} {
		if text, isErr := boardCall(t, app, "create_card", args); !isErr {
			t.Errorf("%s: want error, got %s", name, text)
		}
	}
	if cards, _ := app.store.ListCards(); len(cards) != 1 {
		t.Errorf("%d cards after refused creates", len(cards))
	}

	other := newCard(t, app, root, "")
	app.setStatus(other.ID, StatusFailed)
	text, isErr = boardCall(t, app, "list_cards", map[string]any{"status": "failed"})
	var list []map[string]any
	json.Unmarshal([]byte(text), &list)
	if isErr || len(list) != 1 || list[0]["id"].(float64) != float64(other.ID) || list[0]["refined_prompt"] != nil {
		t.Errorf("filtered list = %s", text)
	}
	text, _ = boardCall(t, app, "list_cards", nil)
	list = nil
	json.Unmarshal([]byte(text), &list)
	if len(list) != 2 {
		t.Errorf("full list = %s", text)
	}
	if _, isErr := boardCall(t, app, "list_cards", map[string]any{"status": "nope"}); !isErr {
		t.Error("unknown status should error")
	}

	text, isErr = boardCall(t, app, "get_card", map[string]any{"id": c.ID})
	var detail map[string]any
	json.Unmarshal([]byte(text), &detail)
	if isErr || detail["card"] == nil || detail["live"] != false {
		t.Errorf("get_card = %s", text)
	}
}

func TestBoardMCPUpdate(t *testing.T) {
	app := newTestApp(t, testDir(t))
	c := newCard(t, app, app.dataDir, "")
	if text, isErr := boardCall(t, app, "update_card", map[string]any{"id": c.ID, "title": "Renamed", "refined_prompt": "do it"}); isErr {
		t.Fatal(text)
	}
	got, _ := app.store.GetCard(c.ID)
	if got.Title != "Renamed" || got.RefinedPrompt != "do it" {
		t.Errorf("after update: %+v", got)
	}
	app.setStatus(c.ID, StatusReview)
	if text, isErr := boardCall(t, app, "update_card", map[string]any{"id": c.ID, "title": "Nope"}); !isErr || !strings.Contains(text, "only todo and ready") {
		t.Errorf("edit in review: %v %s", isErr, text)
	}
}

func TestBoardMCPMove(t *testing.T) {
	app := newTestApp(t, testDir(t))
	a, b := newCard(t, app, app.dataDir, ""), newCard(t, app, app.dataDir, "")

	for _, to := range []string{StatusFailed, StatusReview} {
		if text, isErr := boardCall(t, app, "move_card", map[string]any{"id": a.ID, "status": to}); isErr {
			t.Fatal(text)
		}
		if got, _ := app.store.GetCard(a.ID); got.Status != to {
			t.Errorf("status = %s, want %s", got.Status, to)
		}
	}
	// A refined prompt is optional: the card text stands in for it.
	text, isErr := boardCall(t, app, "move_card", map[string]any{"id": b.ID, "status": "ready"})
	if isErr {
		t.Errorf("ready without refined prompt: %s", text)
	}
	if _, isErr := boardCall(t, app, "move_card", map[string]any{"id": b.ID, "status": "nowhere"}); !isErr {
		t.Error("unknown column should error")
	}
	if text, isErr := boardCall(t, app, "move_card", map[string]any{"id": 999, "status": "failed"}); !isErr {
		t.Errorf("missing card: %s", text)
	}

	// Bulk: one failure does not stop the rest, and the note names the origin.
	text, isErr = boardCall(t, app, "move_card", map[string]any{"ids": []int64{a.ID, 999, b.ID}, "status": "failed"})
	var res []map[string]any
	json.Unmarshal([]byte(text), &res)
	if !isErr || len(res) != 3 || res[0]["ok"] != true || res[1]["ok"] == true || res[2]["ok"] != true {
		t.Errorf("bulk = %v %s", isErr, text)
	}
	evs, _ := app.store.Events(b.ID, 20)
	found := false
	for _, e := range evs {
		found = found || strings.Contains(string(e.Data), "moved to failed by you via MCP")
	}
	if !found {
		t.Errorf("no MCP attribution in notes: %v", evs)
	}
}

func TestBoardMCPReviewTools(t *testing.T) {
	app := newTestApp(t, testDir(t))
	c := newCard(t, app, app.dataDir, "")
	app.update(c.ID, func(c *Card) { c.Status = StatusReview; c.RefinedPrompt = "p" })
	if text, isErr := boardCall(t, app, "answer_input", map[string]any{"id": c.ID, "text": "x"}); !isErr || !strings.Contains(text, "no open input") {
		t.Errorf("answer without input: %s", text)
	}
	if _, isErr := boardCall(t, app, "send_back_card", map[string]any{"id": c.ID}); !isErr {
		t.Error("send back without feedback should error")
	}
	if text, isErr := boardCall(t, app, "approve_card", map[string]any{"id": c.ID, "actions": map[string]string{"1": "local"}}); isErr {
		t.Fatal(text)
	}
	if got, _ := app.store.GetCard(c.ID); got.Status != StatusDone {
		t.Errorf("after approve: %s", got.Status)
	}
	if _, isErr := boardCall(t, app, "approve_card", map[string]any{"id": c.ID}); !isErr {
		t.Error("approving a done card should error")
	}
}

func TestBridge(t *testing.T) {
	app := newTestApp(t, testDir(t))
	in := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"create_card","arguments":{"title":"Bridged","root_folder":%q}}}`, app.dataDir),
	}, "\n") + "\n"
	var out bytes.Buffer
	if err := runBridge(strings.NewReader(in), &out, app.baseURL); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 reply lines (none for the notification), got %q", out.String())
	}
	var init map[string]any
	json.Unmarshal([]byte(lines[0]), &init)
	if init["result"].(map[string]any)["protocolVersion"] != "2025-03-26" {
		t.Errorf("initialize reply: %s", lines[0])
	}
	var call map[string]any
	json.Unmarshal([]byte(lines[1]), &call)
	if text, isErr := toolText(t, call); isErr || !strings.Contains(text, "Bridged") {
		t.Errorf("create_card reply: %s", lines[1])
	}
	if cards, _ := app.store.ListCards(); len(cards) != 1 {
		t.Errorf("%d cards", len(cards))
	}

	// Nothing listening: requests get a JSON-RPC error, notifications stay silent.
	out.Reset()
	dead := `{"jsonrpc":"2.0","id":7,"method":"tools/list"}` + "\n" + `{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n"
	runBridge(strings.NewReader(dead), &out, "http://127.0.0.1:1")
	var e map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &e); err != nil {
		t.Fatalf("not one JSON line: %q", out.String())
	}
	if e["id"].(float64) != 7 || !strings.Contains(e["error"].(map[string]any)["message"].(string), "maestrello start") {
		t.Errorf("unreachable reply: %v", e)
	}
}
