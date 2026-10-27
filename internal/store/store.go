// Package store keeps projects, mentions and check history in SQLite.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure Go driver: no cgo, so the binary builds anywhere
)

const schema = `
CREATE TABLE IF NOT EXISTS projects (
	id           INTEGER PRIMARY KEY,
	name         TEXT NOT NULL,
	domains      TEXT NOT NULL DEFAULT '',
	keywords     TEXT NOT NULL DEFAULT '',
	competitors  TEXT NOT NULL DEFAULT '',
	own_accounts TEXT NOT NULL DEFAULT '',
	created_at   TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS mentions (
	id          INTEGER PRIMARY KEY,
	project_id  INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
	source      TEXT NOT NULL,
	external_id TEXT NOT NULL,
	url         TEXT NOT NULL,
	author      TEXT NOT NULL DEFAULT '',
	title       TEXT NOT NULL DEFAULT '',
	body        TEXT NOT NULL DEFAULT '',
	location    TEXT NOT NULL DEFAULT '',
	posted_at   TEXT NOT NULL,
	found_at    TEXT NOT NULL,
	terms       TEXT NOT NULL DEFAULT '',
	about       TEXT NOT NULL DEFAULT 'product',
	ours        INTEGER NOT NULL DEFAULT 0,
	status      TEXT NOT NULL DEFAULT 'new',
	decision    TEXT NOT NULL DEFAULT '',
	draft       TEXT NOT NULL DEFAULT '',
	reason      TEXT NOT NULL DEFAULT '',
	UNIQUE (project_id, source, external_id)
);
CREATE INDEX IF NOT EXISTS ix_mentions_inbox ON mentions (project_id, status, posted_at);
CREATE TABLE IF NOT EXISTS connections (
	id         INTEGER PRIMARY KEY,
	platform   TEXT NOT NULL,
	name       TEXT NOT NULL,
	secret     TEXT NOT NULL,
	created_at TEXT NOT NULL,
	UNIQUE (platform, name)
);
CREATE TABLE IF NOT EXISTS connection_projects (
	connection_id INTEGER NOT NULL REFERENCES connections(id) ON DELETE CASCADE,
	project_id    INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
	PRIMARY KEY (connection_id, project_id)
);
CREATE TABLE IF NOT EXISTS search_reports (
	project_id INTEGER PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
	fetched_at TEXT NOT NULL,
	error      TEXT NOT NULL DEFAULT '',
	data       TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS analytics_reports (
	project_id INTEGER PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
	fetched_at TEXT NOT NULL,
	error      TEXT NOT NULL DEFAULT '',
	data       TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS settings (
	name  TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS notification_deliveries (
    event_key TEXT NOT NULL,
    channel TEXT NOT NULL,
    sent_at TEXT NOT NULL,
    PRIMARY KEY(event_key, channel)
);
CREATE TABLE IF NOT EXISTS checks (
	id         INTEGER PRIMARY KEY,
	project_id INTEGER NOT NULL,
	source     TEXT NOT NULL,
	at         TEXT NOT NULL,
	found      INTEGER NOT NULL,
	added      INTEGER NOT NULL,
	error      TEXT NOT NULL DEFAULT ''
);
`

// Time format used for every stored timestamp (UTC, sorts as text).
const Layout = "2006-01-02 15:04:05"

type Store struct{ db *sql.DB }

type DraftSettings struct {
	Description string
	Tone        string
	ReplyLength string
	AvoidWords  string
	Examples    string
	ReadLinks   bool
}

type Project struct {
	DraftSettings
	ID          int64
	Name        string
	Domains     []string
	Keywords    []string
	Competitors []string
	OwnAccounts []string
}

