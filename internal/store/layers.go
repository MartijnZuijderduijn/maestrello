package store

import (
	"database/sql"
	"encoding/json"
	"errors"

	"maestrello/internal/model"
)

// ProjectSettings reads a project's own settings (empty when it has none).
func (s *Store) ProjectSettings(id int64) (model.LayerSettings, error) {
	var raw string
	err := s.db.QueryRow(`SELECT settings FROM projects WHERE id=?`, id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return model.LayerSettings{}, ErrNotFound
	}
	var l model.LayerSettings
	if err != nil || raw == "" {
		return l, err
	}
	return l, json.Unmarshal([]byte(raw), &l)
}

func (s *Store) SaveProjectSettings(id int64, l model.LayerSettings) error {
	raw := ""
	if !l.IsEmpty() {
		b, err := json.Marshal(l)
		if err != nil {
			return err
		}
		raw = string(b)
	}
	_, err := s.db.Exec(`UPDATE projects SET settings=? WHERE id=?`, raw, id)
	return err
}
