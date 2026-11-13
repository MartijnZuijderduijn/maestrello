package board

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"maestrello/internal/model"
	"maestrello/internal/settings"
	"maestrello/internal/store"
)

// cardsInProject filters cards by project; project 0 keeps all of them.
func cardsInProject(cards []*model.Card, project int64) []*model.Card {
	if project == 0 {
		return cards
	}
	out := []*model.Card{}
	for _, c := range cards {
		if c.ProjectID == project {
			out = append(out, c)
		}
	}
	return out
}

// projectRoot validates a project's root folder: absolute, cleaned, inside the sandbox.
func (a *App) projectRoot(root string) (string, error) {
	root = strings.TrimSpace(root)
	if !filepath.IsAbs(root) {
		return "", errors.New("root folder must be an absolute path")
	}
	root = filepath.Clean(root)
	if err := a.guard.Check(root); err != nil {
		return "", err
	}
	return root, nil
}

func (a *App) CreateProject(name, root string) (*model.Project, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("name is required")
	}
	root, err := a.projectRoot(root)
	if err != nil {
		return nil, err
	}
	p := &model.Project{Name: name, RootFolder: root}
	if err := a.store.CreateProject(p); err != nil {
		return nil, err
	}
	a.hub.Publish("project", p)
	return p, nil
}

// EditProject renames a project or changes its root folder. Cards keep their own root folder.
func (a *App) EditProject(id int64, name, root *string) (*model.Project, error) {
	p, err := a.store.GetProject(id)
	if err != nil {
		return nil, err
	}
	if name != nil {
		if p.Name = strings.TrimSpace(*name); p.Name == "" {
			return nil, errors.New("name is required")
		}
	}
	if root != nil {
		if p.RootFolder, err = a.projectRoot(*root); err != nil {
			return nil, err
		}
	}
	if err := a.store.SaveProject(p); err != nil {
		return nil, err
	}
	a.hub.Publish("project", p)
	return p, nil
}

func (a *App) DeleteProject(id int64) error {
	if err := a.store.DeleteProject(id); err != nil {
		return err
	}
	a.hub.Publish("project_deleted", map[string]int64{"id": id})
	return nil
}

// ListProjects returns the projects with their per-status card counts.
func (a *App) ListProjects() ([]*model.Project, error) {
	ps, err := a.store.ListProjects()
	if err != nil {
		return nil, err
	}
	counts, err := a.store.ProjectCounts()
	if err != nil {
		return nil, err
	}
	for _, p := range ps {
		p.Counts = counts[p.ID]
		if p.Counts == nil {
			p.Counts = map[string]int{}
		}
	}
	return ps, nil
}

// projectFor resolves the project a card input names; an unknown one is an error.
func (a *App) projectFor(id *int64) (*model.Project, error) {
	if id == nil {
		return nil, nil
	}
	p, err := a.store.GetProject(*id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Errorf("unknown project %d", *id)
	}
	return p, err
}

// projectParam reads the optional ?project=<id> query parameter (0 when absent).
func projectParam(r *http.Request) (int64, error) {
	v := r.URL.Query().Get("project")
	if v == "" {
		return 0, nil
	}
	id, err := strconv.ParseInt(v, 10, 64)
	if err != nil || id <= 0 {
		return 0, errors.New("invalid project")
	}
	return id, nil
}

func (a *App) handleListProjects(w http.ResponseWriter, r *http.Request) {
	ps, err := a.ListProjects()
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, ps)
}

type projectInput struct {
	Name       *string              `json:"name"`
	RootFolder *string              `json:"root_folder"`
	Settings   *model.LayerSettings `json:"settings"` // replaces the project's settings; {} clears them
}

// SetProjectSettings replaces a project's own settings after validating them.
func (a *App) SetProjectSettings(id int64, l model.LayerSettings) (*model.Project, error) {
	if _, err := a.store.GetProject(id); err != nil {
		return nil, err
	}
	if err := settings.ValidateLayer(l, "project settings"); err != nil {
		return nil, err
	}
	if err := a.store.SaveProjectSettings(id, l); err != nil {
		return nil, err
	}
	p, err := a.store.GetProject(id)
	if err == nil {
		a.hub.Publish("project", p)
	}
	return p, err
}

func (a *App) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	var in projectInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, err)
		return
	}
	name, root := "", ""
	if in.Name != nil {
		name = *in.Name
	}
	if in.RootFolder != nil {
		root = *in.RootFolder
	}
	p, err := a.CreateProject(name, root)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 201, p)
}

func (a *App) handlePatchProject(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var in projectInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, err)
		return
	}
	if in.Settings != nil { // validated first, so a refused PATCH changes nothing
		if err := settings.ValidateLayer(*in.Settings, "project settings"); err != nil {
			writeErr(w, err)
			return
		}
	}
	p, err := a.EditProject(id, in.Name, in.RootFolder)
	if err != nil {
		writeErr(w, err)
		return
	}
	if in.Settings != nil {
		if p, err = a.SetProjectSettings(id, *in.Settings); err != nil {
			writeErr(w, err)
			return
		}
	}
	writeJSON(w, 200, p)
}

func (a *App) handleDeleteProject(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := a.DeleteProject(id); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(204)
}