type Mention struct {
	ID         int64
	ProjectID  int64
	Source     string
	ExternalID string
	URL        string
	Author     string
	Title      string
	Body       string
	Location   string
	PostedAt   time.Time
	FoundAt    time.Time
	Terms      []string
	About      string // "product" or "competitor"
	Ours       bool
	Status     string // "new", "done", "ignored"
	Decision   string // "", "suggest", "attention", "skip"
	Draft      string
	Reason     string
	// What the post replies to or sits under, when known: the text of the
	// owner's post that was replied to or quoted, and a link to it.
	ContextText string
	ContextURL  string
	ContextOwn  bool
	CopiedAt    string
	RepliedAt   string
}

type Check struct {
	Source string
	At     time.Time
	Found  int
	Added  int
	Error  string
}

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // one writer; SQLite serialises anyway
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	if err := addColumns(db); err != nil {
		db.Close()
		return nil, err
	}
	if err := migrateV03(db); err != nil {
		db.Close()
		return nil, err
	}
	if err := migrateBing(db); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// addColumns brings databases made by older versions up to date.
func addColumns(db *sql.DB) error {
	have := map[string]bool{}
	rows, err := db.Query(`SELECT name FROM pragma_table_info('mentions')`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		have[name] = true
	}
	rows.Close()
	for col, kind := range map[string]string{"context_text": "TEXT NOT NULL DEFAULT ''", "context_url": "TEXT NOT NULL DEFAULT ''", "context_own": "INTEGER NOT NULL DEFAULT 0"} {
		if !have[col] {
			if _, err := db.Exec(`ALTER TABLE mentions ADD COLUMN ` + col + ` ` + kind); err != nil {
				return err
			}
		}
	}
	return nil
}

func split(s string) []string {
	var out []string
	for _, part := range strings.Split(s, "\n") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func join(values []string) string {
	var clean []string
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			clean = append(clean, v)
		}
	}
	return strings.Join(clean, "\n")
}

func now() string { return time.Now().UTC().Format(Layout) }

func (s *Store) Projects(ctx context.Context) ([]Project, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, domains, keywords, competitors, own_accounts, drafting FROM projects ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Project
	for rows.Next() {
		var p Project
		var d, k, c, o, drafting string
		if err := rows.Scan(&p.ID, &p.Name, &d, &k, &c, &o, &drafting); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(drafting), &p.DraftSettings); err != nil {
			return nil, err
		}
		p.Domains, p.Keywords, p.Competitors, p.OwnAccounts = split(d), split(k), split(c), split(o)
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) Project(ctx context.Context, id int64) (Project, error) {
	projects, err := s.Projects(ctx)
	if err != nil {
		return Project{}, err
	}
	for _, p := range projects {
		if p.ID == id {
			return p, nil
		}
	}
	return Project{}, sql.ErrNoRows
}

