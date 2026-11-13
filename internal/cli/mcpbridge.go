package cli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"maestrello/internal/board"
)

// runBridge serves the board MCP endpoint over stdio, for clients like Claude Desktop that
// start MCP servers as commands. It reads one JSON-RPC message per line from in, posts it to
// <baseURL>/mcp and writes the reply as one line to out. Notifications (202) get no reply.
// Only JSON-RPC goes to out; anything else belongs on stderr.
func runBridge(in io.Reader, out io.Writer, baseURL string) error {
	// move_card with stop can take up to ~30s.
	client := &http.Client{Timeout: 2 * time.Minute}
	r := bufio.NewReader(in)
	for {
		line, err := r.ReadBytes('\n')
		if line = bytes.TrimSpace(line); len(line) > 0 {
			if reply := bridgeCall(client, baseURL+"/mcp", line); reply != nil {
				if _, werr := out.Write(append(reply, '\n')); werr != nil {
					return werr
				}
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// bridgeCall posts one message and returns the line to answer with, or nil for none.
func bridgeCall(client *http.Client, url string, msg []byte) []byte {
	var req board.RPCRequest
	parsed := json.Unmarshal(msg, &req) == nil
	fail := func(code int, text string) []byte {
		if parsed && len(req.ID) == 0 {
			return nil // a notification has no one to answer
		}
		id := req.ID
		if !parsed || len(id) == 0 {
			id = json.RawMessage("null")
		}
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "error": board.RPCError{Code: code, Message: text}})
		return b
	}
	resp, err := client.Post(url, "application/json", bytes.NewReader(msg))
	if err != nil {
		return fail(-32000, "maestrello board is not reachable ("+url+"): start it with \"maestrello start\" and try again")
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusAccepted {
		return nil
	}
	var buf bytes.Buffer
	if resp.StatusCode != http.StatusOK || json.Compact(&buf, body) != nil {
		return fail(-32000, fmt.Sprintf("board answered with status %d at %s; is it a maestrello board of this version?", resp.StatusCode, url))
	}
	return buf.Bytes()
}
