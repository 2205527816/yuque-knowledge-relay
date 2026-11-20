package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

// importFile 解析并入库；先按 source_url，再按 content_hash 去重。
func importFile(db *sql.DB, c Config, path string) (int64, bool, error) {
	doc, err := parseFile(path)
	if err != nil {
		return 0, false, err
	}
	var existing int64
	if doc.SourceURL != "" {
		if err := db.QueryRow("SELECT id FROM documents WHERE source_url=?", doc.SourceURL).Scan(&existing); err == nil {
			return existing, false, nil
		}
	}
	if err := db.QueryRow("SELECT id FROM documents WHERE content_hash=?", doc.ContentHash).Scan(&existing); err == nil {
		return existing, false, nil
	}
	units := countUnits(doc.BodyMD)
	minutes := c.Processing.DeadlineMinutes
	if minutes <= 0 {
		minutes = 10
	}
	res, err := db.Exec(`INSERT INTO documents (source_url, title, author, published_at, raw_md, content_hash, units, status, deadline_at)
		VALUES (?,?,?,?,?,?,?, 'imported', datetime('now', ?))`,
		nullable(doc.SourceURL), doc.Title, nullable(doc.Author), nullable(doc.PublishedAt),
		doc.BodyMD, doc.ContentHash, units, fmt.Sprintf("+%d minutes", minutes))
	if err != nil {
		return 0, false, err
	}
	id, _ := res.LastInsertId()
	addEvent(db, id, "imported", map[string]any{"title": doc.Title})
	return id, true, nil
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

type termRow struct {
	term        string
	typ         string
	importance  float64
	paraIndex   int
	explanation string
	sourcesJSON string
}

// classifyDocument 给 15 个库打分并入库；两次校验失败进待定（不算失败）。
func classifyDocument(db *sql.DB, c Config, docID int64) (string, error) {
	doc, err := getDocument(db, docID)
	if err != nil {
		return "", err
	}
	libs, err := listLibraries(db)
	if err != nil {
		return "", err
	}
	profiles, err := loadProfiles(profilesDir())
	if err != nil {
		return "", err
	}
	// 只给正式库打分；待定库是兜底，不进提示词也不参与校验。
	scored := make([]Profile, 0, len(profiles))
	names := make([]string, 0, len(profiles))
	pendingID := int64(0)
	for _, p := range profiles {
		if p.Name == c.Yuque.PendingLibrary {
			continue
		}
		scored = append(scored, p)
		names = append(names, p.Name)
	}
	profiles = scored
	for _, l := range libs {
		if l.Name == c.Yuque.PendingLibrary {
			pendingID = l.ID
		}
	}
	llm, err := llmFromConfig("classify_model")
	if err != nil {
		return "", err
	}
	messages := classifyMessages(doc.Title, doc.RawMD, profiles, c.Classify.BodyChars)
	var scores map[string]float64
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		raw, err := llm.Chat(messages, "", 0)
		if err != nil {
			return "", err
		}
		scores, lastErr = parseScores(raw, names)
		if lastErr == nil {
			break
		}
	}
	if lastErr != nil {
		db.Exec("UPDATE documents SET library_id=?, scores_json='{}', status='pending_class', error=? WHERE id=?",
			pendingID, "分类结果两次校验失败："+lastErr.Error(), docID)
		addEvent(db, docID, "classification_failed", map[string]any{"error": lastErr.Error()})
		return c.Yuque.PendingLibrary, nil
	}
	picked := pickLibrary(scores, c.Classify.Threshold)
	libID := pendingID
	status := "pending_class"
	if picked != "" {
		for _, l := range libs {
			if l.Name == picked {
				libID = l.ID
			}
		}
		status = "classified"
	}
	raw, _ := json.Marshal(scores)
	db.Exec("UPDATE documents SET library_id=?, scores_json=?, status=?, error=NULL WHERE id=?",
		libID, string(raw), status, docID)
	addEvent(db, docID, "classified", map[string]any{"library": picked, "scores": scores})
	return picked, nil
}

// prepareTerms 抽词→释义，全部成功后一个事务写入，避免半成品可见。
func prepareTerms(db *sql.DB, c Config, docID int64, progress func(done, total int, term string)) (int, error) {
	doc, err := getDocument(db, docID)
	if err != nil {
		return 0, err
	}
	lib, err := getLibrary(db, doc.LibraryID.Int64)
	if err != nil {
		return 0, err
	}
	if isPending(c, lib) {
		return 0, nil
	}
	n := recommendedCount(doc.Units, lib.Multiplier, c.Limits.Density, c.Limits.Floor, c.Limits.HardCap)
	llm, err := llmFromConfig("explain_model")
	if err != nil {
		return 0, err
	}
	messages := termsMessages(doc.Title, doc.RawMD, n+3, 12000)
	var candidates []Candidate
	for attempt := 0; attempt < 2; attempt++ {
		raw, err := llm.Chat(messages, "", 0)
		if err != nil {
			return 0, err
		}
		candidates, err = parseCandidates(raw)
		if err == nil {
			break
		}
	}
	if candidates == nil {
		return 0, errInvalid("候选词结果两次校验失败")
	}
	picked := selectCandidates(doc.RawMD, candidates, n)
	rows := make([]termRow, 0, len(picked))
	for i, cand := range picked {
		context := cand.ContextSentence
		if context == "" {
			context = sentenceFor(cand.Term, doc.RawMD)
		}
		explanation, sources, _, err := explainOne(db, cand.Term, context, doc.LibraryID, llm, true)
		if err != nil {
			return 0, err
		}
		raw, _ := json.Marshal(sources)
		rows = append(rows, termRow{
			term: cand.Term, typ: cand.Type, importance: cand.Importance,
			paraIndex: cand.ParaIndex, explanation: explanation, sourcesJSON: string(raw),
		})
		if progress != nil {
			progress(i+1, len(picked), cand.Term)
		}
	}
	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec("DELETE FROM terms WHERE document_id=? AND manual=0", docID); err != nil {
		return 0, err
	}
	for _, row := range rows {
		if _, err := tx.Exec(`INSERT INTO terms (document_id, term, type, importance, para_index, explanation, sources_json, selected, manual)
			VALUES (?,?,?,?,?,?,?,1,0)
			ON CONFLICT(document_id, term) DO UPDATE SET explanation=excluded.explanation,
			sources_json=excluded.sources_json, selected=1`,
			docID, row.term, row.typ, row.importance, row.paraIndex, row.explanation, row.sourcesJSON); err != nil {
			return 0, err
		}
	}
	if _, err := tx.Exec("UPDATE documents SET status='terms_ready', error=NULL WHERE id=?", docID); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	addEvent(db, docID, "terms_generated", map[string]any{"count": len(rows), "limit": n})
	return len(rows), nil
}

