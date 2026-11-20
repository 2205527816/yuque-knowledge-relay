package main

import (
	"database/sql"
	"fmt"
	"log"
	"strings"
)

// clientsFor 在测试里可替换，生产就是 newYuqueClients。
var clientsFor = newYuqueClients

// YuqueConflict 表示远端被动过，需要确认后才能覆盖。
type YuqueConflict struct{ Msg string }

func (e *YuqueConflict) Error() string { return e.Msg }

func isPending(c Config, lib Library) bool { return lib.Name == c.Yuque.PendingLibrary }

func yuqueReady(c Config, lib Library) bool {
	hasAuth := c.Yuque.Token != "" || (c.Yuque.Session != "" && c.Yuque.Ctoken != "")
	return hasAuth && lib.Namespace != "" && lib.Slug != ""
}

// slugFinder 是可选能力：语雀库里已有同 slug 文档时接管它，避免 slug_exists 卡死。
type slugFinder interface {
	FindDocBySlug(ns, repo, slug string) (int64, error)
}

func isSlugExists(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "slug_exists") || strings.Contains(msg, "slug already exists")
}

func findOrCreateDoc(client YuqueClient, ns, repo, title, body, slug string) (DocRef, error) {
	ref, err := client.CreateDoc(ns, repo, title, body, slug)
	if err == nil {
		return ref, nil
	}
	if !isSlugExists(err) {
		return ref, err
	}
	finder, ok := client.(slugFinder)
	if !ok {
		return ref, err
	}
	id, ferr := finder.FindDocBySlug(ns, repo, slug)
	if ferr != nil || id <= 0 {
		return ref, err
	}
	log.Printf("语雀 slug 已存在，接管旧文档 ns=%s repo=%s slug=%s id=%d", ns, repo, slug, id)
	return DocRef{ID: id}, nil
}

// ensureTOC 建档/写回后兜底：不在目录里就插到顶部，失败只返回提示。
func ensureTOC(client YuqueClient, lib Library, docID int64, check bool) string {
	if client == nil {
		return ""
	}
	if check {
		nodes, err := client.GetTOC(lib.Namespace, lib.Slug)
		if err != nil {
			return "文档已建，但没加进语雀目录：" + err.Error()
		}
		for _, n := range nodes {
			if n.DocID == docID {
				return ""
			}
		}
	}
	if err := client.PrependTOC(lib.Namespace, lib.Slug, docID); err != nil {
		return "文档已建，但没加进语雀目录：" + err.Error()
	}
	return ""
}

// createDoc 建档并落 yuque_doc_id；已建过的直接返回。
func createDoc(db *sql.DB, c Config, docID int64) (int64, error) {
	doc, err := getDocument(db, docID)
	if err != nil {
		return 0, err
	}
	if doc.YuqueDocID.Valid {
		return doc.YuqueDocID.Int64, nil
	}
	if !doc.LibraryID.Valid {
		return 0, fmt.Errorf("文档未分类")
	}
	lib, err := getLibrary(db, doc.LibraryID.Int64)
	if err != nil {
		return 0, err
	}
	if lib.Namespace == "" || lib.Slug == "" {
		return 0, &YuqueError{"库「" + lib.Name + "」未配置 namespace/slug"}
	}
	client := clientsFor(c)
	if client == nil {
		return 0, &YuqueError{"未配置语雀 token"}
	}
	ref, err := findOrCreateDoc(client, lib.Namespace, lib.Slug, doc.Title, withSourceCite(doc.RawMD, doc.SourceURL), "kb-"+shortHash(doc.ContentHash))
	if err != nil {
		return 0, err
	}
	if ref.ID <= 0 {
		return 0, &YuqueError{"语雀返回了空文档 ID"}
	}
	status := "created"
	if isPending(c, lib) {
		status = "pending_class"
	}
	if _, err := db.Exec("UPDATE documents SET yuque_doc_id=?, status=? WHERE id=?", ref.ID, status, docID); err != nil {
		return 0, err
	}
	addEvent(db, docID, "created", map[string]any{"library": lib.Name, "doc_id": ref.ID})
	if remote, err := client.GetDoc(lib.Namespace, lib.Slug, ref.ID); err == nil && remote.Body != nil {
		db.Exec("UPDATE documents SET yuque_body_hash=? WHERE id=?", contentHash(*remote.Body), docID)
	}
	if note := ensureTOC(client, lib, ref.ID, false); note != "" {
		addEvent(db, docID, "toc_failed", map[string]any{"error": note})
	}
	return ref.ID, nil
}

