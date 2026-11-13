package harness

import (
	"slices"
	"strings"
	"testing"

	"maestrello/internal/model"
)

func TestClaudeSessionCommandKinds(t *testing.T) {
	codeArgs, _, _ := sessionArgs(t, testSpec()) // no kind: a code card, as before kinds existed
	if slices.Contains(codeArgs, "--tools") {
		t.Errorf("code card restricts tools: %v", codeArgs)
	}
	spec := testSpec()
	spec.Card.Kind = model.KindCode
	args, _, _ := sessionArgs(t, spec)
	args = slices.Clone(args)
	i := slices.Index(args, "--mcp-config") + 1
	args[i] = codeArgs[i] // each call writes its config in a scratch folder of its own
	if !slices.Equal(args, codeArgs) {
		t.Errorf("explicit code kind changes the args:\n%v\n%v", args, codeArgs)
	}

	for _, kind := range []string{model.KindResearch, model.KindPlan} {
		spec := testSpec()
		spec.Card.Kind = kind
		args, _, _ := sessionArgs(t, spec)
		if v, ok := flagValue(args, "--tools"); !ok || v != "Read,Grep,Glob" {
			t.Errorf("%s: --tools = %q, %v", kind, v, ok)
		}
		allowed, _ := flagValue(args, "--allowedTools")
		if slices.Contains(strings.Split(allowed, ","), "mcp__maestrello__add_repo") {
			t.Errorf("%s allows add_repo: %s", kind, allowed)
		}
		if got := slices.Contains(strings.Split(allowed, ","), "mcp__maestrello__report_plan"); got != (kind == model.KindPlan) {
			t.Errorf("%s: report_plan allowed = %v (%s)", kind, got, allowed)
		}
		if !strings.Contains(allowed, "mcp__maestrello__ask_user") || !strings.Contains(allowed, "mcp__maestrello__report_failed") {
			t.Errorf("%s: allowed = %s", kind, allowed)
		}
		if got := strings.Contains(allowed, "mcp__maestrello__report_done"); got != (kind == model.KindResearch) {
			t.Errorf("%s: report_done allowed = %v", kind, got)
		}
	}

	spec = testSpec()
	spec.Card.Kind = model.KindChat
	args, _, _ = sessionArgs(t, spec)
	if v, ok := flagValue(args, "--tools"); !ok || v != "" {
		t.Errorf("chat: --tools = %q, %v", v, ok)
	}
	allowed, _ := flagValue(args, "--allowedTools")
	if strings.Contains(allowed, "add_repo") || strings.Contains(allowed, "report_plan") {
		t.Errorf("chat allowed = %s", allowed)
	}
}
