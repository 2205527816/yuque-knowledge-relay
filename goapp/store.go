package main

import (
	"database/sql"
	"encoding/json"
	"strings"
)

type Library struct {
	ID         int64
	Name       string
	Namespace  string
	Slug       string
	Multiplier float64
}

type Document struct {
	ID             int64
	SourceURL      string
	Title          string
	Author         string
	RawMD          string
	ContentHash    string
	Units          int
	Status         string
	LibraryID      sql.NullInt64
	YuqueDocID     sql.NullInt64
	YuqueBodyHash  sql.NullString
	ScoresJSON     string
	Error          sql.NullString
	CreatedAt      string
	Retention      string
	Stage          sql.NullString
	DeadlineAt     sql.NullString
	LibraryName    string
	TermCount      int
	SelectedCount  int
	AnnotatedCount int
	TopScore       float64
	HasScore       bool
	Badge          string
}

type Term struct {
	ID          int64
	DocumentID  int64
	Term        string
	Type        string
	Importance  float64
	ParaIndex   int
	Explanation string
	SourcesJSON string
	Selected    bool
	Manual      bool
}

func listLibraries(db *sql.DB) ([]Library, error) {
	rows, err := db.Query("SELECT id, name, yuque_namespace, yuque_slug, multiplier FROM libraries ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Library
	for rows.Next() {
		var l Library
		if err := rows.Scan(&l.ID, &l.Name, &l.Namespace, &l.Slug, &l.Multiplier); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func getLibrary(db *sql.DB, id int64) (Library, error) {
	var l Library
	err := db.QueryRow("SELECT id, name, yuque_namespace, yuque_slug, multiplier FROM libraries WHERE id=?", id).
		Scan(&l.ID, &l.Name, &l.Namespace, &l.Slug, &l.Multiplier)
	return l, err
}

const docColumns = `d.id, COALESCE(d.source_url,''), d.title, COALESCE(d.author,''), d.raw_md,
	d.content_hash, d.units, d.status, d.library_id, d.yuque_doc_id, COALESCE(d.yuque_body_hash,''),
	COALESCE(d.scores_json,''), d.error, d.created_at, d.retention_status, d.stage, d.deadline_at,
	COALESCE(l.name,'')`

func scanDoc(rows interface{ Scan(...any) error }) (Document, error) {
	var d Document
	err := rows.Scan(&d.ID, &d.SourceURL, &d.Title, &d.Author, &d.RawMD, &d.ContentHash, &d.Units,
		&d.Status, &d.LibraryID, &d.YuqueDocID, &d.YuqueBodyHash, &d.ScoresJSON, &d.Error,
		&d.CreatedAt, &d.Retention, &d.Stage, &d.DeadlineAt, &d.LibraryName)
	return d, err
}

func getDocument(db *sql.DB, id int64) (Document, error) {
	row := db.QueryRow("SELECT "+docColumns+" FROM documents d LEFT JOIN libraries l ON l.id=d.library_id WHERE d.id=?", id)
	return scanDoc(row)
}

// listDocuments 对应 index 的筛选：库、保留状态、搜索词、分页。
type listFilter struct {
	LibID     int64
	Retention string
	Query     string
	Status    string
	Limit     int
	Offset    int
}

func buildWhere(f listFilter) (string, []any) {
	var where []string
	var params []any
	if f.LibID > 0 {
		where = append(where, "d.library_id=?")
		params = append(params, f.LibID)
	}
	if f.Retention != "all" {
		where = append(where, "d.retention_status=?")
		params = append(params, f.Retention)
	}
	if f.Retention == "active" {
		where = append(where,
			"(d.status IN ('pending_class','failed','terms_ready') OR (d.status IN ('imported','classified','created') AND EXISTS (SELECT 1 FROM terms t WHERE t.document_id=d.id AND TRIM(COALESCE(t.explanation,'')) <> '')))")
		where = append(where, "(d.stage IS NULL OR d.stage IN ('done','failed'))")
		// 写回进行中的文章先隐藏；写回失败才回到列表。
		where = append(where, "(d.status='failed' OR NOT EXISTS (SELECT 1 FROM pending_writes pw WHERE pw.document_id=d.id))")
	}
	if f.Status == "conflict" {
		where = append(where, "d.error IN (?, ?)")
		params = append(params, remoteConflictBody, remoteConflictLake)
	} else if f.Status != "" {
		where = append(where, "d.status=?")
		params = append(params, f.Status)
	}
	if f.Query != "" {
		where = append(where, "(d.title LIKE ? OR COALESCE(d.source_url,'') LIKE ?)")
		like := "%" + f.Query + "%"
		params = append(params, like, like)
	}
	if len(where) == 0 {
		return "", params
	}
	return " WHERE " + strings.Join(where, " AND "), params
}

func countDocuments(db *sql.DB, f listFilter) (int, error) {
	where, params := buildWhere(f)
	var n int
	err := db.QueryRow("SELECT COUNT(*) FROM documents d"+where, params...).Scan(&n)
	return n, err
}

func listDocuments(db *sql.DB, f listFilter) ([]Document, error) {
	where, params := buildWhere(f)
	q := "SELECT " + docColumns + `, 
		(SELECT COUNT(*) FROM terms t WHERE t.document_id=d.id),
		(SELECT COUNT(*) FROM terms t WHERE t.document_id=d.id AND t.selected=1),
		(SELECT COUNT(*) FROM terms t WHERE t.document_id=d.id AND TRIM(COALESCE(t.explanation,'')) <> '')
		FROM documents d LEFT JOIN libraries l ON l.id=d.library_id` + where +
		" ORDER BY d.id DESC LIMIT ? OFFSET ?"
	rows, err := db.Query(q, append(params, f.Limit, f.Offset)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Document
	for rows.Next() {
		var d Document
		if err := rows.Scan(&d.ID, &d.SourceURL, &d.Title, &d.Author, &d.RawMD, &d.ContentHash, &d.Units,
			&d.Status, &d.LibraryID, &d.YuqueDocID, &d.YuqueBodyHash, &d.ScoresJSON, &d.Error,
			&d.CreatedAt, &d.Retention, &d.Stage, &d.DeadlineAt, &d.LibraryName,
			&d.TermCount, &d.SelectedCount, &d.AnnotatedCount); err != nil {
			return nil, err
		}
		out = append(out, decorate(d))
	}
	return out, rows.Err()
}

// decorate 算 top_score 和列表徽标（跟 main.py 一致）。
func decorate(d Document) Document {
	if d.ScoresJSON != "" {
		var scores map[string]float64
		if json.Unmarshal([]byte(d.ScoresJSON), &scores) == nil {
			for _, v := range scores {
				if !d.HasScore || v > d.TopScore {
					d.TopScore, d.HasScore = v, true
				}
			}
		}
	}
	switch d.Status {
	case "pending_class", "failed", "terms_ready", "written":
		d.Badge = d.Status
	default:
		if d.AnnotatedCount > 0 {
			d.Badge = "terms_ready"
		}
	}
	return d
}

func listTerms(db *sql.DB, docID int64) ([]Term, error) {
	rows, err := db.Query(`SELECT id, document_id, term, COALESCE(type,''), COALESCE(importance,0),
		COALESCE(para_index,0), COALESCE(explanation,''), COALESCE(sources_json,''), selected, manual
		FROM terms WHERE document_id=? ORDER BY para_index, id`, docID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Term
	for rows.Next() {
		var t Term
		if err := rows.Scan(&t.ID, &t.DocumentID, &t.Term, &t.Type, &t.Importance, &t.ParaIndex,
			&t.Explanation, &t.SourcesJSON, &t.Selected, &t.Manual); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func addEvent(db *sql.DB, docID int64, kind string, payload any) {
	var doc any
	if docID > 0 {
		doc = docID
	}
	b, _ := json.Marshal(payload)
	db.Exec("INSERT INTO events (document_id, type, payload_json) VALUES (?,?,?)", doc, kind, string(b))
}
