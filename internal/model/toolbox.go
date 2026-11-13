package model

import (
	"fmt"
	"slices"
	"strings"
)

// ToolDef is one catalog entry.
type ToolDef struct {
	ID          string            `json:"id"`
	Label       string            `json:"label"`
	Description string            `json:"description"`
	Harnesses   []string          `json:"harnesses"` // the harnesses that support it, sorted by name
	Native      map[string]string `json:"-"`         // harness name → that harness's tool name
	Phrase      string            `json:"-"`         // what it lets the agent do, for the instructions
}

var Toolbox = []ToolDef{
	{ID: "web_search", Label: "Web search", Description: "Search the web for current information.",
		Native: map[string]string{"claude": "WebSearch", "opencode": "websearch"}, Phrase: "search the web"},
	{ID: "web_fetch", Label: "Web fetch", Description: "Fetch and read a web page by URL.",
		Native: map[string]string{"claude": "WebFetch", "opencode": "webfetch"}, Phrase: "fetch web pages"},
	{ID: "skills", Label: "Skills", Description: "Use Claude Code skills.",
		Native: map[string]string{"claude": "Skill"}, Phrase: "use skills"},
}

func init() {
	for i := range Toolbox {
		for _, h := range []string{"claude", "opencode"} {
			if Toolbox[i].Native[h] != "" {
				Toolbox[i].Harnesses = append(Toolbox[i].Harnesses, h)
			}
		}
	}
}

func FindTool(id string) *ToolDef {
	for i := range Toolbox {
		if Toolbox[i].ID == id {
			return &Toolbox[i]
		}
	}
	return nil
}

// normalizeTools checks ids against the catalog (`unknown tool <id>`) and drops duplicates, keeping order.
func NormalizeTools(ids []string) ([]string, error) {
	out := []string{}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if FindTool(id) == nil {
			return nil, fmt.Errorf("unknown tool %s", id)
		}
		if !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out, nil
}

// nativeTools returns the harness's own names of the card's tools, in catalog-id order of the card,
// and the ids the harness does not support.
func (c *Card) NativeTools(harness string) (names, unsupported []string) {
	for _, id := range c.Tools {
		d := FindTool(id)
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
func (c *Card) ToolsSentence() string {
	harness := c.Harness
	if harness == "" {
		harness = DefaultHarness
	}
	var parts []string
	for _, id := range c.Tools {
		if d := FindTool(id); d != nil && d.Native[harness] != "" {
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

func ToolIDs() []string {
	ids := []string{}
	for _, d := range Toolbox {
		ids = append(ids, d.ID)
	}
	return ids
}
