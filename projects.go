package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// A Project groups cards into one board and names the root folder new cards start with.
// It only organises cards: a card keeps its own root_folder, which drives everything else.
type Project struct {
	ID         int64          `json:"id"`
	Name       string         `json:"name"`
	RootFolder string         `json:"root_folder"`
	CreatedAt  time.Time      `json:"created_at"`
	UpdatedAt  time.Time      `json:"updated_at"`
	Counts     map[string]int `json:"counts,omitempty"` // cards per status; only in GET /api/projects
	// Settings override the global settings for this project's cards (see layers.go).
	Settings LayerSettings `json:"settings"`
}

const projectCols = `id, name, root_folder, created_at, updated_at, settings`

func scanProject(row interface{ Scan(...any) error }) (*Project, error) {
	var p Project
	var raw string
	if err := row.Scan(&p.ID, &p.Name, &p.RootFolder, &p.CreatedAt, &p.UpdatedAt, &raw); err != nil {
		return nil, err
	}
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &p.Settings); err != nil {
			return nil, fmt.Errorf("project %d settings: %w", p.ID, err)
		}
	}
	return &p, nil
}

func (s *Store) ListProjects() ([]*Project, error) {
	rows, err := s.db.Query(`SELECT ` + projectCols + ` FROM projects ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Project{}
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) GetProject(id int64) (*Project, error) {
	p, err := scanProject(s.db.QueryRow(`SELECT `+projectCols+` FROM projects WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return p, err
}

var errDupProject = errors.New("a project with that name already exists")

func (s *Store) nameTaken(name string, except int64) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM projects WHERE name=? AND id<>?`, name, except).Scan(&n)
	return n > 0, err
}

func (s *Store) CreateProject(p *Project) error {
	if taken, err := s.nameTaken(p.Name, 0); err != nil {
		return err
	} else if taken {
		return errDupProject
	}
	now := time.Now().UTC()
	p.CreatedAt, p.UpdatedAt = now, now
	res, err := s.db.Exec(`INSERT INTO projects (name, root_folder, created_at, updated_at) VALUES (?,?,?,?)`,
		p.Name, p.RootFolder, now, now)
	if err != nil {
		return err
	}
	p.ID, err = res.LastInsertId()
	return err
}

func (s *Store) SaveProject(p *Project) error {
	if taken, err := s.nameTaken(p.Name, p.ID); err != nil {
		return err
	} else if taken {
		return errDupProject
	}
	p.UpdatedAt = time.Now().UTC()
	_, err := s.db.Exec(`UPDATE projects SET name=?, root_folder=?, updated_at=? WHERE id=?`, p.Name, p.RootFolder, p.UpdatedAt, p.ID)
	return err
}

// DeleteProject refuses a project that still has cards.
func (s *Store) DeleteProject(id int64) error {
	if _, err := s.GetProject(id); err != nil {
		return err
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM cards WHERE project_id=?`, id).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return fmt.Errorf("project still has %d card(s); move or delete them first", n)
	}
	_, err := s.db.Exec(`DELETE FROM projects WHERE id=?`, id)
	return err
}

// ProjectCounts returns the number of cards per status for each project id.
func (s *Store) ProjectCounts() (map[int64]map[string]int, error) {
	rows, err := s.db.Query(`SELECT project_id, status, COUNT(*) FROM cards GROUP BY project_id, status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]map[string]int{}
	for rows.Next() {
		var id int64
		var st string
		var n int
		if err := rows.Scan(&id, &st, &n); err != nil {
			return nil, err
		}
		if out[id] == nil {
			out[id] = map[string]int{}
		}
		out[id][st] = n
	}
	return out, rows.Err()
}

// projectIDForRoot is the id ProjectForRoot would return for an existing project, 0 when there is none.
func (s *Store) projectIDForRoot(root string) int64 {
	var id int64
	s.db.QueryRow(`SELECT id FROM projects WHERE root_folder=? ORDER BY id LIMIT 1`, root).Scan(&id)
	return id
}

// ProjectForRoot returns the project with this root folder (the oldest one), creating it
// when there is none. The name is the folder's base name, made unique.
func (s *Store) ProjectForRoot(root string) (*Project, bool, error) {
	var id int64
	err := s.db.QueryRow(`SELECT id FROM projects WHERE root_folder=? ORDER BY id LIMIT 1`, root).Scan(&id)
	if err == nil {
		p, err := s.GetProject(id)
		return p, false, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, false, err
	}
	name, err := s.uniqueProjectName(root)
	if err != nil {
		return nil, false, err
	}
	p := &Project{Name: name, RootFolder: root}
	return p, true, s.CreateProject(p)
}

func (s *Store) uniqueProjectName(root string) (string, error) {
	base := filepath.Base(root)
	if base == "." || base == string(filepath.Separator) || base == "" {
		base = "project"
	}
	name := base
	for i := 2; ; i++ {
		taken, err := s.nameTaken(name, 0)
		if err != nil {
			return "", err
		}
		if !taken {
			return name, nil
		}
		name = fmt.Sprintf("%s (%d)", base, i)
	}
}

// migrateProjects gives every card without a project one, per distinct root folder.
// It is idempotent: once all cards have a project there is nothing left to do.
func (s *Store) migrateProjects() error {
	rows, err := s.db.Query(`SELECT root_folder FROM cards WHERE project_id=0 GROUP BY root_folder ORDER BY MIN(id)`)
	if err != nil {
		return err
	}
	var roots []string
	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err != nil {
			rows.Close()
			return err
		}
		roots = append(roots, r)
	}
	rows.Close()
	for _, root := range roots {
		p, _, err := s.ProjectForRoot(root)
		if err != nil {
			return err
		}
		if _, err := s.db.Exec(`UPDATE cards SET project_id=? WHERE project_id=0 AND root_folder=?`, p.ID, root); err != nil {
			return err
		}
	}
	return nil
}

// cardsInProject filters cards by project; project 0 keeps all of them.
func cardsInProject(cards []*Card, project int64) []*Card {
	if project == 0 {
		return cards
	}
	out := []*Card{}
	for _, c := range cards {
		if c.ProjectID == project {
			out = append(out, c)
		}
	}
	return out
}

// --- App ---

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

func (a *App) CreateProject(name, root string) (*Project, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("name is required")
	}
	root, err := a.projectRoot(root)
	if err != nil {
		return nil, err
	}
	p := &Project{Name: name, RootFolder: root}
	if err := a.store.CreateProject(p); err != nil {
		return nil, err
	}
	a.hub.Publish("project", p)
	return p, nil
}

// EditProject renames a project or changes its root folder. Cards keep their own root folder.
func (a *App) EditProject(id int64, name, root *string) (*Project, error) {
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
func (a *App) ListProjects() ([]*Project, error) {
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
func (a *App) projectFor(id *int64) (*Project, error) {
	if id == nil {
		return nil, nil
	}
	p, err := a.store.GetProject(*id)
	if errors.Is(err, ErrNotFound) {
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
	Name       *string        `json:"name"`
	RootFolder *string        `json:"root_folder"`
	Settings   *LayerSettings `json:"settings"` // replaces the project's settings; {} clears them
}

// SetProjectSettings replaces a project's own settings after validating them.
func (a *App) SetProjectSettings(id int64, l LayerSettings) (*Project, error) {
	if _, err := a.store.GetProject(id); err != nil {
		return nil, err
	}
	if err := l.validate("project settings"); err != nil {
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
		if err := in.Settings.validate("project settings"); err != nil {
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
