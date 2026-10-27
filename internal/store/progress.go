package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/sonic0002/eavesong/internal/gsc"
	"github.com/sonic0002/eavesong/internal/insight"
)

func migrateV03(db *sql.DB) error {
	for table, columns := range map[string]map[string]string{
		"projects": {"drafting": "TEXT NOT NULL DEFAULT '{}'"},
		"mentions": {"copied_at": "TEXT NOT NULL DEFAULT ''", "replied_at": "TEXT NOT NULL DEFAULT ''"},
	} {
		for col, typ := range columns {
			var n int
			if err := db.QueryRow(`SELECT count(*) FROM pragma_table_info(?) WHERE name = ?`, table, col).Scan(&n); err != nil {
				return err
			}
			if n == 0 {
				if _, err := db.Exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + col + ` ` + typ); err != nil {
					return err
				}
			}
		}
	}
	_, err := db.Exec(`
 CREATE TABLE IF NOT EXISTS search_snapshots (
 project_id INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
 day TEXT NOT NULL, fetched_at TEXT NOT NULL, data TEXT NOT NULL,
 PRIMARY KEY(project_id, day));
 CREATE TABLE IF NOT EXISTS recommendation_actions (
 project_id INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
 key TEXT NOT NULL, title TEXT NOT NULL, status TEXT NOT NULL,
 updated_at TEXT NOT NULL, checked_at TEXT NOT NULL DEFAULT '', result TEXT NOT NULL DEFAULT '',
 PRIMARY KEY(project_id, key));
 INSERT OR IGNORE INTO search_snapshots(project_id, day, fetched_at, data)
 SELECT project_id, substr(fetched_at,1,10), fetched_at, data FROM search_reports WHERE data != '' AND error = '';
 `)
	return err
}

// RecordDraftUsage keeps copying distinct from the person's confirmation of a reply.
func (s *Store) RecordDraftUsage(ctx context.Context, id int64, event string) error {
	query := ""
	switch event {
	case "copied":
		query = `UPDATE mentions SET copied_at = ? WHERE id = ? AND draft != ''`
	case "replied":
		query = `UPDATE mentions SET replied_at = ?, status = 'done' WHERE id = ?`
	case "unreplied":
		query = `UPDATE mentions SET replied_at = '', status = 'new' WHERE id = ? AND replied_at != ''`
	default:
		return errors.New("unknown draft event")
	}
	var res sql.Result
	var err error
	if event == "unreplied" {
		res, err = s.db.ExecContext(ctx, query, id)
	} else {
		res, err = s.db.ExecContext(ctx, query, now(), id)
	}
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err == nil && n == 0 {
		return sql.ErrNoRows
	}
	return err
}

// migrateBing adds the tables for Bing Webmaster Tools data. Bing has its own
// tables so Google's, saved by earlier versions, stay exactly as they were.
func migrateBing(db *sql.DB) error {
	_, err := db.Exec(`
 CREATE TABLE IF NOT EXISTS bing_reports (
 project_id INTEGER PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
 fetched_at TEXT NOT NULL, error TEXT NOT NULL DEFAULT '', data TEXT NOT NULL DEFAULT '');
 CREATE TABLE IF NOT EXISTS bing_snapshots (
 project_id INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
 day TEXT NOT NULL, fetched_at TEXT NOT NULL, data TEXT NOT NULL,
 PRIMARY KEY(project_id, day));
 `)
	return err
}

// searchTables names the report and snapshot tables of a search engine:
// "" is Google, "bing" is Bing.
func searchTables(engine string) (reports, snapshots string) {
	if engine == "bing" {
		return "bing_reports", "bing_snapshots"
	}
	return "search_reports", "search_snapshots"
}

// ofEngine limits recommendation_actions to one engine's keys; Bing's start with "bing:".
func ofEngine(engine string) string {
	if engine == "bing" {
		return ` AND key LIKE 'bing:%'`
	}
	return ` AND key NOT LIKE 'bing:%'`
}

