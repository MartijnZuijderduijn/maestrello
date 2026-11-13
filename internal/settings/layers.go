// Package settings resolves and validates the layered settings. The board's Settings (global) are the base. A project can override
// them (Project.Settings), and a card kind can override them at either level
// (Settings.Kinds globally, LayerSettings.Kinds per project).
//
// Precedence, most specific first, for every overridable setting:
//
//	card kind in project > project > card kind (global) > global > built-in default
//
// A setting that a layer leaves unset (nil) falls through to the next layer; it never
// resets to empty. Overrides are pointers so that "" and 0 can still be set on purpose
// (e.g. effort "" = the harness default, max_turns 0 = no limit).
//
// The result is a plain Settings (Resolve), so code that already reads Settings
// (card creation, refinement) just gets the resolved values. A card keeps the values it
// was created with, so editing a layer affects new cards; refinement resolves when it runs.
package settings

import (
	"errors"

	"maestrello/internal/harness"
	"maestrello/internal/model"
	"maestrello/internal/store"
)

// Setting layer names, as reported in Sources.
const (
	LayerBuiltin     = "built-in"
	LayerGlobal      = "global"
	LayerGlobalKind  = "global kind"
	LayerProject     = "project"
	LayerProjectKind = "project kind"
)

// apply sets every field o has set on st and records layer as its source.
func apply(o model.Overrides, st *model.Settings, kind, layer string, src map[string]string) {
	str := func(field string, v *string, dst *string) {
		if v != nil {
			*dst, src[field] = *v, layer
		}
	}
	num := func(field string, v *int, dst *int) {
		if v != nil {
			*dst, src[field] = *v, layer
		}
	}
	flag := func(field string, v *bool, dst *bool) {
		if v != nil {
			*dst, src[field] = *v, layer
		}
	}
	str("harness", o.Harness, &st.Harness)
	str("model", o.Model, &st.DefaultModel)
	str("effort", o.Effort, &st.DefaultEffort)
	str("permission_mode", o.PermissionMode, &st.DefaultPermissionMode)
	num("max_turns", o.MaxTurns, &st.DefaultMaxTurns)
	num("max_minutes", o.MaxMinutes, &st.DefaultMaxMinutes)
	str("push_default", o.PushDefault, &st.DefaultPushDefault)
	str("refine_model", o.RefineModel, &st.RefineModel)
	str("refine_effort", o.RefineEffort, &st.RefineEffort)
	flag("auto_run", o.AutoRun, &st.DefaultAutoRun)
	flag("skip_refinement", o.SkipRefinement, &st.DefaultSkipRefinement)
	flag("auto_model", o.AutoModel, &st.AutoModel)
	if o.Tools != nil {
		tools := make(map[string][]string, len(st.DefaultTools)+1)
		for k, v := range st.DefaultTools {
			tools[k] = v
		}
		tools[kind] = append([]string{}, *o.Tools...)
		st.DefaultTools, src["tools"] = tools, layer
	}
}

// Sources says which layer each setting of a resolved Settings came from.
// Settings that no layer set are not listed: they are the built-in or the global value.
type Sources map[string]string

// resolveLayers applies the layers to the global settings for one card kind.
// proj may be nil (no project, or a project without settings); kind "" is code.
func resolveLayers(global model.Settings, proj *model.LayerSettings, kind string) (model.Settings, Sources) {
	if kind == "" {
		kind = model.KindCode
	}
	st := global
	src := Sources{}
	// The global Settings already carry the built-in defaults. Layers are applied from the
	// least to the most specific, so the last one that sets a value wins.
	apply(global.Kinds[kind], &st, kind, LayerGlobalKind, src) // reading a nil map is fine
	if proj != nil {
		apply(proj.Overrides, &st, kind, LayerProject, src)
		apply(proj.Kinds[kind], &st, kind, LayerProjectKind, src)
	}
	fitToHarness(&st)
	return st, src
}

// fitToHarness drops what the resolved harness does not accept, so layers that set the
// harness without setting its effort or permission mode still give a valid combination
// (the same rule a card edit follows when it changes the harness).
func fitToHarness(st *model.Settings) {
	h, err := harness.For(st.Harness)
	if err != nil {
		return
	}
	if h.ValidateEffort(st.DefaultEffort) != nil {
		st.DefaultEffort = ""
	}
	if h.ValidateEffort(st.RefineEffort) != nil {
		st.RefineEffort = ""
	}
	if modes := h.Caps().PermissionModes; h.ValidatePermissionMode(st.DefaultPermissionMode) != nil && len(modes) > 0 {
		st.DefaultPermissionMode = modes[0]
	}
}

// Resolve is the settings a card of this kind in this project runs with.
// project 0 means no project; an unknown project resolves like none.
func Resolve(s *store.Store, project int64, kind string) (model.Settings, Sources, error) {
	g, err := s.Settings()
	if err != nil {
		return g, nil, err
	}
	var proj *model.LayerSettings
	if project != 0 {
		l, err := s.ProjectSettings(project)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return g, nil, err
		}
		proj = &l
	}
	st, src := resolveLayers(g, proj, kind)
	return st, src, nil
}