// SaveProject inserts a project (ID 0) or updates it, and returns its ID.
func (s *Store) SaveProject(ctx context.Context, p Project) (int64, error) {
	if strings.TrimSpace(p.Name) == "" {
		return 0, errors.New("a project needs a name")
	}
	drafting, err := json.Marshal(p.DraftSettings)
	if err != nil {
		return 0, err
	}
	if p.ID == 0 {
		res, err := s.db.ExecContext(ctx, `INSERT INTO projects (name, domains, keywords, competitors, own_accounts, created_at, drafting) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			p.Name, join(p.Domains), join(p.Keywords), join(p.Competitors), join(p.OwnAccounts), now(), string(drafting))
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	}
	_, err = s.db.ExecContext(ctx, `UPDATE projects SET name = ?, domains = ?, keywords = ?, competitors = ?, own_accounts = ?, drafting = ? WHERE id = ?`,
		p.Name, join(p.Domains), join(p.Keywords), join(p.Competitors), join(p.OwnAccounts), string(drafting), p.ID)
	return p.ID, err
}

// AddMention stores a mention unless it is already known, and reports whether it was new.
func (s *Store) AddMention(ctx context.Context, m Mention) (bool, error) {
	res, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO mentions
		(project_id, source, external_id, url, author, title, body, location, posted_at, found_at, terms, about, ours, status, context_text, context_url, context_own)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ProjectID, m.Source, m.ExternalID, m.URL, m.Author, m.Title, m.Body, m.Location,
		m.PostedAt.UTC().Format(Layout), now(), join(m.Terms), m.About, m.Ours, firstNonEmpty(m.Status, "new"), m.ContextText, m.ContextURL, m.ContextOwn)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err == nil && n == 0 && (m.ContextText != "" || m.ContextURL != "") {
		// Already known: fill in context that older versions did not collect.
		_, err = s.db.ExecContext(ctx, `UPDATE mentions SET context_text = ?, context_url = ?, context_own = ?
			WHERE project_id = ? AND source = ? AND external_id = ? AND (context_url = '' OR context_own != ?)`,
			m.ContextText, m.ContextURL, m.ContextOwn, m.ProjectID, m.Source, m.ExternalID, m.ContextOwn)
	}
	return n == 1, err
}

const mentionColumns = `id, project_id, source, external_id, url, author, title, body, location, posted_at, found_at, terms, about, ours, status, decision, draft, reason, context_text, context_url, context_own, copied_at, replied_at`

func scanMention(row interface{ Scan(...any) error }) (Mention, error) {
	var m Mention
	var posted, found, terms string
	err := row.Scan(&m.ID, &m.ProjectID, &m.Source, &m.ExternalID, &m.URL, &m.Author, &m.Title, &m.Body, &m.Location,
		&posted, &found, &terms, &m.About, &m.Ours, &m.Status, &m.Decision, &m.Draft, &m.Reason, &m.ContextText, &m.ContextURL, &m.ContextOwn, &m.CopiedAt, &m.RepliedAt)
	m.PostedAt, _ = time.Parse(Layout, posted)
	m.FoundAt, _ = time.Parse(Layout, found)
	m.Terms = split(terms)
	return m, err
}

// Filter selects mentions for the inbox.
type Filter struct {
	Status      string // "" means every status
	Source      string
	About       string
	IncludeOurs bool
	Limit       int
}

func (s *Store) Mentions(ctx context.Context, projectID int64, f Filter) ([]Mention, error) {
	q := `SELECT ` + mentionColumns + ` FROM mentions WHERE project_id = ?`
	args := []any{projectID}
	if f.Status != "" {
		q += ` AND status = ?`
		args = append(args, f.Status)
	}
	if f.Source != "" {
		q += ` AND source = ?`
		args = append(args, f.Source)
	}
	if f.About != "" {
		q += ` AND about = ?`
		args = append(args, f.About)
	}
	if !f.IncludeOurs {
		q += ` AND ours = 0`
	}
	q += ` ORDER BY posted_at DESC LIMIT ?`
	if f.Limit <= 0 {
		f.Limit = 200
	}
	args = append(args, f.Limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Mention
	for rows.Next() {
		m, err := scanMention(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) Mention(ctx context.Context, id int64) (Mention, error) {
	return scanMention(s.db.QueryRowContext(ctx, `SELECT `+mentionColumns+` FROM mentions WHERE id = ?`, id))
}

func (s *Store) SetStatus(ctx context.Context, id int64, status string) error {
	switch status {
	case "new", "done", "ignored":
	default:
		return errors.New("unknown status")
	}
	_, err := s.db.ExecContext(ctx, `UPDATE mentions SET status = ? WHERE id = ?`, status, id)
	return err
}

func (s *Store) SetDraft(ctx context.Context, id int64, decision, draft, reason string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE mentions SET decision = ?, draft = ?, reason = ? WHERE id = ?`, decision, draft, reason, id)
	return err
}

// Undrafted lists new mentions by other people that have no reply decision yet, newest first.
func (s *Store) Undrafted(ctx context.Context, limit int) ([]Mention, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+mentionColumns+` FROM mentions
		WHERE decision = '' AND status = 'new' AND ours = 0 ORDER BY posted_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Mention
	for rows.Next() {
		m, err := scanMention(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) RecordCheck(ctx context.Context, projectID int64, c Check) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO checks (project_id, source, at, found, added, error) VALUES (?, ?, ?, ?, ?, ?)`,
		projectID, c.Source, c.At.UTC().Format(Layout), c.Found, c.Added, c.Error)
	return err
}