func (s *Store) saveSearchHistory(ctx context.Context, engine string, projectID int64, data, problem string) error {
	reports, snapshots := searchTables(engine)
	if data == "" {
		return s.saveReport(ctx, reports, projectID, data, problem)
	}
	var report gsc.Report
	if err := json.Unmarshal([]byte(data), &report); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	at := now()
	if _, err = tx.ExecContext(ctx, `INSERT INTO `+reports+`(project_id, fetched_at, error, data) VALUES (?, ?, '', ?)
 ON CONFLICT(project_id) DO UPDATE SET fetched_at=excluded.fetched_at, error='', data=excluded.data`, projectID, at, data); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO `+snapshots+`(project_id, day, fetched_at, data) VALUES (?, ?, ?, ?)
 ON CONFLICT(project_id, day) DO UPDATE SET fetched_at=excluded.fetched_at, data=excluded.data`, projectID, at[:10], at, data); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE recommendation_actions SET checked_at=?, result='No longer detected' WHERE project_id=?`+ofEngine(engine), at, projectID); err != nil {
		return err
	}
	for _, rec := range insight.Analyze(report) {
		if _, err = tx.ExecContext(ctx, `UPDATE recommendation_actions SET checked_at=?, result='Still detected' WHERE project_id=? AND key=?`, at, projectID, insight.Key(rec)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

type SearchSnapshot struct {
	Day       string
	FetchedAt time.Time
	Report    gsc.Report
}

func (s *Store) SearchHistory(ctx context.Context, projectID int64) ([]SearchSnapshot, error) {
	return s.searchHistory(ctx, "", projectID)
}

// BingHistory is SearchHistory for Bing Webmaster Tools data.
func (s *Store) BingHistory(ctx context.Context, projectID int64) ([]SearchSnapshot, error) {
	return s.searchHistory(ctx, "bing", projectID)
}

func (s *Store) searchHistory(ctx context.Context, engine string, projectID int64) ([]SearchSnapshot, error) {
	_, snapshots := searchTables(engine)
	rows, err := s.db.QueryContext(ctx, `SELECT day, fetched_at, data FROM `+snapshots+` WHERE project_id=? ORDER BY day DESC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SearchSnapshot
	for rows.Next() {
		var v SearchSnapshot
		var at, data string
		if err := rows.Scan(&v.Day, &at, &data); err != nil {
			return nil, err
		}
		v.FetchedAt, _ = time.Parse(Layout, at)
		if err := json.Unmarshal([]byte(data), &v.Report); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

type RecommendationAction struct{ Key, Title, Status, UpdatedAt, CheckedAt, Result string }

func (s *Store) RecommendationActions(ctx context.Context, projectID int64) ([]RecommendationAction, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key,title,status,updated_at,checked_at,result FROM recommendation_actions WHERE project_id=? ORDER BY updated_at DESC,key`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RecommendationAction
	for rows.Next() {
		var a RecommendationAction
		if err := rows.Scan(&a.Key, &a.Title, &a.Status, &a.UpdatedAt, &a.CheckedAt, &a.Result); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
func (s *Store) SetRecommendationAction(ctx context.Context, projectID int64, key, status string) error {
	if status == "open" {
		_, err := s.db.ExecContext(ctx, `DELETE FROM recommendation_actions WHERE project_id=? AND key=?`, projectID, key)
		return err
	}
	if status != "done" && status != "dismissed" {
		return errors.New("unknown recommendation status")
	}
	engine := ""
	if strings.HasPrefix(key, "bing:") {
		engine = "bing"
	}
	reports, _ := searchTables(engine)
	saved, err := s.savedReport(ctx, reports, projectID)
	if err != nil {
		return err
	}
	var report gsc.Report
	if err := json.Unmarshal([]byte(saved.Data), &report); err != nil {
		return err
	}
	for _, rec := range insight.Analyze(report) {
		if insight.Key(rec) == key {
			_, err := s.db.ExecContext(ctx, `INSERT INTO recommendation_actions(project_id,key,title,status,updated_at) VALUES (?,?,?,?,?) ON CONFLICT(project_id,key) DO UPDATE SET title=excluded.title,status=excluded.status,updated_at=excluded.updated_at,checked_at='',result=''`, projectID, key, rec.Title, status, now())
			return err
		}
	}
	return errors.New("recommendation is no longer in the latest report")
}
