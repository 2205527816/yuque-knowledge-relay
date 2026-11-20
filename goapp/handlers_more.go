package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// previewHandler 返回预览区 HTML 片段（前端直接替换 innerHTML）。
func previewHandler(db *sql.DB, c Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
		doc, err := getDocument(db, id)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		selected, explanations := parseTermForm(r)
		terms, _ := listTerms(db, id)
		var anns []Annotation
		for _, t := range terms {
			expl := t.Explanation
			if v, ok := explanations[t.ID]; ok {
				expl = v
			}
			if selected[t.ID] && strings.TrimSpace(expl) != "" {
				anns = append(anns, Annotation{Term: t.Term, Explanation: expl})
			}
		}
		rendered, missing := renderMarkdown(withSourceCite(doc.RawMD, doc.SourceURL), anns, c.AnnotateFirstParaOnly)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<article class="article">%s</article>`, markdownToHTML(rendered, doc.SourceURL))
		if len(missing) > 0 {
			fmt.Fprintf(w, `<p class="warn">未命中：%s</p>`, html.EscapeString(strings.Join(missing, "、")))
		}
	}
}

// regenerateHandler 重新生成单词释义（跳过 glossary 缓存）。
func regenerateHandler(db *sql.DB, c Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		docID, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
		termID, _ := strconv.ParseInt(r.PathValue("termid"), 10, 64)
		var term string
		err := db.QueryRow("SELECT term FROM terms WHERE id=? AND document_id=?", termID, docID).Scan(&term)
		if err != nil {
			redirect(w, r, fmt.Sprintf("/docs/%d", docID), "", "词条不存在")
			return
		}
		doc, _ := getDocument(db, docID)
		llm, err := llmFromConfig("explain_model")
		if err != nil {
			redirect(w, r, fmt.Sprintf("/docs/%d", docID), "", err.Error())
			return
		}
		explanation, sources, _, err := explainOne(db, term, sentenceFor(term, doc.RawMD), doc.LibraryID, llm, false)
		if err != nil {
			redirect(w, r, fmt.Sprintf("/docs/%d", docID), "", "重新生成失败："+err.Error())
			return
		}
		raw, _ := json.Marshal(sources)
		db.Exec("UPDATE terms SET explanation=?, sources_json=? WHERE id=?", explanation, string(raw), termID)
		redirect(w, r, fmt.Sprintf("/docs/%d", docID), "释义已更新", "")
	}
}

// addTermHandler 手动加词：必须能在正文定位，否则拒绝。
func addTermHandler(db *sql.DB, c Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		docID, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
		r.ParseMultipartForm(8 << 20)
		term := strings.TrimSpace(r.FormValue("term"))
		if term == "" {
			redirect(w, r, fmt.Sprintf("/docs/%d", docID), "", "词不能为空")
			return
		}
		doc, err := getDocument(db, docID)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		found := locateTerms(doc.RawMD, []string{term})
		para, ok := found[term]
		if !ok {
			redirect(w, r, fmt.Sprintf("/docs/%d", docID), "", "正文中找不到该词")
			return
		}
		explanation := strings.TrimSpace(r.FormValue("explanation"))
		sources := map[string]any{}
		if explanation == "" {
			llm, err := llmFromConfig("explain_model")
			if err != nil {
				redirect(w, r, fmt.Sprintf("/docs/%d", docID), "", err.Error())
				return
			}
			expl, src, _, err := explainOne(db, term, sentenceFor(term, doc.RawMD), doc.LibraryID, llm, true)
			if err != nil {
				redirect(w, r, fmt.Sprintf("/docs/%d", docID), "", "生成释义失败："+err.Error())
				return
			}
			explanation, sources = expl, src
		}
		raw, _ := json.Marshal(sources)
		db.Exec(`INSERT INTO terms (document_id, term, type, importance, para_index, explanation, sources_json, selected, manual)
			VALUES (?, ?, '手动', 1.0, ?, ?, ?, 1, 1)
			ON CONFLICT(document_id, term) DO UPDATE SET manual=1, selected=1,
			explanation=excluded.explanation, sources_json=excluded.sources_json`,
			docID, term, para, explanation, string(raw))
		redirect(w, r, fmt.Sprintf("/docs/%d", docID), "已添加："+term, "")
	}
}

// librarySettingsHandler 保存倍率（0.5–2）。
func librarySettingsHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
		r.ParseMultipartForm(1 << 20)
		target := "/libraries"
		if r.FormValue("return_profile") == "1" {
			target = fmt.Sprintf("/libraries/%d/profile", id)
		}
		value, err := strconv.ParseFloat(r.FormValue("multiplier"), 64)
		if err != nil || value < 0.5 || value > 2 {
			redirect(w, r, target, "", "倍率必须在 0.5 到 2 之间")
			return
		}
		db.Exec("UPDATE libraries SET multiplier=? WHERE id=?", value, id)
		redirect(w, r, target, "已保存倍率", "")
	}
}

func profilePath(name string) string { return filepath.Join(profilesDir(), name+".md") }

// profilePageHandler 读写库档案（直接进分类提示词）。
func profilePageHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
		lib, err := getLibrary(db, id)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		content := ""
		if raw, err := os.ReadFile(profilePath(lib.Name)); err == nil {
			content = string(raw)
		}
		libs := []Library{lib}
		render(w, "profile", pageData{
			Title:       lib.Name + " · 分类档案",
			Msg:         r.URL.Query().Get("msg"),
			Error:       r.URL.Query().Get("error"),
			Library:     &libs[0],
			LibraryName: lib.Name,
			Profile:     content,
			ProfileHTML: markdownToHTML(content, ""),
		})
	}
}

func profileSaveHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
		lib, err := getLibrary(db, id)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		r.ParseMultipartForm(4 << 20)
		if err := os.MkdirAll(profilesDir(), 0o755); err != nil {
			redirect(w, r, fmt.Sprintf("/libraries/%d/profile", id), "", err.Error())
			return
		}
		if err := os.WriteFile(profilePath(lib.Name), []byte(r.FormValue("content")), 0o644); err != nil {
			redirect(w, r, fmt.Sprintf("/libraries/%d/profile", id), "", "保存失败："+err.Error())
			return
		}
		db.Exec("UPDATE libraries SET profile_version=profile_version+1 WHERE id=?", id)
		redirect(w, r, fmt.Sprintf("/libraries/%d/profile", id), "档案已保存", "")
	}
}