// LastChecks returns the latest check of each source for a project.
func (s *Store) LastChecks(ctx context.Context, projectID int64) ([]Check, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT source, at, found, added, error FROM checks c
		WHERE project_id = ? AND id = (SELECT MAX(id) FROM checks WHERE project_id = c.project_id AND source = c.source)
		ORDER BY source`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Check
	for rows.Next() {
		var c Check
		var at string
		if err := rows.Scan(&c.Source, &at, &c.Found, &c.Added, &c.Error); err != nil {
			return nil, err
		}
		c.At, _ = time.Parse(Layout, at)
		out = append(out, c)
	}
	return out, rows.Err()
}

// Count is one row of a report breakdown.
type Count struct {
	Label string
	N     int
}

// Breakdown counts mentions by other people posted since a time, grouped by a column.
func (s *Store) Breakdown(ctx context.Context, projectID int64, since time.Time, column string) ([]Count, error) {
	switch column {
	case "source", "about", "location":
	default:
		return nil, errors.New("unknown column")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+column+`, COUNT(*) FROM mentions
		WHERE project_id = ? AND ours = 0 AND posted_at >= ? GROUP BY 1 ORDER BY 2 DESC`, projectID, since.UTC().Format(Layout))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Count
	for rows.Next() {
		var c Count
		if err := rows.Scan(&c.Label, &c.N); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// StatusCounts counts a project's mentions per status.
func (s *Store) StatusCounts(ctx context.Context, projectID int64, includeOurs bool) (map[string]int, error) {
	q := `SELECT status, COUNT(*) FROM mentions WHERE project_id = ?`
	if !includeOurs {
		q += ` AND ours = 0`
	}
	rows, err := s.db.QueryContext(ctx, q+` GROUP BY status`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var status string
		var n int
		if err := rows.Scan(&status, &n); err != nil {
			return nil, err
		}
		out[status] = n
		out[""] += n
	}
	return out, rows.Err()
}

// SourceCounts counts a project's mentions per source, for one status ("" for all).
func (s *Store) SourceCounts(ctx context.Context, projectID int64, status string, includeOurs bool) ([]Count, error) {
	q := `SELECT source, COUNT(*) FROM mentions WHERE project_id = ?`
	args := []any{projectID}
	if status != "" {
		q += ` AND status = ?`
		args = append(args, status)
	}
	if !includeOurs {
		q += ` AND ours = 0`
	}
	rows, err := s.db.QueryContext(ctx, q+` GROUP BY source ORDER BY 2 DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Count
	for rows.Next() {
		var c Count
		if err := rows.Scan(&c.Label, &c.N); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Daily counts mentions by other people per day for the last n days, oldest
// first, with zero for days without any.
func (s *Store) Daily(ctx context.Context, projectID int64, days int) ([]Count, error) {
	start := time.Now().UTC().AddDate(0, 0, -(days - 1))
	rows, err := s.db.QueryContext(ctx, `SELECT substr(posted_at, 1, 10), COUNT(*) FROM mentions
		WHERE project_id = ? AND ours = 0 AND posted_at >= ? GROUP BY 1`, projectID, start.Format("2006-01-02"))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byDay := map[string]int{}
	for rows.Next() {
		var day string
		var n int
		if err := rows.Scan(&day, &n); err != nil {
			return nil, err
		}
		byDay[day] = n
	}
	out := make([]Count, days)
	for i := range out {
		day := start.AddDate(0, 0, i).Format("2006-01-02")
		out[i] = Count{Label: day, N: byDay[day]}
	}
	return out, rows.Err()
}

// Settings returns every stored installation setting.
func (s *Store) Settings(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name, value FROM settings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var name, value string
		if err := rows.Scan(&name, &value); err != nil {
			return nil, err
		}
		out[name] = value
	}
	return out, rows.Err()
}

// SaveSettings stores installation settings in one transaction; an empty value removes the setting.
func (s *Store) SaveSettings(ctx context.Context, values map[string]string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for name, value := range values {
		if value == "" {
			_, err = tx.ExecContext(ctx, `DELETE FROM settings WHERE name = ?`, name)
		} else {
			_, err = tx.ExecContext(ctx, `INSERT INTO settings (name, value) VALUES (?, ?) ON CONFLICT(name) DO UPDATE SET value = excluded.value`, name, value)
		}
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

// RefreshOurs re-marks which of a project's mentions are by its own accounts,
// after the list of accounts changes.
func (s *Store) RefreshOurs(ctx context.Context, p Project) error {
	rows, err := s.db.QueryContext(ctx, `SELECT id, source, author, ours FROM mentions WHERE project_id = ?`, p.ID)
	if err != nil {
		return err
	}
	type change struct {
		id   int64
		ours bool
	}
	var changes []change
	for rows.Next() {
		var id int64
		var source, author string
		var ours bool
		if err := rows.Scan(&id, &source, &author, &ours); err != nil {
			rows.Close()
			return err
		}
		if now := p.IsOwn(source, author); now != ours {
			changes = append(changes, change{id, now})
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, c := range changes {
		if _, err := s.db.ExecContext(ctx, `UPDATE mentions SET ours = ? WHERE id = ?`, c.ours, c.id); err != nil {
			return err
		}
	}
	return nil
}

// Connection is one account Eavesong uses to read a source: a Bluesky handle
// and app password, a Reddit inbox feed, or a GitHub token. Secret is stored
// encrypted; the store never decrypts it.
type Connection struct {
	ID       int64
	Platform string
	Name     string
	Secret   string
	Created  time.Time
}

func (s *Store) Connections(ctx context.Context) ([]Connection, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, platform, name, secret, created_at FROM connections ORDER BY platform, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Connection
	for rows.Next() {
		var c Connection
		var created string
		if err := rows.Scan(&c.ID, &c.Platform, &c.Name, &c.Secret, &created); err != nil {
			return nil, err
		}
		c.Created, _ = time.Parse(Layout, created)
		out = append(out, c)
	}
	return out, rows.Err()
}

// SaveConnection adds an account, or replaces the secret of the same account.
func (s *Store) SaveConnection(ctx context.Context, c Connection) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO connections (platform, name, secret, created_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(platform, name) DO UPDATE SET secret = excluded.secret`, c.Platform, c.Name, c.Secret, now())
	return err
}

func (s *Store) DeleteConnection(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM connections WHERE id = ?`, id)
	return err
}

// SearchReport is the latest Search Console data saved for a project, as JSON.
type SearchReport struct {
	FetchedAt time.Time
	Error     string
	Data      string
}

func (s *Store) SearchReport(ctx context.Context, projectID int64) (SearchReport, error) {
	return s.savedReport(ctx, "search_reports", projectID)
}

// AnalyticsReport is the latest Google Analytics data saved for a project.
func (s *Store) AnalyticsReport(ctx context.Context, projectID int64) (SearchReport, error) {
	return s.savedReport(ctx, "analytics_reports", projectID)
}

// SaveAnalyticsReport stores fresh Analytics data, or only the error.
func (s *Store) SaveAnalyticsReport(ctx context.Context, projectID int64, data, problem string) error {
	return s.saveReport(ctx, "analytics_reports", projectID, data, problem)
}

func (s *Store) savedReport(ctx context.Context, table string, projectID int64) (SearchReport, error) {
	var r SearchReport
	var at string
	err := s.db.QueryRowContext(ctx, `SELECT fetched_at, error, data FROM `+table+` WHERE project_id = ?`, projectID).Scan(&at, &r.Error, &r.Data)
	if errors.Is(err, sql.ErrNoRows) {
		return r, nil
	}
	r.FetchedAt, _ = time.Parse(Layout, at)
	return r, err
}

// SaveSearchReport stores fresh data, or only the error when data is empty so
// the last good report stays visible.
func (s *Store) SaveSearchReport(ctx context.Context, projectID int64, data, problem string) error {
	return s.saveSearchHistory(ctx, "", projectID, data, problem)
}

// BingReport is the latest Bing Webmaster Tools data saved for a project.
func (s *Store) BingReport(ctx context.Context, projectID int64) (SearchReport, error) {
	return s.savedReport(ctx, "bing_reports", projectID)
}

// SaveBingReport stores fresh Bing data, or only the error.
func (s *Store) SaveBingReport(ctx context.Context, projectID int64, data, problem string) error {
	return s.saveSearchHistory(ctx, "bing", projectID, data, problem)
}

// saveReport writes one of the report tables, whose names are constants.
func (s *Store) saveReport(ctx context.Context, table string, projectID int64, data, problem string) error {
	if data == "" {
		_, err := s.db.ExecContext(ctx, `INSERT INTO `+table+` (project_id, fetched_at, error) VALUES (?, ?, ?)
			ON CONFLICT(project_id) DO UPDATE SET fetched_at = excluded.fetched_at, error = excluded.error`, projectID, now(), problem)
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO `+table+` (project_id, fetched_at, error, data) VALUES (?, ?, '', ?)
		ON CONFLICT(project_id) DO UPDATE SET fetched_at = excluded.fetched_at, error = '', data = excluded.data`, projectID, now(), data)
	return err
}

// ConnectionProjects returns, for each account limited to certain projects,
// those projects. Accounts without an entry are used for every project.
func (s *Store) ConnectionProjects(ctx context.Context) (map[int64][]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT connection_id, project_id FROM connection_projects ORDER BY connection_id, project_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]int64{}
	for rows.Next() {
		var c, p int64
		if err := rows.Scan(&c, &p); err != nil {
			return nil, err
		}
		out[c] = append(out[c], p)
	}
	return out, rows.Err()
}

// SetConnectionProjects limits an account to some projects; no projects
// means every project.
func (s *Store) SetConnectionProjects(ctx context.Context, connectionID int64, projectIDs []int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM connection_projects WHERE connection_id = ?`, connectionID); err != nil {
		return err
	}
	for _, p := range projectIDs {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO connection_projects (connection_id, project_id) VALUES (?, ?)`, connectionID, p); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Activity is what the inbox polls: the newest mention and the latest check.
type Activity struct {
	LatestFound string // found_at of the newest mention by others
	LastCheck   string // time of the latest source check
	NewSince    int    // mentions by others found after the time asked about
}

func (s *Store) Activity(ctx context.Context, projectID int64, since string) (Activity, error) {
	var a Activity
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(found_at), ''),
		COALESCE(SUM(CASE WHEN found_at > ? AND ? != '' THEN 1 ELSE 0 END), 0)
		FROM mentions WHERE project_id = ? AND ours = 0`, since, since, projectID).Scan(&a.LatestFound, &a.NewSince)
	if err != nil {
		return a, err
	}
	err = s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(at), '') FROM checks WHERE project_id = ?`, projectID).Scan(&a.LastCheck)
	return a, err
}

// MentionsBySource counts mentions by others found from a date (YYYY-MM-DD)
// through the end of another, per source.
func (s *Store) MentionsBySource(ctx context.Context, projectID int64, start, end string) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT source, COUNT(*) FROM mentions
		WHERE project_id = ? AND ours = 0 AND substr(found_at, 1, 10) BETWEEN ? AND ? GROUP BY source`, projectID, start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var src string
		var n int
		if err := rows.Scan(&src, &n); err != nil {
			return nil, err
		}
		out[src] = n
	}
	return out, rows.Err()
}
