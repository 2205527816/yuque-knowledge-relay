package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS libraries (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    yuque_namespace TEXT NOT NULL,
    yuque_slug TEXT NOT NULL,
    multiplier REAL NOT NULL DEFAULT 1.0,
    profile_path TEXT,
    profile_version INTEGER NOT NULL DEFAULT 1
);
CREATE TABLE IF NOT EXISTS documents (
    id INTEGER PRIMARY KEY,
    source_url TEXT,
    title TEXT NOT NULL,
    author TEXT,
    published_at TEXT,
    raw_md TEXT NOT NULL,
    content_hash TEXT NOT NULL UNIQUE,
    units INTEGER NOT NULL,
    status TEXT NOT NULL DEFAULT 'imported',
    library_id INTEGER REFERENCES libraries(id),
    scores_json TEXT,
    yuque_doc_id INTEGER UNIQUE,
    yuque_body_hash TEXT,
    error TEXT,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    retention_status TEXT NOT NULL DEFAULT 'active',
    archived_at TEXT,
    deleted_at TEXT,
    deadline_at TEXT
);
CREATE TABLE IF NOT EXISTS terms (
    id INTEGER PRIMARY KEY,
    document_id INTEGER NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    term TEXT NOT NULL,
    type TEXT,
    importance REAL,
    para_index INTEGER,
    explanation TEXT,
    sources_json TEXT,
    selected INTEGER NOT NULL DEFAULT 1,
    manual INTEGER NOT NULL DEFAULT 0,
    UNIQUE (document_id, term)
);
CREATE TABLE IF NOT EXISTS glossary (
    term TEXT NOT NULL,
    library_id INTEGER NOT NULL REFERENCES libraries(id),
    explanation TEXT NOT NULL,
    sources_json TEXT,
    updated_at TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE (term, library_id)
);
CREATE TABLE IF NOT EXISTS events (
    id INTEGER PRIMARY KEY,
    document_id INTEGER REFERENCES documents(id) ON DELETE CASCADE,
    type TEXT NOT NULL,
    payload_json TEXT,
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE TABLE IF NOT EXISTS backups (
    id INTEGER PRIMARY KEY,
    document_id INTEGER NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    provider TEXT NOT NULL,
    path TEXT,
    content_sha TEXT,
    blob_sha TEXT,
    commit_sha TEXT,
    status TEXT NOT NULL,
    error TEXT,
    updated_at TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE (document_id, provider)
);
CREATE TABLE IF NOT EXISTS settings (
    key TEXT PRIMARY KEY,
    value TEXT
);
CREATE TABLE IF NOT EXISTS pending_writes (
    document_id INTEGER PRIMARY KEY,
    force INTEGER NOT NULL DEFAULT 0,
    attempts INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE TABLE IF NOT EXISTS batches (
    id INTEGER PRIMARY KEY,
    source TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'running',
    total INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    finished_at TEXT
);
`

// openDB 打开同一个 kb.db；WAL 和当时 Python 版一致。
func openDB(path string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(30000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // SQLite 写锁不抢，一个连接贯穿全进程
	if err := db.Ping(); err != nil {
		return nil, err
	}
	return db, nil
}

// initDB 建表并把老库缺的列补上（跟 db.py 一致）。
func initDB(db *sql.DB) error {
	if _, err := db.Exec(schema); err != nil {
		return err
	}
	cols, err := tableColumns(db, "documents")
	if err != nil {
		return err
	}
	add := func(name, ddl string) error {
		if cols[name] {
			return nil
		}
		_, err := db.Exec(fmt.Sprintf("ALTER TABLE documents ADD COLUMN %s", ddl))
		return err
	}
	for _, c := range []struct{ name, ddl string }{
		{"retention_status", "retention_status TEXT NOT NULL DEFAULT 'active'"},
		{"archived_at", "archived_at TEXT"},
		{"deleted_at", "deleted_at TEXT"},
		{"deadline_at", "deadline_at TEXT"},
		{"batch_id", "batch_id INTEGER"},
		{"stage", "stage TEXT"},
		{"stage_detail", "stage_detail TEXT"},
		{"stage_pct", "stage_pct INTEGER"},
	} {
		if err := add(c.name, c.ddl); err != nil {
			return err
		}
	}
	if cols, err := tableColumns(db, "pending_writes"); err == nil && len(cols) > 0 && !cols["attempts"] {
		db.Exec("ALTER TABLE pending_writes ADD COLUMN attempts INTEGER NOT NULL DEFAULT 0")
	}
	_, err = db.Exec("UPDATE documents SET retention_status='active' WHERE retention_status IS NULL OR retention_status=''")
	return err
}

func tableColumns(db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, typ string
		var notnull int
		var dflt sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			return nil, err
		}
		out[name] = true
	}
	return out, rows.Err()
}

// syncLibraries 把 config.yaml 里的库同步进数据库：缺少就插入，
// 已存在则只校正 namespace/slug（库名是主键标识，倍率允许在页面上改）。
// 没有它，全新部署的库表会是空的，页面看到不到任何库。
func syncLibraries(db *sql.DB, libs []ConfigLibrary) error {
	for _, l := range libs {
		name := strings.TrimSpace(l.Name)
		if name == "" {
			continue
		}
		multiplier := l.Multiplier
		if multiplier <= 0 {
			multiplier = 1.0
		}
		if _, err := db.Exec(`INSERT INTO libraries (name, yuque_namespace, yuque_slug, multiplier, profile_path)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(name) DO UPDATE SET
				yuque_namespace=excluded.yuque_namespace,
				yuque_slug=excluded.yuque_slug`,
			name, l.Namespace, l.Slug, multiplier, "profiles/"+name+".md"); err != nil {
			return err
		}
	}
	return nil
}
