package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// Layered settings. The board's Settings (global) are the base. A project can override
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
// The result is a plain Settings (ResolveSettings), so code that already reads Settings
// (card creation, refinement) just gets the resolved values. A card keeps the values it
// was created with, so editing a layer affects new cards; refinement resolves when it runs.

// Overrides are the settings a layer may set. Field names match the JSON of Settings
// without the "default_" prefix where the global setting has one.
type Overrides struct {
	Harness        *string   `json:"harness,omitempty"`
	Model          *string   `json:"model,omitempty"`
	Effort         *string   `json:"effort,omitempty"`
	PermissionMode *string   `json:"permission_mode,omitempty"`
	MaxTurns       *int      `json:"max_turns,omitempty"`
	MaxMinutes     *int      `json:"max_minutes,omitempty"`
	PushDefault    *string   `json:"push_default,omitempty"`
	RefineModel    *string   `json:"refine_model,omitempty"`
	RefineEffort   *string   `json:"refine_effort,omitempty"`
	AutoRun        *bool     `json:"auto_run,omitempty"`
	SkipRefinement *bool     `json:"skip_refinement,omitempty"`
	AutoModel      *bool     `json:"auto_model,omitempty"`
	Tools          *[]string `json:"tools,omitempty"` // toolbox ids new cards of the kind start with
}

// LayerSettings is a project's own settings: overrides for every card in the project,
// and per card kind overrides within the project.
type LayerSettings struct {
	Overrides
	Kinds map[string]Overrides `json:"kinds,omitempty"`
}

// Setting layer names, as reported in Sources.
const (
	LayerBuiltin     = "built-in"
	LayerGlobal      = "global"
	LayerGlobalKind  = "global kind"
	LayerProject     = "project"
	LayerProjectKind = "project kind"
)

// apply sets every field o has set on st and records layer as its source.
func (o Overrides) apply(st *Settings, kind, layer string, src map[string]string) {
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
func resolveLayers(global Settings, proj *LayerSettings, kind string) (Settings, Sources) {
	if kind == "" {
		kind = KindCode
	}
	st := global
	src := Sources{}
	// The global Settings already carry the built-in defaults. Layers are applied from the
	// least to the most specific, so the last one that sets a value wins.
	global.Kinds[kind].apply(&st, kind, LayerGlobalKind, src) // reading a nil map is fine
	if proj != nil {
		proj.Overrides.apply(&st, kind, LayerProject, src)
		proj.Kinds[kind].apply(&st, kind, LayerProjectKind, src)
	}
	st.fitToHarness()
	return st, src
}

// fitToHarness drops what the resolved harness does not accept, so layers that set the
// harness without setting its effort or permission mode still give a valid combination
// (the same rule a card edit follows when it changes the harness).
func (st *Settings) fitToHarness() {
	h, err := harnessFor(st.Harness)
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

// validate checks one layer's overrides. Effort and permission mode are checked against the
// layer's own harness when it sets one, and otherwise against any known harness (the final
// combination is fitted when resolving).
func (o Overrides) validate(where string) error {
	wrap := func(err error) error { return fmt.Errorf("%s: %w", where, err) }
	var harnessList []Harness
	if o.Harness != nil {
		h, err := harnessFor(*o.Harness)
		if err != nil {
			return wrap(err)
		}
		harnessList = []Harness{h}
	} else {
		for name := range harnesses {
			h, _ := harnessFor(name)
			harnessList = append(harnessList, h)
		}
	}
	anyOK := func(f func(Harness) error) bool {
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
	case o.Effort != nil && !anyOK(func(h Harness) error { return h.ValidateEffort(*o.Effort) }):
		return wrap(errors.New("invalid effort"))
	case o.RefineEffort != nil && !anyOK(func(h Harness) error { return h.ValidateEffort(*o.RefineEffort) }):
		return wrap(errors.New("invalid refinement effort"))
	case o.PermissionMode != nil && !anyOK(func(h Harness) error { return h.ValidatePermissionMode(*o.PermissionMode) }):
		return wrap(errors.New("invalid permission mode"))
	case o.PushDefault != nil && !validPush[*o.PushDefault]:
		return wrap(errors.New("invalid push default"))
	case o.MaxTurns != nil && *o.MaxTurns < 0, o.MaxMinutes != nil && *o.MaxMinutes < 0:
		return wrap(errors.New("limits must not be negative"))
	}
	if o.Tools != nil {
		if _, err := normalizeTools(*o.Tools); err != nil {
			return wrap(err)
		}
	}
	return nil
}

func validateKinds(where string, kinds map[string]Overrides) error {
	for k, o := range kinds {
		if !contains(validKinds, k) {
			return fmt.Errorf("%s: invalid kind %s", where, k)
		}
		if err := o.validate(where + " kind " + k); err != nil {
			return err
		}
	}
	return nil
}

func (l LayerSettings) validate(where string) error {
	if err := l.Overrides.validate(where); err != nil {
		return err
	}
	return validateKinds(where, l.Kinds)
}

// isEmpty reports whether the layer sets nothing.
func (l LayerSettings) isEmpty() bool {
	b, _ := json.Marshal(l)
	return string(b) == "{}"
}

// --- storage ---

// ProjectSettings reads a project's own settings (empty when it has none).
func (s *Store) ProjectSettings(id int64) (LayerSettings, error) {
	var raw string
	err := s.db.QueryRow(`SELECT settings FROM projects WHERE id=?`, id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return LayerSettings{}, ErrNotFound
	}
	var l LayerSettings
	if err != nil || raw == "" {
		return l, err
	}
	return l, json.Unmarshal([]byte(raw), &l)
}

func (s *Store) SaveProjectSettings(id int64, l LayerSettings) error {
	raw := ""
	if !l.isEmpty() {
		b, err := json.Marshal(l)
		if err != nil {
			return err
		}
		raw = string(b)
	}
	_, err := s.db.Exec(`UPDATE projects SET settings=? WHERE id=?`, raw, id)
	return err
}

// ResolveSettings is the settings a card of this kind in this project runs with.
// project 0 means no project; an unknown project resolves like none.
func (s *Store) ResolveSettings(project int64, kind string) (Settings, Sources, error) {
	g, err := s.Settings()
	if err != nil {
		return g, nil, err
	}
	var proj *LayerSettings
	if project != 0 {
		l, err := s.ProjectSettings(project)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return g, nil, err
		}
		proj = &l
	}
	st, src := resolveLayers(g, proj, kind)
	return st, src, nil
}

// --- HTTP ---

// GET /api/settings/resolved?project=<id>&kind=<kind> shows what a new card of that kind in that
// project would start with, and which layer each overridden value came from.
func (a *App) handleResolvedSettings(w http.ResponseWriter, r *http.Request) {
	project, err := projectParam(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	kind, err := normalizeKind(r.URL.Query().Get("kind"))
	if err != nil {
		writeErr(w, err)
		return
	}
	if project != 0 {
		if _, err := a.store.GetProject(project); err != nil {
			writeErr(w, err)
			return
		}
	}
	st, src, err := a.store.ResolveSettings(project, kind)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"project": project, "kind": kind, "settings": st, "sources": src})
}
