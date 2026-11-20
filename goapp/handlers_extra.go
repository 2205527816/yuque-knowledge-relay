package main

import (
	"crypto/sha1"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var reHTMLContent = regexp.MustCompile(`(?i)^\s*(<!doctype|<html)`)

// changeLibrary 在新库建档、插目录，再删旧文档。
func changeLibrary(db *sql.DB, c Config, docID, newLibID int64) (string, error) {
	doc, err := getDocument(db, docID)
	if err != nil {
		return "", err
	}
	newLib, err := getLibrary(db, newLibID)
	if err != nil {
		return "", err
	}
	if doc.LibraryID.Valid && doc.LibraryID.Int64 == newLib.ID {
		return "", nil
	}
	var oldLib *Library
	if doc.LibraryID.Valid {
		if l, err := getLibrary(db, doc.LibraryID.Int64); err == nil {
			oldLib = &l
		}
	}
	client := clientsFor(c)
	if client == nil {
		return "", &YuqueError{"未配置语雀 token"}
	}
	wasWritten := doc.Status == "written"
	body := doc.RawMD
	if wasWritten {
		sel, _ := selectedTerms(db, docID)
		body, _ = renderMarkdown(doc.RawMD, sel, c.AnnotateFirstParaOnly)
	}
	ref, err := findOrCreateDoc(client, newLib.Namespace, newLib.Slug, doc.Title, body, "kb-"+shortHash(doc.ContentHash))
	if err != nil {
		return "", err
	}
	warning := ensureTOC(client, newLib, ref.ID, false)
	if oldLib != nil && doc.YuqueDocID.Valid {
		if err := client.DeleteDoc(oldLib.Namespace, oldLib.Slug, doc.YuqueDocID.Int64); err != nil {
			note := fmt.Sprintf("旧文档未删除，请手动清理：%v", err)
			if warning != "" {
				warning += "；" + note
			} else {
				warning = note
			}
		}
	}
	status := "created"
	if wasWritten {
		status = "written"
	} else if n, _ := db.Query("SELECT COUNT(*) FROM terms WHERE document_id=?", docID); n != nil {
		count := 0
		n.Scan(&count)
		n.Close()
		if count > 0 {
			status = "terms_ready"
		}
	}
	if isPending(c, newLib) {
		status = "pending_class"
	}
	db.Exec("UPDATE documents SET library_id=?, yuque_doc_id=?, yuque_body_hash=NULL, status=?, error=? WHERE id=?",
		newLib.ID, ref.ID, status, nullable(warning), docID)
	addEvent(db, docID, "library_changed", map[string]any{"to": newLib.Name, "warning": warning})
	return warning, nil
}

func libraryHandler(db *sql.DB, c Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
		libID, _ := strconv.ParseInt(r.FormValue("library_id"), 10, 64)
		if libID == 0 {
			redirect(w, r, fmt.Sprintf("/docs/%d", id), "", "请选择知识库")
			return
		}
		warning, err := changeLibrary(db, c, id, libID)
		if err != nil {
			redirect(w, r, fmt.Sprintf("/docs/%d", id), "", "改分类失败："+err.Error())
			return
		}
		msg := "已改分类"
		if warning != "" {
			msg += "（" + warning + "）"
		}
		redirect(w, r, fmt.Sprintf("/docs/%d", id), msg, "")
	}
}

// simpreadHandler 接收 SimpRead Webhook：JSON（content/html/markdown）或原始正文。
func simpreadHandler(db *sql.DB, c Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(io.LimitReader(r.Body, 10<<20))
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "read"})
			return
		}
		var payload map[string]any
		hasJSON := json.Unmarshal(raw, &payload) == nil
		content := ""
		if hasJSON {
			for _, key := range []string{"content", "html", "markdown"} {
				if v, ok := payload[key].(string); ok && strings.TrimSpace(v) != "" {
					content = v
					break
				}
			}
		} else {
			content = string(raw)
		}
		content = strings.TrimSpace(content)
		if content == "" {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "empty content"})
			return
		}
		if len(content) > 10<<20 {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "too large"})
			return
		}
		title, srcURL := "", ""
		if hasJSON {
			title, _ = payload["title"].(string)
			srcURL, _ = payload["url"].(string)
		}
		isHTML := reHTMLContent.MatchString(content)
		ext := ".md"
		if isHTML {
			ext = ".html"
		}
		sum := sha1.Sum([]byte(content))
		name := fmt.Sprintf("simpread-%s-%s%s", time.Now().Format("20060102-150405"), hex.EncodeToString(sum[:4]), ext)
		var header strings.Builder
		header.WriteString("---\n")
		if title != "" {
			header.WriteString("title: " + strings.ReplaceAll(title, "\n", " ") + "\n")
		}
		if srcURL != "" {
			header.WriteString("url: " + srcURL + "\n")
		}
		header.WriteString("---\n\n")
		os.MkdirAll(inboxDir(), 0o755)
		path := filepath.Join(inboxDir(), name)
		if err := os.WriteFile(path, []byte(header.String()+content), 0o644); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "write"})
			return
		}
		batchID := startBatchWithSource(db, c, []string{path}, "simpread")
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "file": name, "batch_id": batchID})
	}
}
