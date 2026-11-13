package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"maestrello/internal/model"
	_ "modernc.org/sqlite"
)

type Store struct{ db *sql.DB }

const schema = `
CREATE TABLE IF NOT EXISTS cards (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	title TEXT NOT NULL,
	description TEXT NOT NULL DEFAULT '',
	root_folder TEXT NOT NULL DEFAULT '',
	restrict_refinement INTEGER NOT NULL DEFAULT 0,
	auto_run INTEGER NOT NULL DEFAULT 0,
	skip_refinement INTEGER NOT NULL DEFAULT 0,
	deep_refinement INTEGER NOT NULL DEFAULT 0,
	title_auto INTEGER NOT NULL DEFAULT 0,
	model TEXT NOT NULL DEFAULT '',
	effort TEXT NOT NULL DEFAULT '',
	permission_mode TEXT NOT NULL DEFAULT '',
	harness TEXT NOT NULL DEFAULT 'claude',
	kind TEXT NOT NULL DEFAULT 'code',
	plan_tasks TEXT NOT NULL DEFAULT '',
	context TEXT NOT NULL DEFAULT '',
	max_turns INTEGER NOT NULL DEFAULT 0,
	max_minutes INTEGER NOT NULL DEFAULT 0,
	push_default TEXT NOT NULL DEFAULT '',
	refined_prompt TEXT NOT NULL DEFAULT '',
	refine_notes TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL DEFAULT 'todo',
	session_id TEXT NOT NULL DEFAULT '',
	summary TEXT NOT NULL DEFAULT '',
	fail_reason TEXT NOT NULL DEFAULT '',
	warning TEXT NOT NULL DEFAULT '',
	activity TEXT NOT NULL DEFAULT '',
	waiting_on INTEGER NOT NULL DEFAULT 0,
	turns_used INTEGER NOT NULL DEFAULT 0,
	cost_usd REAL NOT NULL DEFAULT 0,
	created_at TIMESTAMP NOT NULL,
	updated_at TIMESTAMP NOT NULL
);
CREATE TABLE IF NOT EXISTS projects (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	name TEXT NOT NULL UNIQUE,
	root_folder TEXT NOT NULL,
	settings TEXT NOT NULL DEFAULT '',
	created_at TIMESTAMP NOT NULL,
	updated_at TIMESTAMP NOT NULL
);
CREATE TABLE IF NOT EXISTS card_repos (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	card_id INTEGER NOT NULL REFERENCES cards(id),
	repo_path TEXT NOT NULL,
	worktree_path TEXT NOT NULL DEFAULT '',
	branch TEXT NOT NULL DEFAULT '',
	base_ref TEXT NOT NULL DEFAULT '',
	orig_status TEXT NOT NULL DEFAULT '',
	result TEXT NOT NULL DEFAULT '',
	pr_url TEXT NOT NULL DEFAULT '',
	local_since TEXT NOT NULL DEFAULT '',
	UNIQUE(card_id, repo_path)
);
CREATE TABLE IF NOT EXISTS inputs (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	card_id INTEGER NOT NULL REFERENCES cards(id),
	kind TEXT NOT NULL,
	payload TEXT NOT NULL DEFAULT '{}',
	answer TEXT NOT NULL DEFAULT '',
	answered_at TIMESTAMP,
	created_at TIMESTAMP NOT NULL
);
CREATE TABLE IF NOT EXISTS events (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	card_id INTEGER NOT NULL REFERENCES cards(id),
	ts TIMESTAMP NOT NULL,
	kind TEXT NOT NULL,
	data TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS events_card ON events(card_id, id);
CREATE TABLE IF NOT EXISTS settings (
	key TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
`

func OpenStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	// Columns added after the first release; the error on an up-to-date db is expected.
	db.Exec(`ALTER TABLE card_repos ADD COLUMN local_since TEXT NOT NULL DEFAULT ''`)
	db.Exec(`ALTER TABLE cards ADD COLUMN waiting_on INTEGER NOT NULL DEFAULT 0`)
	db.Exec(`ALTER TABLE cards ADD COLUMN auto_run INTEGER NOT NULL DEFAULT 0`)
	db.Exec(`ALTER TABLE cards ADD COLUMN harness TEXT NOT NULL DEFAULT 'claude'`)
	db.Exec(`ALTER TABLE cards ADD COLUMN skip_refinement INTEGER NOT NULL DEFAULT 0`)
	db.Exec(`ALTER TABLE cards ADD COLUMN project_id INTEGER NOT NULL DEFAULT 0`)
	db.Exec(`ALTER TABLE cards ADD COLUMN title_auto INTEGER NOT NULL DEFAULT 0`)
	db.Exec(`ALTER TABLE cards ADD COLUMN kind TEXT NOT NULL DEFAULT 'code'`)
	db.Exec(`ALTER TABLE projects ADD COLUMN settings TEXT NOT NULL DEFAULT ''`)
	db.Exec(`ALTER TABLE cards ADD COLUMN plan_tasks TEXT NOT NULL DEFAULT ''`)
	db.Exec(`ALTER TABLE cards ADD COLUMN tools TEXT NOT NULL DEFAULT ''`)
	db.Exec(`ALTER TABLE cards ADD COLUMN deep_refinement INTEGER NOT NULL DEFAULT 0`)
	db.Exec(`ALTER TABLE cards ADD COLUMN auto_model INTEGER NOT NULL DEFAULT 0`)
	db.Exec(`ALTER TABLE cards ADD COLUMN context TEXT NOT NULL DEFAULT ''`)
	st := &Store{db: db}
	if err := st.migrateProjects(); err != nil {
		return nil, fmt.Errorf("migrate projects: %w", err)
	}
	return st, nil
}

const cardCols = `id, title, description, root_folder, project_id, restrict_refinement, auto_run, skip_refinement, deep_refinement, title_auto, auto_model, model, effort,
	permission_mode, harness, kind, tools, max_turns, max_minutes, push_default, refined_prompt, refine_notes, status, session_id, summary, plan_tasks, context,
	fail_reason, warning, activity, waiting_on, turns_used, cost_usd, created_at, updated_at`

