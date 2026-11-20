package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

type viewTerm struct {
	ID          int64
	Term        string
	Type        string
	Explanation string
	Selected    bool
	Manual      bool
	HasWiki     bool
	HasGoogle   bool
	HasCache    bool
	Fallback    bool
}

type docProgress struct {
	Pct      int
	Position int
	Pending  int
}

type scoreItem struct {
	Name  string
	Score float64
}

type iconItem struct{ ID, Label string }
type iconGroup struct {
	Name  string
	Items []iconItem
}

func toViewTerm(t Term) viewTerm {
	v := viewTerm{ID: t.ID, Term: t.Term, Type: t.Type, Explanation: t.Explanation, Selected: t.Selected, Manual: t.Manual}
	if t.SourcesJSON != "" {
		var sources map[string]any
		if json.Unmarshal([]byte(t.SourcesJSON), &sources) == nil {
			if v0, ok := sources["wiki"]; ok && v0 != nil {
				v.HasWiki = true
			}
			if v1, ok := sources["google"]; ok && v1 != nil {
				if arr, ok := v1.([]any); ok && len(arr) > 0 {
					v.HasGoogle = true
				}
			}
			if cached, ok := sources["cached"].(bool); ok && cached {
				v.HasCache = true
			}
			if fb, ok := sources["llm_fallback"].(bool); ok && fb {
				v.Fallback = true
			}
		}
	}
	return v
}

func docQueuePosition(db *sql.DB, docID int64) docProgress {
	rows, err := db.Query(`SELECT d.id, d.status, d.library_id,
		(SELECT COUNT(*) FROM terms t WHERE t.document_id=d.id),
		(SELECT COUNT(*) FROM terms t WHERE t.document_id=d.id AND TRIM(COALESCE(t.explanation,'')) <> '')
		FROM documents d WHERE d.retention_status='active' ORDER BY d.id`)
	if err != nil {
		return docProgress{}
	}
	defer rows.Close()
	var pending []int64
	pct := 0
	for rows.Next() {
		var id, terms, annotated int64
		var status string
		var libID sql.NullInt64
		if rows.Scan(&id, &status, &libID, &terms, &annotated) != nil {
			continue
		}
		p := 10
		switch {
		case status == "written":
			p = 100
		case annotated > 0:
			p = 80
		case terms > 0:
			p = 55
		case libID.Valid:
			p = 30
		}
		if id == docID {
			pct = p
		}
		if p < 100 {
			pending = append(pending, id)
		}
	}
	pos := 0
	for i, id := range pending {
		if id == docID {
			pos = i + 1
		}
	}
	return docProgress{Pct: pct, Position: pos, Pending: len(pending)}
}

func docHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		doc, err := getDocument(db, id)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		libs, _ := listLibraries(db)
		data := pageData{
			Title:     doc.Title,
			Msg:       r.URL.Query().Get("msg"),
			Error:     r.URL.Query().Get("error"),
			Doc:       doc,
			Libraries: libs,
			HashShort: shortHash(doc.ContentHash),
			Conflict:  strings.Contains(r.URL.Query().Get("error"), "冲突"),
		}
		if doc.LibraryID.Valid {
			if lib, err := getLibrary(db, doc.LibraryID.Int64); err == nil {
				data.Library = &lib
				data.LibraryName = lib.Name
			}
		}
		terms, _ := listTerms(db, id)
		var anns []Annotation
		for _, t := range terms {
			vt := toViewTerm(t)
			data.Terms = append(data.Terms, vt)
			if t.Selected && strings.TrimSpace(t.Explanation) != "" {
				anns = append(anns, Annotation{Term: t.Term, Explanation: t.Explanation})
			}
		}
		if len(anns) > 0 {
			rendered, missing := renderMarkdown(withSourceCite(doc.RawMD, doc.SourceURL), anns, cfg.AnnotateFirstParaOnly)
			data.Missing = missing
			data.Preview = markdownToHTML(rendered, doc.SourceURL)
		} else {
			data.Preview = markdownToHTML(withSourceCite(doc.RawMD, doc.SourceURL), doc.SourceURL)
		}
		data.DocProgress = docQueuePosition(db, id)
		if doc.ScoresJSON != "" {
			var scores map[string]float64
			if json.Unmarshal([]byte(doc.ScoresJSON), &scores) == nil {
				for name, score := range scores {
					data.ScoreItems = append(data.ScoreItems, scoreItem{name, score})
				}
				sort.Slice(data.ScoreItems, func(i, j int) bool { return data.ScoreItems[i].Score > data.ScoreItems[j].Score })
			}
		}
		render(w, "doc", data)
	}
}

func librariesHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		libs, err := listLibraries(db)
		if err != nil {
			http.Error(w, "数据库错误", 500)
			return
		}
		render(w, "libraries", pageData{
			Title:     "知识库 · 知识库中转站",
			Msg:       r.URL.Query().Get("msg"),
			Error:     r.URL.Query().Get("error"),
			Libraries: libs,
		})
	}
}

func iconsHandler() http.HandlerFunc {
	groups := []iconGroup{
		{"文件夹", []iconItem{{"doc", "文史"}, {"doc-word", "策论"}, {"doc-file", "四方"}}},
		{"对话框", []iconItem{{"chat-news", "时评"}, {"chat-hot", "浪潮"}, {"chat-casual", "娱乐"}, {"chat", "冗谈"}, {"chat-mood", "奇偶"}}},
		{"书本", []iconItem{{"book-science", "数理"}, {"book-tech", "编程"}}},
		{"人物与技法", []iconItem{{"method", "技法"}, {"users-people", "人物"}}},
		{"档案盒", []iconItem{{"box-research", "研学"}, {"box-career", "功利"}, {"box-business", "经营"}}},
		{"品牌", []iconItem{{"logo", "知识库"}}},
	}
	return func(w http.ResponseWriter, r *http.Request) {
		render(w, "icons", pageData{Title: "图标样张 · 知识库中转站", IconGroups: groups})
	}
}
