package harness

import (
	"slices"
	"strings"
	"testing"

	"maestrello/internal/model"
)

func toolSpec(kind string, tools ...string) SessionSpec {
	spec := testSpec()
	spec.Card.Kind, spec.Card.Tools = kind, tools
	return spec
}

func TestClaudeToolboxArgs(t *testing.T) {
	// Chat: --tools lists the tool, and it is allowed next to the chat board tools.
	args, _, _ := sessionArgs(t, toolSpec(model.KindChat, "web_search"))
	if v, ok := flagValue(args, "--tools"); !ok || v != "WebSearch" {
		t.Errorf("chat --tools = %q, %v", v, ok)
	}
	allowed, _ := flagValue(args, "--allowedTools")
	if allowed != "mcp__maestrello__report_failed,mcp__maestrello__ask_user,WebSearch" {
		t.Errorf("chat allowed = %s", allowed)
	}
	if slices.Contains(args, "--add-dir") {
		t.Error("chat got --add-dir")
	}
	// Read-only kinds keep Read,Grep,Glob and add the tool.
	for _, kind := range []string{model.KindResearch, model.KindPlan} {
		args, _, _ := sessionArgs(t, toolSpec(kind, "web_search", "skills"))
		if v, _ := flagValue(args, "--tools"); v != "Read,Grep,Glob,WebSearch,Skill" {
			t.Errorf("%s --tools = %q", kind, v)
		}
		allowed, _ := flagValue(args, "--allowedTools")
		if !strings.HasSuffix(allowed, ",WebSearch,Skill") || strings.Contains(allowed, "Edit") || strings.Contains(allowed, "Bash") {
			t.Errorf("%s allowed = %s", kind, allowed)
		}
	}
	// Code: still no --tools flag, but the tool is allowed.
	args, _, _ = sessionArgs(t, toolSpec(model.KindCode, "web_fetch"))
	if slices.Contains(args, "--tools") {
		t.Errorf("code card has --tools: %v", args)
	}
	allowed, _ = flagValue(args, "--allowedTools")
	if !strings.HasSuffix(allowed, ",WebFetch") {
		t.Errorf("code allowed = %s", allowed)
	}
}

func TestEmptyToolboxArgsUnchanged(t *testing.T) {
	for _, kind := range []string{"", model.KindCode, model.KindResearch, model.KindPlan, model.KindChat} {
		base, _, _ := sessionArgs(t, toolSpec(kind))
		for _, empty := range [][]string{nil, {}} {
			spec := toolSpec(kind)
			spec.Card.Tools = empty
			got, _, _ := sessionArgs(t, spec)
			got, base := slices.Clone(got), slices.Clone(base)
			i := slices.Index(base, "--mcp-config") + 1
			got[i] = base[i]
			if !slices.Equal(got, base) {
				t.Errorf("%s: args differ with empty toolbox:\n%v\n%v", kind, got, base)
			}
		}
	}
	// And the literal values from before the toolbox.
	args, _, _ := sessionArgs(t, toolSpec(model.KindChat))
	if v, ok := flagValue(args, "--tools"); !ok || v != "" {
		t.Errorf("chat --tools = %q, %v", v, ok)
	}
}

func TestOpencodeToolbox(t *testing.T) {
	spec := ocSpec(model.KindChat)
	spec.Card.Tools = []string{"web_search", "skills"}
	spec.Strict = true
	_, _, _, cfg := ocSession(t, spec)
	tools, perm := ocAgent(t, cfg)
	if tools["websearch"] != true || perm["websearch"] != "allow" {
		t.Errorf("websearch: tool %v perm %v", tools["websearch"], perm["websearch"])
	}
	if tools["webfetch"] != false || perm["webfetch"] != "deny" {
		t.Errorf("strict chat: webfetch tool %v perm %v", tools["webfetch"], perm["webfetch"])
	}
	if tools["bash"] != false || tools["edit"] != false || perm["bash"] != "deny" {
		t.Errorf("chat with tools can write: %v %v", tools, perm)
	}
	if _, has := tools["Skill"]; has || tools["skill"] == true {
		t.Errorf("skills leaked into opencode: %v", tools)
	}
	if _, unsupported := spec.Card.NativeTools("opencode"); !slices.Equal(unsupported, []string{"skills"}) {
		t.Errorf("unsupported = %v", unsupported)
	}
	// Strict denies webfetch unless the card asked for it.
	spec = ocSpec(model.KindCode)
	spec.Card.Tools, spec.Strict = []string{"web_fetch"}, true
	_, _, _, cfg = ocSession(t, spec)
	tools, perm = ocAgent(t, cfg)
	if tools["webfetch"] != true || perm["webfetch"] != "allow" {
		t.Errorf("selected web_fetch in strict: %v %v", tools["webfetch"], perm["webfetch"])
	}
	// An empty toolbox: nothing about websearch.
	_, _, _, cfg = ocSession(t, ocSpec(model.KindChat))
	tools, perm = ocAgent(t, cfg)
	if tools["websearch"] != false || perm["websearch"] != nil {
		t.Errorf("empty toolbox: %v %v", tools["websearch"], perm["websearch"])
	}
}
