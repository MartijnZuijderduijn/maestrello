package main

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// Automatic model selection. With Settings.AutoModel on, the manual model setting is ignored
// for cards that get refined: the refiner is told which (harness, model) pairs exist, how
// capable (tier) and how expensive (relative cost) each is, and answers with the smallest one
// that can do the task well. The board validates the answer and falls back safely; see
// resolveModel. With the setting off none of this runs.

// Tier scale: how capable a model is, 1 (small, fast, for simple edits) to 5 (the most capable).
const (
	minTier = 1
	maxTier = 5
)

// ModelMeta is what the board knows about one model. Both numbers are relative, not prices:
// Cost is in arbitrary units (0 = free, e.g. a local Ollama model), and only the ordering matters.
type ModelMeta struct {
	Tier int     `json:"tier"` // capability, 1 (small) to 5 (most capable)
	Cost float64 `json:"cost"` // relative price per task, 0 = free
}

// defaultModelInfo is the built-in catalog, keyed "<harness>/<model>". The numbers are rough
// relative figures for the claude aliases, not real prices; Settings.ModelInfo overrides or extends
// them (that is how opencode/ollama models become selectable: a model without an entry is never
// offered to the refiner, as the board will not invent capability or cost for it).
func defaultModelInfo() map[string]ModelMeta {
	return map[string]ModelMeta{
		"claude/haiku":             {Tier: 1, Cost: 1},
		"claude/sonnet":            {Tier: 3, Cost: 3},
		"claude/opus":              {Tier: 5, Cost: 15},
		"claude/claude-haiku-5-5":  {Tier: 1, Cost: 1},
		"claude/claude-sonnet-5-5": {Tier: 3, Cost: 3},
		"claude/claude-opus-5-5":   {Tier: 5, Cost: 15},
	}
}

// modelInfoKey is the catalog key of a model on a harness.
func modelInfoKey(harness, model string) string {
	if harness == "" {
		harness = defaultHarness
	}
	return harness + "/" + model
}

// validateModelInfo checks the user-supplied catalog entries.
func validateModelInfo(info map[string]ModelMeta) error {
	for key, m := range info {
		h, model, ok := strings.Cut(key, "/")
		if _, err := harnessFor(h); !ok || model == "" || err != nil {
			return fmt.Errorf("model_info key %q must be <harness>/<model> with a known harness", key)
		}
		if m.Tier < minTier || m.Tier > maxTier {
			return fmt.Errorf("model_info %q: tier must be %d to %d", key, minTier, maxTier)
		}
		if m.Cost < 0 {
			return fmt.Errorf("model_info %q: cost must not be negative", key)
		}
	}
	return nil
}

// Candidate is one (harness, model) pair the refiner may choose.
type Candidate struct {
	Harness, Model string
	ModelMeta
}

func (c Candidate) String() string {
	return fmt.Sprintf("harness=%s model=%s tier=%d cost=%g", c.Harness, c.Model, c.Tier, c.Cost)
}

// modelCandidates lists the models the refiner may route a task to: every model of every
// available harness that has catalog metadata (built-ins overridden by settings), in a stable order.
func (a *App) modelCandidates(st Settings) []Candidate {
	info := defaultModelInfo()
	maps.Copy(info, st.ModelInfo)
	var out []Candidate
	for _, name := range slices.Sorted(maps.Keys(harnesses)) {
		hs := a.harnessStatus(name)
		if !hs.Available {
			continue
		}
		models := hs.Models
		if name == "claude" {
			models = knownModels
		}
		for _, m := range models {
			if meta, ok := info[modelInfoKey(name, m)]; ok {
				out = append(out, Candidate{name, m, meta})
			}
		}
	}
	return out
}

// compareCandidates orders by cost, then by lower tier (smaller), then by name, so the choice is deterministic.
func compareCandidates(a, b Candidate) int {
	return cmp.Or(cmp.Compare(a.Cost, b.Cost), cmp.Compare(a.Tier, b.Tier),
		cmp.Compare(a.Harness, b.Harness), cmp.Compare(a.Model, b.Model))
}