// processDocument 导入后的完整流程：分类 → 建档（软）→ 抽词，阶段进度写全。
func processDocument(db *sql.DB, c Config, docID int64) error {
	setStage(db, docID, "classifying", 20, "AI 分类")
	if _, err := classifyDocument(db, c, docID); err != nil {
		markFailed(db, docID, err)
		return err
	}
	doc, err := getDocument(db, docID)
	if err != nil {
		markFailed(db, docID, err)
		return err
	}
	if doc.Status == "pending_class" {
		setStage(db, docID, "done", 100, "待归类")
		return nil
	}
	setStage(db, docID, "creating", 35, "语雀建档")
	note := createDocSoftly(db, c, docID)
	setStage(db, docID, "extracting", 45, "抽取候选词")
	_, err = prepareTerms(db, c, docID, func(done, total int, term string) {
		setStage(db, docID, "explaining", 45+50*done/maxInt(total, 1), termDetail(done, total, term))
	})
	if err != nil {
		markFailed(db, docID, err)
		return err
	}
	if note != "" {
		db.Exec("UPDATE documents SET error=? WHERE id=?", note, docID)
	}
	setStage(db, docID, "done", 100, "待审核")
	return nil
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func termDetail(done, total int, term string) string {
	return itoa(done) + "/" + itoa(total) + " " + term
}

// retryDocument 重跑一篇：重置状态 → 分类（缺失时）→ 建档 → 抽词。
func retryDocument(db *sql.DB, c Config, docID int64) (string, error) {
	db.Exec("UPDATE documents SET status='imported', error=NULL WHERE id=?", docID)
	doc, err := getDocument(db, docID)
	if err != nil {
		return "", err
	}
	if !doc.LibraryID.Valid {
		if _, err := classifyDocument(db, c, docID); err != nil {
			markFailed(db, docID, err)
			return "", err
		}
		if doc, err = getDocument(db, docID); err != nil {
			return "", err
		}
	}
	note := ""
	if !doc.YuqueDocID.Valid {
		note = createDocSoftly(db, c, docID)
		if doc, err = getDocument(db, docID); err != nil {
			return "", err
		}
	}
	status := "classified"
	if doc.YuqueDocID.Valid {
		status = "created"
	}
	if lib, err := getLibrary(db, doc.LibraryID.Int64); err == nil && isPending(c, lib) {
		status = "pending_class"
	} else {
		if _, err := prepareTerms(db, c, docID, nil); err != nil {
			markFailed(db, docID, err)
			return "", err
		}
		status = "terms_ready"
	}
	db.Exec("UPDATE documents SET status=? WHERE id=?", status, docID)
	if note != "" {
		db.Exec("UPDATE documents SET error=? WHERE id=?", note, docID)
	}
	setStage(db, docID, "done", 100, pick(statusLabels, status))
	return status, nil
}

// saveTerms 保存勾选与释义编辑。
func saveTerms(db *sql.DB, docID int64, selected map[int64]bool, explanations map[int64]string) {
	rows, err := db.Query("SELECT id FROM terms WHERE document_id=?", docID)
	if err != nil {
		return
	}
	var ids []int64
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()
	for _, id := range ids {
		sel := 0
		if selected[id] {
			sel = 1
		}
		if text, ok := explanations[id]; ok && strings.TrimSpace(text) != "" {
			db.Exec("UPDATE terms SET selected=?, explanation=? WHERE id=?", sel, text, id)
		} else {
			db.Exec("UPDATE terms SET selected=? WHERE id=?", sel, id)
		}
	}
}

// failOverdue 把超过 deadline 仍停在中间态的文章收口为 failed（对应 Python 的 fail_overdue）。
func failOverdue(db *sql.DB, c Config) int {
	res, err := db.Exec(`UPDATE documents SET status='failed', error='处理超时，已自动标记失败',
		stage='failed', stage_pct=100, stage_detail='超时'
		WHERE deadline_at IS NOT NULL AND deadline_at < datetime('now')
		AND status IN ('imported','classified','created')`)
	if err != nil {
		return 0
	}
	n, _ := res.RowsAffected()
	return int(n)
}
