package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"maestrello/internal/model"
)

const projectCols = `id, name, root_folder, created_at, updated_at, settings`

func scanProject(row interface{ Scan(...any) error }) (*model.Project, error) {
	var p model.Project
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

func (s *Store) ListProjects() ([]*model.Project, error) {
	rows, err := s.db.Query(`SELECT ` + projectCols + ` FROM projects ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*model.Project{}
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) GetProject(id int64) (*model.Project, error) {
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

func (s *Store) CreateProject(p *model.Project) error {
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

func (s *Store) SaveProject(p *model.Project) error {
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

// ProjectIDForRoot is the id ProjectForRoot would return for an existing project, 0 when there is none.
func (s *Store) ProjectIDForRoot(root string) int64 {
	var id int64
	s.db.QueryRow(`SELECT id FROM projects WHERE root_folder=? ORDER BY id LIMIT 1`, root).Scan(&id)
	return id
}

// ProjectForRoot returns the project with this root folder (the oldest one), creating it
// when there is none. The name is the folder's base name, made unique.
func (s *Store) ProjectForRoot(root string) (*model.Project, bool, error) {
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
	p := &model.Project{Name: name, RootFolder: root}
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