// writeBack 渲染 → 冲突检查 → 更新语雀 → 回读 → 保存 glossary → 兜底目录。
func writeBack(db *sql.DB, c Config, docID int64, anns []Annotation, force bool) ([]string, error) {
	if yuqueB.blocked() {
		return nil, &YuqueError{"语雀限流中（429），词条已生成；等限流解除后点“写回”即可建档并进目录。"}
	}
	doc, err := getDocument(db, docID)
	if err != nil {
		return nil, err
	}
	if !doc.YuqueDocID.Valid {
		if _, err := createDoc(db, c, docID); err != nil {
			return nil, err
		}
		if doc, err = getDocument(db, docID); err != nil {
			return nil, err
		}
	}
	lib, err := getLibrary(db, doc.LibraryID.Int64)
	if err != nil {
		return nil, err
	}
	client := clientsFor(c)
	if client == nil {
		return nil, &YuqueError{"未配置语雀 token"}
	}

	var warnings []string
	var active []Annotation
	var empty []string
	for _, a := range anns {
		if strings.TrimSpace(a.Explanation) == "" {
			empty = append(empty, a.Term)
			continue
		}
		active = append(active, a)
	}
	rendered, missing := renderMarkdown(withSourceCite(doc.RawMD, doc.SourceURL), active, c.AnnotateFirstParaOnly)

	remote, err := client.GetDoc(lib.Namespace, lib.Slug, doc.YuqueDocID.Int64)
	if err != nil {
		return nil, err
	}
	if remote.Format != "" && remote.Format != "markdown" && !force {
		return nil, &YuqueConflict{"远端文档已转为 lake 格式，确认后可强制覆盖"}
	}
	if doc.YuqueBodyHash.Valid && remote.Body != nil && contentHash(*remote.Body) != doc.YuqueBodyHash.String && !force {
		return nil, &YuqueConflict{"远端正文已被修改，确认后可强制覆盖"}
	}

	yuqueURL := fmt.Sprintf("https://www.yuque.com/%s/%s/kb-%s", lib.Namespace, lib.Slug, shortHash(doc.ContentHash))
	bk := backupConfig(c)
	backupNote := ""
	runBackup := func() {
		res := backupArticle(db, c, doc, lib.Name, rendered, yuqueURL, len(active))
		if res == nil || res["status"] != "failed" {
			return
		}
		msg := fmt.Sprintf("GitHub 备份失败：%v", res["error"])
		addEvent(db, docID, "backup_github", res)
		if bk.Required {
			backupNote = msg
			return
		}
		backupNote = msg
	}
	if bk.Enabled && bk.Order != "after" {
		runBackup()
		if bk.Required && backupNote != "" {
			return nil, &YuqueError{backupNote}
		}
	}

	rendered, imageNotes := localizeImages(c, doc, rendered)
	warnings = append(warnings, imageNotes...)
	if err := client.UpdateDoc(lib.Namespace, lib.Slug, doc.YuqueDocID.Int64, doc.Title, rendered); err != nil {
		return nil, err
	}
	if bk.Enabled && bk.Order == "after" {
		runBackup()
		if bk.Required && backupNote != "" {
			return nil, &YuqueError{backupNote}
		}
	}
	note := ensureTOC(client, lib, doc.YuqueDocID.Int64, true)
	for _, a := range active {
		saveGlossary(db, a.Term, doc.LibraryID, a.Explanation, map[string]any{})
	}
	bodyHash := contentHash(rendered)
	if latest, err := client.GetDoc(lib.Namespace, lib.Slug, doc.YuqueDocID.Int64); err == nil && latest.Body != nil && strings.TrimSpace(*latest.Body) != "" {
		bodyHash = contentHash(*latest.Body)
	}
	if _, err := db.Exec("UPDATE documents SET yuque_body_hash=?, status='written', error=NULL WHERE id=?", bodyHash, docID); err != nil {
		return nil, err
	}
	addEvent(db, docID, "written_to_yuque", map[string]any{"missing": missing, "terms": len(active)})

	if c.Retention.AutoArchiveWritten && doc.Retention != "archived" {
		db.Exec("UPDATE documents SET retention_status='archived', archived_at=datetime('now'), deleted_at=NULL WHERE id=?", docID)
		addEvent(db, docID, "retention_archived", map[string]any{"reason": "auto_archive_written"})
	}
	warnings = append(warnings, missing...)
	for _, t := range empty {
		warnings = append(warnings, t+"（释义为空，已跳过）")
	}
	if note != "" {
		warnings = append(warnings, note)
	}
	if backupNote != "" {
		warnings = append(warnings, backupNote)
	}
	return warnings, nil
}

// markFailed 失败收口：状态置 failed 并留错因。
func markFailed(db *sql.DB, docID int64, err error) {
	db.Exec("UPDATE documents SET status='failed', error=? WHERE id=?", truncate(err.Error(), 500), docID)
	addEvent(db, docID, "error", map[string]any{"error": truncate(err.Error(), 500)})
}

// createDocSoftly 建档失败（限流/网络）不阻断抽词，返回一句提示。
func createDocSoftly(db *sql.DB, c Config, docID int64) string {
	if yuqueB.blocked() {
		return "语雀限流中（429），词条已生成；等限流解除后点“写回”即可建档并进目录。"
	}
	if _, err := createDoc(db, c, docID); err != nil {
		if strings.Contains(err.Error(), "429") {
			// 熔断由客户端层 breakerGuard 统一触发，这里只负责给提示
			return "语雀限流中（429），词条已生成；稍后点“写回”重试。"
		}
		return "语雀建档失败，词条仍已生成；稍后点“确认并写回语雀”重建：" + truncate(err.Error(), 120)
	}
	return ""
}

// selectedTerms 返回勾选的词条（按段落序）。
func selectedTerms(db *sql.DB, docID int64) ([]Annotation, error) {
	rows, err := db.Query(`SELECT term, COALESCE(explanation,'') FROM terms
		WHERE document_id=? AND selected=1 ORDER BY COALESCE(para_index,0), id`, docID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Annotation
	for rows.Next() {
		var a Annotation
		if err := rows.Scan(&a.Term, &a.Explanation); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// shortHash 取前 10 位；异常短哈希也不会 panic。
func shortHash(h string) string {
	if len(h) > 10 {
		return h[:10]
	}
	return h
}