// pickModel is the board's own rule, used when the refiner names no valid model but rates the task:
// the cheapest candidate whose tier covers the task's complexity (1 to 5, on the tier scale). A free
// adapter wins whenever it is capable enough. When nothing is capable enough the most capable
// candidate (cheapest among equals) is used: the task is too hard for the pool, so do the best possible.
func pickModel(cands []Candidate, complexity int) (Candidate, bool) {
	var capable []Candidate
	for _, c := range cands {
		if c.Tier >= complexity {
			capable = append(capable, c)
		}
	}
	if len(capable) > 0 {
		return slices.MinFunc(capable, compareCandidates), true
	}
	if len(cands) == 0 {
		return Candidate{}, false
	}
	return slices.MinFunc(cands, func(a, b Candidate) int {
		return cmp.Or(-cmp.Compare(a.Tier, b.Tier), compareCandidates(a, b))
	}), true
}

// modelChoice is the part of the refiner's answer that picks the model.
type modelChoice struct {
	Model      string `json:"model"`
	Harness    string `json:"harness"`
	Complexity int    `json:"complexity"`
	Reason     string `json:"model_reason"`
}

// resolveModel turns the refiner's choice into a candidate, or says why it cannot.
// Order: the named (harness, model) if it is a candidate; else, if the refiner rated the task, the
// board's pickModel rule; else not ok (the caller keeps the manual model). The returned reason is for the log.
func resolveModel(cands []Candidate, ch modelChoice) (Candidate, string, bool) {
	why := strings.TrimSpace(ch.Reason)
	if why == "" {
		why = "no reason given"
	}
	if m := strings.TrimSpace(ch.Model); m != "" {
		h := strings.TrimSpace(ch.Harness)
		var match []Candidate
		for _, c := range cands {
			if c.Model == m && (h == "" || c.Harness == h) {
				match = append(match, c)
			}
		}
		if len(match) > 0 {
			return slices.MinFunc(match, compareCandidates), "refiner chose it: " + why, true
		}
		if ch.Complexity >= minTier && ch.Complexity <= maxTier {
			if c, ok := pickModel(cands, ch.Complexity); ok {
				return c, fmt.Sprintf("refiner chose %s/%s, which is not available; picked the cheapest capable model for complexity %d", h, m, ch.Complexity), true
			}
		}
		return Candidate{}, fmt.Sprintf("refiner chose %s/%s, which is not available", h, m), false
	}
	if ch.Complexity >= minTier && ch.Complexity <= maxTier {
		if c, ok := pickModel(cands, ch.Complexity); ok {
			return c, fmt.Sprintf("refiner named no model; picked the cheapest capable model for complexity %d", ch.Complexity), true
		}
	}
	return Candidate{}, "refiner gave no usable model choice", false
}

// autoModelSchemaProps are the extra answer fields asked for when automatic selection is on.
const autoModelSchemaProps = `,
    "complexity": {"type": "integer", "minimum": 1, "maximum": 5, "description": "How demanding the task is, on the same 1-5 scale as the model tiers"},
    "model": {"type": "string", "description": "The chosen model, exactly as listed"},
    "harness": {"type": "string", "description": "The adapter (harness) the chosen model runs on, exactly as listed"},
    "model_reason": {"type": "string", "description": "One sentence: why this model is the smallest that can do the task"}`

// refineSchemaFor is the refiner's answer schema; with automatic selection it also asks for the model choice.
func refineSchemaFor(auto bool) string {
	if !auto {
		return refineSchema
	}
	i := strings.LastIndex(refineSchema, "}\n  },")
	return refineSchema[:i+1] + autoModelSchemaProps + refineSchema[i+1:]
}

// modelSelectionPrompt is the paragraph that tells the refiner which models it may choose from.
func modelSelectionPrompt(cands []Candidate) string {
	var b strings.Builder
	b.WriteString("\n\nAlso choose the model the coding agent runs with. Pick the smallest, cheapest model that can still do this task well: " +
		"weigh capability against cost, neither one alone. A simple, well-contained task (a small edit, a rename, a docs change) goes to a low tier; " +
		"a hard or large task (many files, subtle design, tricky debugging) needs a higher tier, as a model that is too weak fails and costs more in the end. " +
		"Prefer a free or cheaper adapter whenever its model is capable enough for the task. " +
		"Rate the task's complexity from 1 to 5 on the same scale as the tiers, then answer model and harness exactly as listed, with a one-sentence model_reason.\n" +
		"Tier is capability (1 = small, 5 = most capable); cost is relative (0 = free).\nAvailable models:\n")
	for _, c := range cands {
		fmt.Fprintf(&b, "- harness=%s model=%s tier=%d cost=%g\n", c.Harness, c.Model, c.Tier, c.Cost)
	}
	return b.String()
}
