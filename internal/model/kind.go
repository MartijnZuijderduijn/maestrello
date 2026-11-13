package model

import (
	"errors"
	"slices"
	"strings"
)

// Card kinds. Every kind runs through the same lifecycle, MCP endpoint and UI;
// the kind decides what the agent may touch and how its work ends.
const (
	KindCode     = "code"     // edits repos in worktrees (or local work); report_done → Review → Approve
	KindResearch = "research" // read-only; a written report through report_done
	KindPlan     = "plan"     // read-only; report_plan proposes tasks, Approve creates one card per task
	KindChat     = "chat"     // no tools; each reply ends the turn and waits for me in Needs input
)

var ValidKinds = []string{KindCode, KindResearch, KindPlan, KindChat}

// kind is the card's kind; cards saved before kinds existed are code.
func (c *Card) EffectiveKind() string {
	if c.Kind == "" {
		return KindCode
	}
	return c.Kind
}

// readOnly reports whether the card's agent only reads the root folder: no worktrees,
// no local work, no leases.
func (c *Card) ReadOnly() bool {
	return c.EffectiveKind() == KindResearch || c.EffectiveKind() == KindPlan
}

// usesRepos reports whether the card works in repos (worktrees or local work).
func (c *Card) UsesRepos() bool { return c.EffectiveKind() == KindCode }

// PlanTask is one task a plan card proposes. Approving the plan creates a card from it.
type PlanTask struct {
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Kind        string   `json:"kind,omitempty"` // code (default), research or plan
	Repos       []string `json:"repos,omitempty"`
}

// normalizeKind validates a kind given as input; "" means code.
func NormalizeKind(k string) (string, error) {
	k = strings.TrimSpace(k)
	if k == "" {
		return KindCode, nil
	}
	if !slices.Contains(ValidKinds, k) {
		return "", errors.New("invalid kind")
	}
	return k, nil
}