func scanCard(row interface{ Scan(...any) error }) (*model.Card, error) {
	var c model.Card
	var planTasks, tools, contextJSON string
	err := row.Scan(&c.ID, &c.Title, &c.Description, &c.RootFolder, &c.ProjectID, &c.RestrictRefinement, &c.AutoRun, &c.SkipRefinement, &c.DeepRefinement, &c.TitleAuto, &c.AutoModel, &c.Model,
		&c.Effort, &c.PermissionMode, &c.Harness, &c.Kind, &tools, &c.MaxTurns, &c.MaxMinutes, &c.PushDefault, &c.RefinedPrompt, &c.RefineNotes, &c.Status,
		&c.SessionID, &c.Summary, &planTasks, &contextJSON, &c.FailReason, &c.Warning, &c.Activity, &c.WaitingOn, &c.TurnsUsed, &c.CostUSD, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if planTasks != "" {
		json.Unmarshal([]byte(planTasks), &c.PlanTasks)
	}
	c.NormalizeRunFlags() // a stored skip + auto-model combination reads as skip only
	c.Context = []model.ContextItem{}
	if contextJSON != "" {
		json.Unmarshal([]byte(contextJSON), &c.Context)
		if c.Context == nil {
			c.Context = []model.ContextItem{} // a stored "null" still reads as empty
		}
	}
	c.Tools = []string{}
	if tools != "" {
		c.Tools = strings.Split(tools, ",")
	}
	return &c, nil
}

// contextJSON is how context is stored: "" for none.
func contextJSON(items []model.ContextItem) string {
	if len(items) == 0 {
		return ""
	}
	data, _ := json.Marshal(items)
	return string(data)
}

// planTasksJSON is how plan_tasks is stored: "" for none.
func planTasksJSON(t []model.PlanTask) string {
	if len(t) == 0 {
		return ""
	}
	data, _ := json.Marshal(t)
	return string(data)
}

func (s *Store) CreateCard(c *model.Card) error {
	now := time.Now().UTC()
	c.CreatedAt, c.UpdatedAt = now, now
	if c.Status == "" {
		c.Status = model.StatusTodo
	}
	if c.Harness == "" {
		c.Harness = model.DefaultHarness
	}
	if c.Kind == "" {
		c.Kind = model.KindCode
	}
	if c.Context == nil {
		c.Context = []model.ContextItem{}
	}
	res, err := s.db.Exec(`INSERT INTO cards (title, description, root_folder, project_id, restrict_refinement, auto_run, skip_refinement, deep_refinement, title_auto, auto_model, model,
		effort, permission_mode, harness, kind, tools, context, max_turns, max_minutes, push_default, refined_prompt, status, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		c.Title, c.Description, c.RootFolder, c.ProjectID, c.RestrictRefinement, c.AutoRun, c.SkipRefinement, c.DeepRefinement, c.TitleAuto, c.AutoModel, c.Model, c.Effort, c.PermissionMode,
		c.Harness, c.Kind, strings.Join(c.Tools, ","), contextJSON(c.Context), c.MaxTurns, c.MaxMinutes, c.PushDefault, c.RefinedPrompt, c.Status, now, now)
	if err != nil {
		return err
	}
	c.ID, err = res.LastInsertId()
	return err
}

// SaveCard writes every mutable field of c.
func (s *Store) SaveCard(c *model.Card) error {
	c.UpdatedAt = time.Now().UTC()
	if c.Harness == "" {
		c.Harness = model.DefaultHarness
	}
	if c.Kind == "" {
		c.Kind = model.KindCode
	}
	if c.Context == nil {
		c.Context = []model.ContextItem{}
	}
	_, err := s.db.Exec(`UPDATE cards SET title=?, description=?, root_folder=?, project_id=?, restrict_refinement=?, auto_run=?, skip_refinement=?, deep_refinement=?, title_auto=?, auto_model=?, model=?,
		effort=?, permission_mode=?, harness=?, kind=?, tools=?, context=?, max_turns=?, max_minutes=?, push_default=?, refined_prompt=?, refine_notes=?,
		status=?, session_id=?, summary=?, plan_tasks=?, fail_reason=?, warning=?, activity=?, waiting_on=?, turns_used=?, cost_usd=?, updated_at=?
		WHERE id=?`,
		c.Title, c.Description, c.RootFolder, c.ProjectID, c.RestrictRefinement, c.AutoRun, c.SkipRefinement, c.DeepRefinement, c.TitleAuto, c.AutoModel, c.Model, c.Effort, c.PermissionMode,
		c.Harness, c.Kind, strings.Join(c.Tools, ","), contextJSON(c.Context), c.MaxTurns, c.MaxMinutes, c.PushDefault, c.RefinedPrompt, c.RefineNotes, c.Status, c.SessionID,
		c.Summary, planTasksJSON(c.PlanTasks), c.FailReason, c.Warning, c.Activity, c.WaitingOn, c.TurnsUsed, c.CostUSD, c.UpdatedAt, c.ID)
	return err
}

var ErrNotFound = errors.New("not found")

func (s *Store) GetCard(id int64) (*model.Card, error) {
	c, err := scanCard(s.db.QueryRow(`SELECT `+cardCols+` FROM cards WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return c, err
}

func (s *Store) ListCards() ([]*model.Card, error) {
	rows, err := s.db.Query(`SELECT ` + cardCols + ` FROM cards ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cards := []*model.Card{}
	for rows.Next() {
		c, err := scanCard(rows)
		if err != nil {
			return nil, err
		}
		cards = append(cards, c)
	}
	return cards, rows.Err()
}

func (s *Store) CardsWithStatus(statuses ...string) ([]*model.Card, error) {
	all, err := s.ListCards()
	if err != nil {
		return nil, err
	}
	var out []*model.Card
	for _, c := range all {
		for _, st := range statuses {
			if c.Status == st {
				out = append(out, c)
			}
		}
	}
	return out, nil
}

func (s *Store) DeleteCard(id int64) error {
	for _, q := range []string{
		`DELETE FROM events WHERE card_id=?`, `DELETE FROM inputs WHERE card_id=?`,
		`DELETE FROM card_repos WHERE card_id=?`, `DELETE FROM cards WHERE id=?`,
	} {
		if _, err := s.db.Exec(q, id); err != nil {
			return err
		}
	}
	return nil
}

const repoCols = `id, card_id, repo_path, worktree_path, branch, base_ref, orig_status, result, pr_url, local_since`

func (s *Store) Repos(cardID int64) ([]*model.CardRepo, error) {
	rows, err := s.db.Query(`SELECT `+repoCols+` FROM card_repos WHERE card_id=? ORDER BY repo_path`, cardID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	repos := []*model.CardRepo{}
	for rows.Next() {
		var r model.CardRepo
		if err := rows.Scan(&r.ID, &r.CardID, &r.RepoPath, &r.WorktreePath, &r.Branch, &r.BaseRef, &r.OrigStatus, &r.Result, &r.PRURL, &r.LocalSince); err != nil {
			return nil, err
		}
		repos = append(repos, &r)
	}
	return repos, rows.Err()
}

// SetPlannedRepos replaces the card's repo list. Only valid before any worktree exists.
func (s *Store) SetPlannedRepos(cardID int64, paths []string) error {
	if _, err := s.db.Exec(`DELETE FROM card_repos WHERE card_id=? AND worktree_path=''`, cardID); err != nil {
		return err
	}
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if _, err := s.db.Exec(`INSERT OR IGNORE INTO card_repos (card_id, repo_path) VALUES (?,?)`, cardID, p); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) AddRepo(r *model.CardRepo) error {
	res, err := s.db.Exec(`INSERT INTO card_repos (card_id, repo_path) VALUES (?,?)
		ON CONFLICT(card_id, repo_path) DO NOTHING`, r.CardID, r.RepoPath)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 1 {
		r.ID, _ = res.LastInsertId()
		return nil
	}
	return s.db.QueryRow(`SELECT id FROM card_repos WHERE card_id=? AND repo_path=?`, r.CardID, r.RepoPath).Scan(&r.ID)
}

func (s *Store) SaveRepo(r *model.CardRepo) error {
	_, err := s.db.Exec(`UPDATE card_repos SET worktree_path=?, branch=?, base_ref=?, orig_status=?, result=?, pr_url=?, local_since=? WHERE id=?`,
		r.WorktreePath, r.Branch, r.BaseRef, r.OrigStatus, r.Result, r.PRURL, r.LocalSince, r.ID)
	return err
}

func (s *Store) CreateInput(cardID int64, kind string, payload any) (*model.Input, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	res, err := s.db.Exec(`INSERT INTO inputs (card_id, kind, payload, created_at) VALUES (?,?,?,?)`, cardID, kind, string(data), now)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return &model.Input{ID: id, CardID: cardID, Kind: kind, Payload: data, CreatedAt: now}, nil
}

func (s *Store) Inputs(cardID int64) ([]*model.Input, error) {
	rows, err := s.db.Query(`SELECT id, card_id, kind, payload, answer, answered_at, created_at FROM inputs WHERE card_id=? ORDER BY id`, cardID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	inputs := []*model.Input{}
	for rows.Next() {
		var in model.Input
		var payload string
		if err := rows.Scan(&in.ID, &in.CardID, &in.Kind, &payload, &in.Answer, &in.AnsweredAt, &in.CreatedAt); err != nil {
			return nil, err
		}
		in.Payload = json.RawMessage(payload)
		inputs = append(inputs, &in)
	}
	return inputs, rows.Err()
}

// OpenInput returns the card's unanswered input, if any.
func (s *Store) OpenInput(cardID int64) (*model.Input, error) {
	inputs, err := s.Inputs(cardID)
	if err != nil {
		return nil, err
	}
	for _, in := range inputs {
		if in.AnsweredAt == nil {
			return in, nil
		}
	}
	return nil, nil
}

func (s *Store) AnswerInput(id int64, answer string) error {
	_, err := s.db.Exec(`UPDATE inputs SET answer=?, answered_at=? WHERE id=? AND answered_at IS NULL`, answer, time.Now().UTC(), id)
	return err
}

func (s *Store) AddEvent(cardID int64, kind string, data json.RawMessage) (*model.Event, error) {
	now := time.Now().UTC()
	res, err := s.db.Exec(`INSERT INTO events (card_id, ts, kind, data) VALUES (?,?,?,?)`, cardID, now, kind, string(data))
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return &model.Event{ID: id, CardID: cardID, TS: now, Kind: kind, Data: data}, nil
}

// Events returns the last `limit` events of a card, oldest first.
func (s *Store) Events(cardID int64, limit int) ([]*model.Event, error) {
	rows, err := s.db.Query(`SELECT id, card_id, ts, kind, data FROM
		(SELECT * FROM events WHERE card_id=? ORDER BY id DESC LIMIT ?) ORDER BY id`, cardID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := []*model.Event{}
	for rows.Next() {
		var e model.Event
		var data string
		if err := rows.Scan(&e.ID, &e.CardID, &e.TS, &e.Kind, &data); err != nil {
			return nil, err
		}
		e.Data = json.RawMessage(data)
		events = append(events, &e)
	}
	return events, rows.Err()
}

func (s *Store) Settings() (model.Settings, error) {
	st := model.DefaultSettings()
	var raw string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key='settings'`).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	err = json.Unmarshal([]byte(raw), &st)
	if st.Harness == "" {
		st.Harness = model.DefaultHarness // saved before the setting existed, or without it
	}
	st.NormalizeRunFlags()
	return st, err
}

func (s *Store) SaveSettings(st model.Settings) error {
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO settings (key, value) VALUES ('settings', ?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, string(data))
	return err
}

// GetKV and SetKV keep board state other than Settings in the settings table
// (e.g. the last usage-limit snapshot). They never touch the 'settings' key.
func (s *Store) GetKV(key string) (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key=?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

func (s *Store) SetKV(key, value string) error {
	if key == "settings" {
		return errors.New("reserved key")
	}
	_, err := s.db.Exec(`INSERT INTO settings (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}
