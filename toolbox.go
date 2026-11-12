package main

import (
	"fmt"
	"net/http"
	"strings"
)

// The toolbox: a fixed, harness-neutral catalog of optional agent tools that a card can switch on.
// A card's `tools` holds catalog ids; each harness adapter maps an id to its own tool name, or
// does not support it (then the tool is skipped, with a card note).

// ToolDef is one catalog entry.
type ToolDef struct {
	ID          string            `json:"id"`
	Label       string            `json:"label"`
	Description string            `json:"description"`
	Harnesses   []string          `json:"harnesses"` // the harnesses that support it, sorted by name
	Native      map[string]string `json:"-"`         // harness name → that harness's tool name
	Phrase      string            `json:"-"`         // what it lets the agent do, for the instructions
}

var toolbox = []ToolDef{
	{ID: "web_search", Label: "Web search", Description: "Search the web for current information.",
		Native: map[string]string{"claude": "WebSearch", "opencode": "websearch"}, Phrase: "search the web"},
	{ID: "web_fetch", Label: "Web fetch", Description: "Fetch and read a web page by URL.",
		Native: map[string]string{"claude": "WebFetch", "opencode": "webfetch"}, Phrase: "fetch web pages"},
	{ID: "skills", Label: "Skills", Description: "Use Claude Code skills.",
		Native: map[string]string{"claude": "Skill"}, Phrase: "use skills"},
}

func init() {
	for i := range toolbox {
		for _, h := range []string{"claude", "opencode"} {
			if toolbox[i].Native[h] != "" {
				toolbox[i].Harnesses = append(toolbox[i].Harnesses, h)
			}
		}
	}
}

func toolDef(id string) *ToolDef {
	for i := range toolbox {
		if toolbox[i].ID == id {
			return &toolbox[i]
		}
	}
	return nil
}

// normalizeTools checks ids against the catalog (`unknown tool <id>`) and drops duplicates, keeping order.
func normalizeTools(ids []string) ([]string, error) {
	out := []string{}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if toolDef(id) == nil {
			return nil, fmt.Errorf("unknown tool %s", id)
		}
		if !contains(out, id) {
			out = append(out, id)
		}
	}
	return out, nil
}

// nativeTools returns the harness's own names of the card's tools, in catalog-id order of the card,
// and the ids the harness does not support.
func (c *Card) nativeTools(harness string) (names, unsupported []string) {
	for _, id := range c.Tools {
		d := toolDef(id)
		switch {
		case d == nil:
		case d.Native[harness] == "":
			unsupported = append(unsupported, id)
		default:
			names = append(names, d.Native[harness])
		}
	}
	return names, unsupported
}

// toolsSentence names what the card's tools let the agent do ("search the web with WebSearch and
// fetch web pages with WebFetch"); "" when it has none that its harness supports.
func (c *Card) toolsSentence() string {
	harness := c.Harness
	if harness == "" {
		harness = defaultHarness
	}
	var parts []string
	for _, id := range c.Tools {
		if d := toolDef(id); d != nil && d.Native[harness] != "" {
			parts = append(parts, d.Phrase+" with "+d.Native[harness])
		}
	}
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	}
	return strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1]
}

// validateDefaultTools checks Settings.DefaultTools (kinds and ids) and drops duplicates.
func (st Settings) validateDefaultTools() error {
	for kind, ids := range st.DefaultTools {
		if !contains(validKinds, kind) {
			return fmt.Errorf("default tools: invalid kind %s", kind)
		}
		n, err := normalizeTools(ids)
		if err != nil {
			return err
		}
		st.DefaultTools[kind] = n
	}
	return nil
}

// handleToolbox serves the catalog: [{id, label, description, harnesses}].
func (a *App) handleToolbox(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, toolbox)
}

func toolIDs() []string {
	ids := []string{}
	for _, d := range toolbox {
		ids = append(ids, d.ID)
	}
	return ids
}
