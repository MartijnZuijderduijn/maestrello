package harness

import (
	"slices"
	"strings"
	"testing"

	"maestrello/internal/model"
)

func TestClaudeSessionContextDirs(t *testing.T) {
	spec := testSpec() // root /root
	spec.Context = []model.ContextItem{
		{Path: "/ref/docs", Type: "dir"}, {Path: "/ref/a.txt", Type: "file"}, {Path: "/ref/b.txt", Type: "file"}, // parent /ref/ once
		{Path: "/ref/docs", Type: "dir"},    // duplicate
		{Path: "/root/inner", Type: "dir"},  // under the root: skipped
		{Path: "/root/x.txt", Type: "file"}, // parent is the root: skipped
	}
	args, _, _ := sessionArgs(t, spec)
	if got, want := addDirs(args), []string{"/root", "/ref/docs", "/ref"}; !slices.Equal(got, want) {
		t.Errorf("--add-dir = %v, want %v", got, want)
	}
	if d := addDirs(args); slices.Index(args, "--append-system-prompt") < slices.Index(args, d[len(d)-1]) {
		t.Errorf("--add-dir after --append-system-prompt: %v", args)
	}

	spec.Card.Kind = model.KindChat
	args, _, _ = sessionArgs(t, spec)
	if got := addDirs(args); len(got) != 0 {
		t.Errorf("chat got --add-dir %v", got)
	}
	for _, k := range []string{model.KindResearch, model.KindPlan} {
		spec.Card.Kind = k
		args, _, _ = sessionArgs(t, spec)
		if !slices.Contains(addDirs(args), "/ref/docs") {
			t.Errorf("%s: context dir missing: %v", k, args)
		}
	}
}

func TestClaudeRefineExtraDirs(t *testing.T) {
	h := claudeHarness{bin: "/bin/claude"}
	cmd, err := h.RefineCommand(t.Context(), RefineSpec{Model: "m", Tools: "Read", Schema: "{}", ExtraDirs: []string{"/a", "/b"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := addDirs(cmd.Args); !slices.Equal(got, []string{"/a", "/b"}) {
		t.Errorf("--add-dir = %v", got)
	}
	cmd, _ = h.RefineCommand(t.Context(), RefineSpec{Model: "m", Schema: "{}"})
	if got := addDirs(cmd.Args); len(got) != 0 {
		t.Errorf("no ExtraDirs but --add-dir %v", got)
	}
}

func TestOpencodeContextPermissions(t *testing.T) {
	spec := ocSpec(model.KindChat)
	spec.Context = []model.ContextItem{{Path: "/ref", Type: "dir"}}
	_, _, _, cfg := ocSession(t, spec)
	if _, perm := ocAgent(t, cfg); perm["external_directory"] != "deny" {
		t.Errorf("chat external_directory = %v", perm["external_directory"])
	}
	spec = ocSpec(model.KindResearch)
	_, _, _, cfg = ocSession(t, spec)
	if _, perm := ocAgent(t, cfg); perm["external_directory"] != "allow" {
		t.Errorf("research external_directory = %v", perm["external_directory"])
	}

	h := newOpencodeHarness("/bin/opencode")
	for _, c := range []struct {
		extra []string
		want  string
	}{{nil, "deny"}, {[]string{"/ref"}, "allow"}} {
		cmd, err := h.RefineCommand(t.Context(), RefineSpec{Model: "ollama/x", Tools: "Read", Schema: "{}", ExtraDirs: c.extra})
		if err != nil {
			t.Fatal(err)
		}
		var env string
		for _, e := range cmd.Env {
			if v, ok := strings.CutPrefix(e, "OPENCODE_CONFIG_CONTENT="); ok {
				env = v
			}
		}
		if !strings.Contains(env, `"external_directory":"`+c.want+`"`) || !strings.Contains(env, `"edit":"deny"`) ||
			!strings.Contains(env, `"bash":"deny"`) || !strings.Contains(env, `"webfetch":"deny"`) {
			t.Errorf("extra %v: config %s", c.extra, env)
		}
	}
}

func addDirs(args []string) []string {
	var out []string
	for i, a := range args {
		if a == "--add-dir" && i+1 < len(args) {
			out = append(out, args[i+1])
		}
	}
	return out
}
