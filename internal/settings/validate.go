package settings

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"maestrello/internal/harness"
	"maestrello/internal/model"
)

// validateModelInfo checks the user-supplied catalog entries.
func validateModelInfo(info map[string]model.ModelMeta) error {
	for key, m := range info {
		h, name, ok := strings.Cut(key, "/")
		if _, err := harness.For(h); !ok || name == "" || err != nil {
			return fmt.Errorf("model_info key %q must be <harness>/<model> with a known harness", key)
		}
		if m.Tier < model.MinTier || m.Tier > model.MaxTier {
			return fmt.Errorf("model_info %q: tier must be %d to %d", key, model.MinTier, model.MaxTier)
		}
		if m.Cost < 0 {
			return fmt.Errorf("model_info %q: cost must not be negative", key)
		}
	}
	return nil
}

// validateOverrides checks one layer's overrides. Effort and permission mode are checked against the
// layer's own harness when it sets one, and otherwise against any known harness (the final
// combination is fitted when resolving).
func validateOverrides(o model.Overrides, where string) error {
	wrap := func(err error) error { return fmt.Errorf("%s: %w", where, err) }
	var harnessList []harness.Harness
	if o.Harness != nil {
		h, err := harness.For(*o.Harness)
		if err != nil {
			return wrap(err)
		}
		harnessList = []harness.Harness{h}
	} else {
		for _, name := range harness.Names() {
			h, _ := harness.For(name)
			harnessList = append(harnessList, h)
		}
	}
	anyOK := func(f func(harness.Harness) error) bool {
		for _, h := range harnessList {
			if f(h) == nil {
				return true
			}
		}
		return false
	}
	switch {
	case o.Model != nil && strings.TrimSpace(*o.Model) == "":
		return wrap(errors.New("model must not be empty"))
	case o.RefineModel != nil && strings.TrimSpace(*o.RefineModel) == "":
		return wrap(errors.New("refinement model must not be empty"))
	case o.Effort != nil && !anyOK(func(h harness.Harness) error { return h.ValidateEffort(*o.Effort) }):
		return wrap(errors.New("invalid effort"))
	case o.RefineEffort != nil && !anyOK(func(h harness.Harness) error { return h.ValidateEffort(*o.RefineEffort) }):
		return wrap(errors.New("invalid refinement effort"))
	case o.PermissionMode != nil && !anyOK(func(h harness.Harness) error { return h.ValidatePermissionMode(*o.PermissionMode) }):
		return wrap(errors.New("invalid permission mode"))
	case o.PushDefault != nil && !model.ValidPush[*o.PushDefault]:
		return wrap(errors.New("invalid push default"))
	case o.SkipRefinement != nil && *o.SkipRefinement && o.AutoModel != nil && *o.AutoModel:
		return wrap(errors.New("skip refinement and automatic model selection exclude each other"))
	case o.MaxTurns != nil && *o.MaxTurns < 0, o.MaxMinutes != nil && *o.MaxMinutes < 0:
		return wrap(errors.New("limits must not be negative"))
	}
	if o.Tools != nil {
		if _, err := model.NormalizeTools(*o.Tools); err != nil {
			return wrap(err)
		}
	}
	return nil
}

func validateKinds(where string, kinds map[string]model.Overrides) error {
	for k, o := range kinds {
		if !slices.Contains(model.ValidKinds, k) {
			return fmt.Errorf("%s: invalid kind %s", where, k)
		}
		if err := validateOverrides(o, where+" kind "+k); err != nil {
			return err
		}
	}
	return nil
}

// ValidateLayer checks a project's settings.
func ValidateLayer(l model.LayerSettings, where string) error {
	if err := validateOverrides(l.Overrides, where); err != nil {
		return err
	}
	return validateKinds(where, l.Kinds)
}

// Validate checks the values a PUT /api/settings may save.
func Validate(st model.Settings) error {
	h, err := harness.For(st.Harness) // "" is claude: the web UI does not send it
	if err != nil {
		return err
	}
	switch {
	case strings.TrimSpace(st.DefaultModel) == "":
		return errors.New("default model is required")
	case strings.TrimSpace(st.RefineModel) == "":
		return errors.New("refinement model is required")
	case st.DefaultEffort == "" && len(h.Caps().Efforts) > 0 || h.ValidateEffort(st.DefaultEffort) != nil:
		return errors.New("invalid default effort")
	case st.RefineEffort == "" && len(h.Caps().Efforts) > 0 || h.ValidateEffort(st.RefineEffort) != nil:
		return errors.New("invalid refinement effort")
	case h.ValidatePermissionMode(st.DefaultPermissionMode) != nil:
		return errors.New("invalid default permission mode")
	case !model.ValidPush[st.DefaultPushDefault]:
		return errors.New("invalid default push default")
	case st.DefaultSkipRefinement && st.AutoModel:
		return errors.New("skip refinement and automatic model selection exclude each other")
	case st.DefaultMaxTurns < 0 || st.DefaultMaxMinutes < 0:
		return errors.New("limits must not be negative")
	}
	if err := validateModelInfo(st.ModelInfo); err != nil {
		return err
	}
	if err := validateKinds("kinds", st.Kinds); err != nil {
		return err
	}
	return validateDefaultTools(st)
}

// validateDefaultTools checks Settings.DefaultTools (kinds and ids) and drops duplicates.
func validateDefaultTools(st model.Settings) error {
	for kind, ids := range st.DefaultTools {
		if !slices.Contains(model.ValidKinds, kind) {
			return fmt.Errorf("default tools: invalid kind %s", kind)
		}
		n, err := model.NormalizeTools(ids)
		if err != nil {
			return err
		}
		st.DefaultTools[kind] = n
	}
	return nil
}
