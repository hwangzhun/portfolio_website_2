package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct {
	db      *sql.DB
	dataDir string
}
type Document struct {
	Content   object `json:"content"`
	UpdatedAt string `json:"updatedAt"`
	Hash      string `json:"hash"`
}
type Revision struct {
	ID                                             int64 `json:"id"`
	Hash, ShortHash                                string
	MatchesPublished, MatchesDraft, SameAsPrevious bool
	CreatedAt, CreatedBy, SiteName, HeroTitle      string
	Counts                                         object
}
type Media struct {
	ID           int64  `json:"id"`
	OriginalName string `json:"originalName"`
	Storage      string `json:"storage"`
	OriginalURL  string `json:"originalUrl"`
	OptimizedURL string `json:"optimizedUrl"`
	OriginalKey  string `json:"originalKey,omitempty"`
	OptimizedKey string `json:"optimizedKey,omitempty"`
	MimeType     string `json:"mimeType"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
	Size         int64  `json:"size"`
	CreatedAt    string `json:"createdAt,omitempty"`
}
type AdminLog struct {
	ID            int64  `json:"id"`
	Username      string `json:"username"`
	Action        string `json:"action"`
	TargetType    string `json:"targetType"`
	TargetID      string `json:"targetId"`
	Detail        string `json:"detail"`
	IPAddress     string `json:"ipAddress"`
	CreatedAt     string `json:"createdAt"`
	RequestID     string `json:"requestId,omitempty"`
	Method        string `json:"method,omitempty"`
	Path          string `json:"path,omitempty"`
	StatusCode    int    `json:"statusCode,omitempty"`
	DurationMS    int64  `json:"durationMs,omitempty"`
	ResponseBytes int64  `json:"responseBytes,omitempty"`
	UserAgent     string `json:"userAgent,omitempty"`
	Referer       string `json:"referer,omitempty"`
}

const schema = `
CREATE TABLE IF NOT EXISTS site_content (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS videos (id INTEGER PRIMARY KEY AUTOINCREMENT,title TEXT NOT NULL,description TEXT DEFAULT '',cover_url TEXT DEFAULT '',file_id TEXT DEFAULT '',playback_url TEXT DEFAULT '',sort_order INTEGER DEFAULT 0,published INTEGER DEFAULT 1,created_at TEXT DEFAULT CURRENT_TIMESTAMP,updated_at TEXT DEFAULT CURRENT_TIMESTAMP);
CREATE TABLE IF NOT EXISTS admin_users (id INTEGER PRIMARY KEY AUTOINCREMENT,username TEXT UNIQUE NOT NULL,password_hash TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS site_documents (name TEXT PRIMARY KEY CHECK(name IN ('draft','published')),content TEXT NOT NULL,updated_at TEXT DEFAULT CURRENT_TIMESTAMP);
CREATE TABLE IF NOT EXISTS content_revisions (id INTEGER PRIMARY KEY AUTOINCREMENT,content TEXT NOT NULL,created_at TEXT DEFAULT CURRENT_TIMESTAMP,created_by TEXT DEFAULT 'admin');
CREATE TABLE IF NOT EXISTS admin_sessions (token_hash TEXT PRIMARY KEY,username TEXT NOT NULL,expires_at INTEGER NOT NULL,created_at TEXT DEFAULT CURRENT_TIMESTAMP);
CREATE TABLE IF NOT EXISTS media_assets (id INTEGER PRIMARY KEY AUTOINCREMENT,original_name TEXT NOT NULL,storage TEXT NOT NULL,original_url TEXT NOT NULL,optimized_url TEXT NOT NULL,original_key TEXT DEFAULT '',optimized_key TEXT DEFAULT '',mime_type TEXT NOT NULL,width INTEGER DEFAULT 0,height INTEGER DEFAULT 0,size INTEGER DEFAULT 0,created_at TEXT DEFAULT CURRENT_TIMESTAMP);
CREATE TABLE IF NOT EXISTS cms_settings (key TEXT PRIMARY KEY,value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS admin_logs (id INTEGER PRIMARY KEY AUTOINCREMENT,username TEXT NOT NULL DEFAULT 'system',action TEXT NOT NULL,target_type TEXT DEFAULT '',target_id TEXT DEFAULT '',detail TEXT DEFAULT '',ip_address TEXT DEFAULT '',request_id TEXT DEFAULT '',method TEXT DEFAULT '',path TEXT DEFAULT '',status_code INTEGER DEFAULT 0,duration_ms INTEGER DEFAULT 0,response_bytes INTEGER DEFAULT 0,user_agent TEXT DEFAULT '',referer TEXT DEFAULT '',created_at TEXT DEFAULT CURRENT_TIMESTAMP);`

func openStore(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", filepath.Join(dataDir, "portfolio.sqlite"))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	s := &Store{db: db, dataDir: dataDir}
	if err = s.init(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) init() error {
	for _, q := range []string{"PRAGMA journal_mode=WAL", "PRAGMA foreign_keys=ON", "PRAGMA busy_timeout=5000", schema} {
		if _, err := s.db.Exec(q); err != nil {
			return err
		}
	}
	rows, err := s.db.Query("PRAGMA table_info(content_revisions)")
	if err != nil {
		return err
	}
	hasHash := false
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var def any
		if rows.Scan(&cid, &name, &typ, &notnull, &def, &pk) == nil && name == "content_hash" {
			hasHash = true
		}
	}
	rows.Close()
	if !hasHash {
		if _, err = s.db.Exec("ALTER TABLE content_revisions ADD COLUMN content_hash TEXT NOT NULL DEFAULT ''"); err != nil {
			return err
		}
	}
	logColumns := map[string]string{
		"request_id": "TEXT DEFAULT ''", "method": "TEXT DEFAULT ''", "path": "TEXT DEFAULT ''",
		"status_code": "INTEGER DEFAULT 0", "duration_ms": "INTEGER DEFAULT 0", "response_bytes": "INTEGER DEFAULT 0",
		"user_agent": "TEXT DEFAULT ''", "referer": "TEXT DEFAULT ''",
	}
	logRows, logErr := s.db.Query("PRAGMA table_info(admin_logs)")
	if logErr != nil {
		return logErr
	}
	existingLogColumns := map[string]bool{}
	for logRows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var def any
		if logRows.Scan(&cid, &name, &typ, &notnull, &def, &pk) == nil {
			existingLogColumns[name] = true
		}
	}
	logRows.Close()
	for name, definition := range logColumns {
		if !existingLogColumns[name] {
			if _, err = s.db.Exec("ALTER TABLE admin_logs ADD COLUMN " + name + " " + definition); err != nil {
				return err
			}
		}
	}
	if _, err = s.db.Exec("CREATE INDEX IF NOT EXISTS idx_content_revisions_hash ON content_revisions(content_hash); CREATE INDEX IF NOT EXISTS idx_admin_logs_created_at ON admin_logs(created_at DESC, id DESC)"); err != nil {
		return err
	}
	initial := cloneObject(defaultContent)
	var legacy string
	if err = s.db.QueryRow("SELECT value FROM site_content WHERE key='main'").Scan(&legacy); err == nil {
		initial = normalizeContent(parseObject([]byte(legacy)))
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	videoRows, err := s.db.Query("SELECT title,description,cover_url,file_id,published FROM videos ORDER BY sort_order,id")
	if err != nil {
		return err
	}
	known := map[string]object{}
	for _, raw := range asArray(initial["projects"]) {
		item := asObject(raw)
		known[stringValue(item["title"])] = item
	}
	idx := 0
	for videoRows.Next() {
		var title, desc, cover, fileID string
		var published int
		if err = videoRows.Scan(&title, &desc, &cover, &fileID, &published); err != nil {
			return err
		}
		if existing := known[title]; existing != nil {
			if stringValue(existing["vodFileId"]) == "" && fileID != "" {
				existing["vodFileId"] = fileID
			}
			continue
		}
		idx++
		initial["projects"] = append(asArray(initial["projects"]), object{"id": fmt.Sprintf("legacy-video-%d", idx), "type": "video", "title": title, "category": "视频", "year": "", "meta": "", "description": desc, "coverUrl": cover, "externalUrl": "", "vodFileId": fileID, "featured": false, "published": published != 0})
	}
	videoRows.Close()
	b, _ := json.Marshal(initial)
	if _, err = s.db.Exec("INSERT OR IGNORE INTO site_documents (name,content) VALUES (?,?),(?,?)", "published", string(b), "draft", string(b)); err != nil {
		return err
	}
	docs, err := s.db.Query("SELECT name,content FROM site_documents")
	if err != nil {
		return err
	}
	var updates [][2]string
	for docs.Next() {
		var name, raw string
		if err = docs.Scan(&name, &raw); err != nil {
			return err
		}
		normalized, _ := json.Marshal(normalizeContent(parseObject([]byte(raw))))
		updates = append(updates, [2]string{name, string(normalized)})
	}
	docs.Close()
	for _, u := range updates {
		if _, err = s.db.Exec("UPDATE site_documents SET content=? WHERE name=?", u[1], u[0]); err != nil {
			return err
		}
	}
	revs, err := s.db.Query("SELECT id,content FROM content_revisions WHERE content_hash='' ")
	if err != nil {
		return err
	}
	type rh struct {
		id   int64
		hash string
	}
	var hashes []rh
	for revs.Next() {
		var id int64
		var raw string
		if err = revs.Scan(&id, &raw); err != nil {
			return err
		}
		hashes = append(hashes, rh{id, contentHash(parseObject([]byte(raw)))})
	}
	revs.Close()
	for _, h := range hashes {
		if _, err = s.db.Exec("UPDATE content_revisions SET content_hash=? WHERE id=?", h.hash, h.id); err != nil {
			return err
		}
	}
	_, err = s.db.Exec("INSERT OR IGNORE INTO cms_settings (key,value) VALUES ('storage_provider','local')")
	return err
}

type queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func getDocumentQ(ctx context.Context, q queryer, name string) (Document, error) {
	var raw, updated string
	err := q.QueryRowContext(ctx, "SELECT content,updated_at FROM site_documents WHERE name=?", name).Scan(&raw, &updated)
	if err != nil {
		return Document{}, err
	}
	content := normalizeContent(parseObject([]byte(raw)))
	return Document{content, updated, contentHash(content)}, nil
}
func (s *Store) GetDocument(name string) (Document, error) {
	return getDocumentQ(context.Background(), s.db, name)
}
func (s *Store) SaveDraft(content object) (Document, error) {
	content = normalizeContent(content)
	b, _ := json.Marshal(content)
	_, err := s.db.Exec("UPDATE site_documents SET content=?,updated_at=CURRENT_TIMESTAMP WHERE name='draft'", string(b))
	if err != nil {
		return Document{}, err
	}
	return s.GetDocument("draft")
}
func (s *Store) Publish(username string) (object, error) {
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	draft, err := getDocumentQ(ctx, tx, "draft")
	if err != nil {
		return nil, err
	}
	published, err := getDocumentQ(ctx, tx, "published")
	if err != nil {
		return nil, err
	}
	if draft.Hash == published.Hash {
		return object{"changed": false, "id": nil, "content": published.Content, "updatedAt": published.UpdatedAt, "hash": published.Hash}, nil
	}
	b, _ := json.Marshal(draft.Content)
	if _, err = tx.Exec("UPDATE site_documents SET content=?,updated_at=CURRENT_TIMESTAMP WHERE name='published'", string(b)); err != nil {
		return nil, err
	}
	result, err := tx.Exec("INSERT INTO content_revisions (content,content_hash,created_by) VALUES (?,?,?)", string(b), draft.Hash, username)
	if err != nil {
		return nil, err
	}
	id, _ := result.LastInsertId()
	published, err = getDocumentQ(ctx, tx, "published")
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return object{"changed": true, "id": id, "content": published.Content, "updatedAt": published.UpdatedAt, "hash": published.Hash}, nil
}

func (s *Store) ListRevisions() ([]object, error) {
	published, err := s.GetDocument("published")
	if err != nil {
		return nil, err
	}
	draft, err := s.GetDocument("draft")
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query("SELECT id,content,content_hash,created_at,created_by FROM content_revisions ORDER BY id DESC LIMIT 30")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type rawRev struct {
		id                         int64
		content, hash, created, by string
	}
	var all []rawRev
	for rows.Next() {
		var r rawRev
		if err = rows.Scan(&r.id, &r.content, &r.hash, &r.created, &r.by); err != nil {
			return nil, err
		}
		all = append(all, r)
	}
	out := make([]object, 0, len(all))
	for i, r := range all {
		c := normalizeContent(parseObject([]byte(r.content)))
		profile, hero := asObject(c["profile"]), asObject(c["hero"])
		same := false
		if i+1 < len(all) {
			same = r.hash == all[i+1].hash
		}
		out = append(out, object{"id": r.id, "hash": r.hash, "shortHash": prefix(r.hash, 10), "matchesPublished": r.hash == published.Hash, "matchesDraft": r.hash == draft.Hash, "sameAsPrevious": same, "createdAt": r.created, "createdBy": r.by, "siteName": stringValue(profile["siteName"]), "heroTitle": strings.TrimSpace(stringValue(hero["title"]) + " " + stringValue(hero["linkedTitle"])), "counts": object{"experiences": len(asArray(c["experiences"])), "caseStudies": len(asArray(c["caseStudies"])), "projects": len(asArray(c["projects"]))}})
	}
	return out, nil
}
func prefix(s string, n int) string {
	if len(s) < n {
		return s
	}
	return s[:n]
}
func (s *Store) RestoreRevision(id int64) (Document, bool, error) {
	var raw string
	err := s.db.QueryRow("SELECT content FROM content_revisions WHERE id=?", id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return Document{}, false, nil
	}
	if err != nil {
		return Document{}, false, err
	}
	d, err := s.SaveDraft(parseObject([]byte(raw)))
	return d, true, err
}
func (s *Store) DeleteRevision(id int64) (object, bool, error) {
	var hash, created, by string
	err := s.db.QueryRow("SELECT content_hash,created_at,created_by FROM content_revisions WHERE id=?", id).Scan(&hash, &created, &by)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if _, err = s.db.Exec("DELETE FROM content_revisions WHERE id=?", id); err != nil {
		return nil, false, err
	}
	return object{"id": id, "hash": hash, "createdAt": created, "createdBy": by}, true, nil
}

func (s *Store) Setting(key string) string {
	var v string
	_ = s.db.QueryRow("SELECT value FROM cms_settings WHERE key=?", key).Scan(&v)
	return v
}
func (s *Store) SetSetting(key, value string) error {
	_, err := s.db.Exec("INSERT INTO cms_settings (key,value) VALUES (?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", key, value)
	return err
}
func (s *Store) CreateMedia(m Media) (int64, error) {
	r, err := s.db.Exec("INSERT INTO media_assets (original_name,storage,original_url,optimized_url,original_key,optimized_key,mime_type,width,height,size) VALUES (?,?,?,?,?,?,?,?,?,?)", m.OriginalName, m.Storage, m.OriginalURL, m.OptimizedURL, m.OriginalKey, m.OptimizedKey, m.MimeType, m.Width, m.Height, m.Size)
	if err != nil {
		return 0, err
	}
	return r.LastInsertId()
}

const mediaSelect = "SELECT id,original_name,storage,original_url,optimized_url,original_key,optimized_key,mime_type,width,height,size,created_at FROM media_assets"

func scanMedia(scanner interface{ Scan(...any) error }) (Media, error) {
	var m Media
	err := scanner.Scan(&m.ID, &m.OriginalName, &m.Storage, &m.OriginalURL, &m.OptimizedURL, &m.OriginalKey, &m.OptimizedKey, &m.MimeType, &m.Width, &m.Height, &m.Size, &m.CreatedAt)
	return m, err
}
func (s *Store) GetMedia(id int64) (Media, bool, error) {
	m, err := scanMedia(s.db.QueryRow(mediaSelect+" WHERE id=?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return Media{}, false, nil
	}
	return m, err == nil, err
}
func (s *Store) ListMedia() ([]Media, error) {
	rows, err := s.db.Query(mediaSelect + " ORDER BY id DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Media, 0)
	for rows.Next() {
		m, err := scanMedia(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
func (s *Store) DeleteMedia(id int64) error {
	_, err := s.db.Exec("DELETE FROM media_assets WHERE id=?", id)
	return err
}
func (s *Store) CleanSessions() error {
	_, err := s.db.Exec("DELETE FROM admin_sessions WHERE expires_at<?", time.Now().UnixMilli())
	return err
}
func (s *Store) Session(hash string) (string, bool) {
	_ = s.CleanSessions()
	var username string
	var expires int64
	err := s.db.QueryRow("SELECT username,expires_at FROM admin_sessions WHERE token_hash=?", hash).Scan(&username, &expires)
	return username, err == nil && expires >= time.Now().UnixMilli()
}
func (s *Store) CreateSession(hash, user string, expires int64) error {
	_, err := s.db.Exec("INSERT INTO admin_sessions (token_hash,username,expires_at) VALUES (?,?,?)", hash, user, expires)
	return err
}
func (s *Store) DeleteSession(hash string) error {
	_, err := s.db.Exec("DELETE FROM admin_sessions WHERE token_hash=?", hash)
	return err
}
func (s *Store) UserHash(username string) (string, bool) {
	var hash string
	err := s.db.QueryRow("SELECT password_hash FROM admin_users WHERE username=?", username).Scan(&hash)
	return hash, err == nil
}
func (s *Store) UpsertAdminHash(hash string) error {
	_, exists := s.UserHash("admin")
	if exists {
		_, err := s.db.Exec("UPDATE admin_users SET password_hash=? WHERE username='admin'", hash)
		return err
	}
	_, err := s.db.Exec("INSERT INTO admin_users (username,password_hash) VALUES ('admin',?)", hash)
	return err
}
func (s *Store) Log(l AdminLog) error {
	_, err := s.db.Exec("INSERT INTO admin_logs (username,action,target_type,target_id,detail,ip_address,request_id,method,path,status_code,duration_ms,response_bytes,user_agent,referer) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)", l.Username, l.Action, l.TargetType, l.TargetID, l.Detail, l.IPAddress, l.RequestID, l.Method, l.Path, l.StatusCode, l.DurationMS, l.ResponseBytes, l.UserAgent, l.Referer)
	return err
}
func (s *Store) ListLogs(limit int) ([]AdminLog, error) {
	if limit < 1 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	rows, err := s.db.Query("SELECT id,username,action,target_type,target_id,detail,ip_address,created_at,request_id,method,path,status_code,duration_ms,response_bytes,user_agent,referer FROM admin_logs ORDER BY id DESC LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]AdminLog, 0)
	for rows.Next() {
		var l AdminLog
		if err = rows.Scan(&l.ID, &l.Username, &l.Action, &l.TargetType, &l.TargetID, &l.Detail, &l.IPAddress, &l.CreatedAt, &l.RequestID, &l.Method, &l.Path, &l.StatusCode, &l.DurationMS, &l.ResponseBytes, &l.UserAgent, &l.Referer); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}
func (s *Store) PruneLogs(keep int) error {
	if keep < 1000 {
		keep = 1000
	}
	_, err := s.db.Exec("DELETE FROM admin_logs WHERE id NOT IN (SELECT id FROM admin_logs ORDER BY id DESC LIMIT ?)", keep)
	return err
}
func (s *Store) Close() error { return s.db.Close() }
