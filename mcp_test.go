package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"
)

func rpc(t *testing.T, app *App, cardID int64, method string, params any) (int, map[string]any) {
	t.Helper()
	body := map[string]any{"jsonrpc": "2.0", "id": 1, "method": method}
	if params != nil {
		body["params"] = params
	}
	data, _ := json.Marshal(body)
	resp, err := http.Post(fmt.Sprintf("%s/mcp/%d", app.baseURL, cardID), "application/json", bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func toolText(t *testing.T, out map[string]any) (string, bool) {
	t.Helper()
	res, ok := out["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result: %v", out)
	}
	content := res["content"].([]any)[0].(map[string]any)
	return content["text"].(string), res["isError"].(bool)
}

func TestMCPProtocol(t *testing.T) {
	app := newTestApp(t, testDir(t))
	c := newCard(t, app, app.dataDir, "")

	_, out := rpc(t, app, c.ID, "initialize", map[string]any{"protocolVersion": "2025-03-26"})
	res := out["result"].(map[string]any)
	if res["protocolVersion"] != "2025-03-26" {
		t.Errorf("protocol version not echoed: %v", res)
	}

	_, out = rpc(t, app, c.ID, "tools/list", nil)
	var names []string
	for _, tl := range out["result"].(map[string]any)["tools"].([]any) {
		names = append(names, tl.(map[string]any)["name"].(string))
	}
	for _, want := range []string{"report_done", "report_failed", "ask_user", "add_repo", "permission"} {
		if !contains(names, want) {
			t.Errorf("tool %s missing from %v", want, names)
		}
	}

	_, out = rpc(t, app, c.ID, "no/such", nil)
	if out["error"] == nil {
		t.Error("unknown method did not error")
	}

	// Notifications get 202 and no body.
	resp, _ := http.Post(fmt.Sprintf("%s/mcp/%d", app.baseURL, c.ID), "application/json",
		bytes.NewReader([]byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)))
	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("notification status = %d", resp.StatusCode)
	}
	// No server-initiated stream.
	resp, _ = http.Get(fmt.Sprintf("%s/mcp/%d", app.baseURL, c.ID))
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET status = %d", resp.StatusCode)
	}
}

func TestMCPReportTools(t *testing.T) {
	app := newTestApp(t, testDir(t))
	done := newCard(t, app, app.dataDir, "")
	failed := newCard(t, app, app.dataDir, "")

	rpc(t, app, done.ID, "tools/call", map[string]any{"name": "report_done", "arguments": map[string]any{"summary": "all good"}})
	rpc(t, app, failed.ID, "tools/call", map[string]any{"name": "report_failed", "arguments": map[string]any{"reason": "no way"}})

	c, _ := app.store.GetCard(done.ID)
	if c.Status != StatusReview || c.Summary != "all good" {
		t.Errorf("after report_done: %s %q", c.Status, c.Summary)
	}
	c, _ = app.store.GetCard(failed.ID)
	if c.Status != StatusFailed || c.FailReason != "no way" {
		t.Errorf("after report_failed: %s %q", c.Status, c.FailReason)
	}

	// Each endpoint only touches its own card.
	other, _ := app.store.GetCard(done.ID)
	if other.Status != StatusReview {
		t.Error("report_failed leaked into another card")
	}
}

func TestMCPAskUserBlocksUntilAnswered(t *testing.T) {
	app := newTestApp(t, testDir(t))
	c := newCard(t, app, app.dataDir, "")

	type result struct {
		text  string
		isErr bool
	}
	got := make(chan result, 1)
	go func() {
		_, out := rpc(t, app, c.ID, "tools/call", map[string]any{"name": "ask_user", "arguments": map[string]any{"question": "tabs or spaces?"}})
		text, isErr := toolText(t, out)
		got <- result{text, isErr}
	}()

	in := waitOpenInput(t, app, c.ID)
	if card, _ := app.store.GetCard(c.ID); card.Status != StatusNeedsInput {
		t.Errorf("status while waiting = %s", card.Status)
	}
	select {
	case <-got:
		t.Fatal("ask_user returned before an answer")
	case <-time.After(100 * time.Millisecond):
	}

	if err := app.Answer(c.ID, in.ID, "tabs", false); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-got:
		if r.text != "tabs" || r.isErr {
			t.Errorf("ask_user returned %q (err %v)", r.text, r.isErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ask_user did not return after answer")
	}
	if card, _ := app.store.GetCard(c.ID); card.Status != StatusRunning {
		t.Errorf("status after answer = %s", card.Status)
	}
}

func TestMCPPermission(t *testing.T) {
	app := newTestApp(t, testDir(t))
	c := newCard(t, app, app.dataDir, "")

	ask := func(allow bool, note string) map[string]any {
		got := make(chan string, 1)
		go func() {
			_, out := rpc(t, app, c.ID, "tools/call", map[string]any{"name": "permission",
				"arguments": map[string]any{"tool_name": "Bash", "input": map[string]any{"command": "rm -rf build"}}})
			text, _ := toolText(t, out)
			got <- text
		}()
		in := waitOpenInput(t, app, c.ID)
		if in.Kind != "permission" {
			t.Fatalf("input kind = %s", in.Kind)
		}
		if err := app.Answer(c.ID, in.ID, note, allow); err != nil {
			t.Fatal(err)
		}
		var decision map[string]any
		json.Unmarshal([]byte(<-got), &decision)
		return decision
	}

	d := ask(true, "")
	if d["behavior"] != "allow" || d["updatedInput"].(map[string]any)["command"] != "rm -rf build" {
		t.Errorf("allow decision = %v", d)
	}
	d = ask(false, "use make clean")
	if d["behavior"] != "deny" || d["message"] != "Denied by the user. use make clean" {
		t.Errorf("deny decision = %v", d)
	}
}

func TestAnswerRejectsWrongInput(t *testing.T) {
	app := newTestApp(t, testDir(t))
	c := newCard(t, app, app.dataDir, "")
	if err := app.Answer(c.ID, 999, "x", false); err == nil {
		t.Error("answering a non-open input should fail")
	}
}
