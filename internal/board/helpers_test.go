package board

import (
	"regexp"
	"strings"
	"testing"

	"maestrello/internal/model"
)

func TestSlug(t *testing.T) {
	cases := map[string]string{
		"Fix the Login bug!":       "fix-the-login-bug",
		"  --weird__chars-- ":      "weird-chars",
		"":                         "task",
		"!!!":                      "task",
		strings.Repeat("abc ", 20): "abc-abc-abc-abc-abc-abc-abc-abc-abc-abc",
	}
	for in, want := range cases {
		if got := slug(in); got != want {
			t.Errorf("slug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBranchName(t *testing.T) {
	if got := branchName(&model.Card{ID: 7, Title: "Add dark mode"}); got != "maestrello/7-add-dark-mode" {
		t.Errorf("branchName = %q", got)
	}
}

func TestNewUUID(t *testing.T) {
	re := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		u := newUUID()
		if !re.MatchString(u) {
			t.Fatalf("not a v4 UUID: %s", u)
		}
		if seen[u] {
			t.Fatalf("duplicate UUID %s", u)
		}
		seen[u] = true
	}
}

func TestCardInputValidation(t *testing.T) {
	str := func(s string) *string { return &s }
	base := func() *model.Card {
		return &model.Card{Title: "t", RootFolder: "/abs", PermissionMode: "auto", Effort: "medium"}
	}
	bad := []cardInput{
		{Title: str("  ")},
		{RootFolder: str("relative/path")},
		{PermissionMode: str("yolo")},
		{Effort: str("extreme")},
		{PushDefault: str("force-push")},
	}
	for i, in := range bad {
		if err := in.apply(base()); err == nil {
			t.Errorf("case %d: expected validation error", i)
		}
	}
	c := base()
	in := cardInput{Title: str(" New "), PermissionMode: str("acceptEdits"), PushDefault: str("pr")}
	if err := in.apply(c); err != nil {
		t.Fatal(err)
	}
	if c.Title != "New" || c.PermissionMode != "acceptEdits" || c.PushDefault != "pr" {
		t.Errorf("apply did not set fields: %+v", c)
	}
}
