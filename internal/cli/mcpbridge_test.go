package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"maestrello/internal/testenv"
)

func TestBridge(t *testing.T) {
	addr, dataDir := runningBoard(t, testenv.Dir(t))
	in := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"create_card","arguments":{"title":"Bridged","root_folder":%q}}}`, dataDir),
	}, "\n") + "\n"
	var out bytes.Buffer
	if err := runBridge(strings.NewReader(in), &out, "http://"+addr); err != nil {
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
	res, _ := call["result"].(map[string]any)
	text := ""
	if content, ok := res["content"].([]any); ok && len(content) > 0 {
		text, _ = content[0].(map[string]any)["text"].(string)
	}
	if isErr, _ := res["isError"].(bool); isErr || !strings.Contains(text, "Bridged") {
		t.Errorf("create_card reply: %s", lines[1])
	}
	resp, err := http.Get("http://" + addr + "/api/cards")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var cards []map[string]any
	if json.NewDecoder(resp.Body).Decode(&cards); len(cards) != 1 {
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
