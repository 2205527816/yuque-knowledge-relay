package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

var processLock sync.Mutex

func setStage(db *sql.DB, docID int64, stage string, pct int, detail string) {
	db.Exec("UPDATE documents SET stage=?, stage_pct=?, stage_detail=? WHERE id=?", stage, pct, truncate(detail, 160), docID)
}

func redirect(w http.ResponseWriter, r *http.Request, target, msg, errMsg string) {
	q := []string{}
	if msg != "" {
		q = append(q, "msg="+urlQueryEscape(msg))
	}
	if errMsg != "" {
		q = append(q, "error="+urlQueryEscape(errMsg))
	}
	if len(q) > 0 {
		sep := "?"
		if strings.Contains(target, "?") {
			sep = "&"
		}
		target += sep + strings.Join(q, "&")
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func nextDocURL(db *sql.DB, current int64) (string, bool) {
	var id int64
	err := db.QueryRow(`SELECT id FROM documents WHERE id<? AND retention_status='active'
		AND status IN ('terms_ready','pending_class','failed')
		AND (status='failed' OR NOT EXISTS (SELECT 1 FROM pending_writes pw WHERE pw.document_id=documents.id))
		ORDER BY id DESC LIMIT 1`, current).Scan(&id)
	if err != nil {
		return "/?done=1", false
	}
	return fmt.Sprintf("/docs/%d", id), true
}

func parseTermForm(r *http.Request) (map[int64]bool, map[int64]string) {
	r.ParseMultipartForm(32 << 20)
	selected := map[int64]bool{}
	for _, v := range r.Form["term_ids"] {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil {
			selected[id] = true
		}
	}
	explanations := map[int64]string{}
	for key, vals := range r.Form {
		if strings.HasPrefix(key, "explanation_") && len(vals) > 0 {
			if id, err := strconv.ParseInt(strings.TrimPrefix(key, "explanation_"), 10, 64); err == nil {
				explanations[id] = vals[0]
			}
		}
	}
	return selected, explanations
}

func saveHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
		selected, explanations := parseTermForm(r)
		saveTerms(db, id, selected, explanations)
		redirect(w, r, fmt.Sprintf("/docs/%d", id), "已保存", "")
	}
}

func writeHandler(db *sql.DB, c Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
		r.ParseMultipartForm(32 << 20)
		selected, explanations := parseTermForm(r)
		if len(selected) > 0 || len(explanations) > 0 {
			saveTerms(db, id, selected, explanations)
		}
		force := r.FormValue("force") == "1"
		anns, err := selectedTerms(db, id)
		if err != nil {
			redirect(w, r, fmt.Sprintf("/docs/%d", id), "", "读取词条失败："+err.Error())
			return
		}
		if err := persistWrite(db, id, force); err != nil {
			log.Printf("记录写回作业失败 doc=%d: %v", id, err)
		}
		if !enqueueWrite(writeJob{docID: id, anns: anns, force: force}) {
			redirect(w, r, fmt.Sprintf("/docs/%d", id), "", "写回队列已满，稍后重试")
			return
		}
		msg := "已提交写回"
		if target, ok := nextDocURL(db, id); ok {
			redirect(w, r, target, msg+"，继续下一篇", "")
			return
		}
		redirect(w, r, "/?done=1", msg, "")
	}
}

func retryHandler(db *sql.DB, c Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
		processLock.Lock()
		status, err := retryDocument(db, c, id)
		processLock.Unlock()
		if err != nil {
			redirect(w, r, fmt.Sprintf("/docs/%d", id), "", "重试失败："+err.Error())
			return
		}
		redirect(w, r, fmt.Sprintf("/docs/%d", id), "重试完成："+pick(statusLabels, status), "")
	}
}

func retentionHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
		status := r.FormValue("retention_status")
		if status != "active" && status != "archived" && status != "trash" {
			redirect(w, r, fmt.Sprintf("/docs/%d", id), "", "保留状态不合法")
			return
		}
		if status == "active" {
			db.Exec("UPDATE documents SET retention_status=?, archived_at=NULL, deleted_at=NULL WHERE id=?", status, id)
		} else if status == "trash" {
			db.Exec("UPDATE documents SET retention_status=?, deleted_at=datetime('now') WHERE id=?", status, id)
		} else {
			db.Exec("UPDATE documents SET retention_status=?, archived_at=datetime('now') WHERE id=?", status, id)
		}
		if status == "active" {
			redirect(w, r, fmt.Sprintf("/docs/%d", id), "已恢复到进行中", "")
			return
		}
		if target, ok := nextDocURL(db, id); ok {
			redirect(w, r, target, "已处理，继续下一篇", "")
			return
		}
		redirect(w, r, "/?done=1", "已处理", "")
	}
}

func batchHandler(db *sql.DB, c Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.ParseMultipartForm(8 << 20)
		action := r.FormValue("action")
		q := strings.TrimSpace(r.FormValue("q"))
		libID, _ := strconv.ParseInt(r.FormValue("lib_id"), 10, 64)
		retention := r.FormValue("retention")
		if retention != "active" && retention != "archived" && retention != "trash" && retention != "all" {
			retention = "active"
		}
		page, _ := strconv.Atoi(r.FormValue("page"))
		if page < 1 {
			page = 1
		}
		perPage, _ := strconv.Atoi(r.FormValue("per_page"))
		if perPage < 1 || perPage > 100 {
			perPage = 20
		}
		target := keepQueueStatus(listURL(q, libID, retention, perPage, page), queueStatus(r.FormValue("status")))
		ids := []int64{}
		for _, v := range r.Form["doc_ids"] {
			if id, err := strconv.ParseInt(v, 10, 64); err == nil {
				ids = append(ids, id)
			}
		}
		switch action {
		case "retry_failed_active":
			rows, err := db.Query("SELECT id FROM documents WHERE retention_status='active' AND status='failed' ORDER BY id")
			if err != nil {
				redirect(w, r, target, "", err.Error())
				return
			}
			var failedIDs []int64
			for rows.Next() {
				var id int64
				rows.Scan(&id)
				failedIDs = append(failedIDs, id)
			}
			rows.Close()
			bad := 0
			for _, id := range failedIDs {
				if _, err := retryDocument(db, c, id); err != nil {
					bad++
				}
			}
			redirect(w, r, target, fmt.Sprintf("重试完成 %d 篇，失败 %d 篇", len(failedIDs)-bad, bad), "")
			return
		case "archive", "trash", "activate":
			for _, id := range ids {
				switch action {
				case "archive":
					db.Exec("UPDATE documents SET retention_status='archived', archived_at=datetime('now') WHERE id=?", id)
				case "trash":
					db.Exec("UPDATE documents SET retention_status='trash', deleted_at=datetime('now') WHERE id=?", id)
				default:
					db.Exec("UPDATE documents SET retention_status='active', archived_at=NULL, deleted_at=NULL WHERE id=?", id)
				}
			}
		case "purge":
			deleted := 0
			for _, id := range ids {
				res, err := db.Exec("DELETE FROM documents WHERE id=? AND retention_status='trash'", id)
				if err != nil {
					continue
				}
				if n, err := res.RowsAffected(); err == nil {
					deleted += int(n)
				}
			}
			if deleted == 0 {
				redirect(w, r, target, "", "只能永久删除回收站里的文章")
				return
			}
			redirect(w, r, target, fmt.Sprintf("已永久删除 %d 篇", deleted), "")
			return
		default:
			redirect(w, r, target, "", "未知批量操作")
			return
		}
		redirect(w, r, target, fmt.Sprintf("已对 %d 篇执行批量操作", len(ids)), "")
	}
}

func retryAllHandler(db *sql.DB, c Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rows, err := db.Query("SELECT id FROM documents WHERE retention_status='active' AND status='failed' ORDER BY id")
		if err != nil {
			redirect(w, r, "/", "", err.Error())
			return
		}
		var ids []int64
		for rows.Next() {
			var id int64
			rows.Scan(&id)
			ids = append(ids, id)
		}
		rows.Close()
		failed := 0
		for _, id := range ids {
			if _, err := retryDocument(db, c, id); err != nil {
				failed++
			}
		}
		redirect(w, r, "/", fmt.Sprintf("重试完成 %d 篇，失败 %d 篇", len(ids)-failed, failed), "")
	}
}

func uploadHandler(db *sql.DB, c Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		file, header, err := r.FormFile("file")
		if err != nil {
			redirect(w, r, "/", "", "请选择文件")
			return
		}
		defer file.Close()
		ext := strings.ToLower(filepath.Ext(header.Filename))
		if ext != ".md" && ext != ".html" && ext != ".htm" {
			redirect(w, r, "/", "", "只收 .md / .html / .htm")
			return
		}
		data, err := io.ReadAll(io.LimitReader(file, 10<<20))
		if err != nil {
			redirect(w, r, "/", "", "读取上传失败")
			return
		}
		os.MkdirAll(inboxDir(), 0o755)
		name := fmt.Sprintf("upload-%s-%d%s", time.Now().Format("20060102-150405"), time.Now().UnixNano()%1e6, ext)
		path := filepath.Join(inboxDir(), name)
		if err := os.WriteFile(path, data, 0o644); err != nil {
			redirect(w, r, "/", "", "写入 inbox 失败")
			return
		}
		batchID := startBatch(db, c, path)
		redirect(w, r, fmt.Sprintf("/?batch=%d", batchID), "已接收，开始处理", "")
	}
}

func scanHandler(db *sql.DB, c Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		entries, err := os.ReadDir(inboxDir())
		if err != nil {
			redirect(w, r, "/", "", "inbox 不可读")
			return
		}
		var paths []string
		for _, e := range entries {
			ext := strings.ToLower(filepath.Ext(e.Name()))
			if !e.IsDir() && (ext == ".md" || ext == ".html" || ext == ".htm") {
				paths = append(paths, filepath.Join(inboxDir(), e.Name()))
			}
		}
		if len(paths) == 0 {
			redirect(w, r, "/", "inbox 里没有新文件", "")
			return
		}
		batchID := startBatchMany(db, c, paths)
		redirect(w, r, fmt.Sprintf("/?batch=%d", batchID), fmt.Sprintf("已接收 %d 个文件", len(paths)), "")
	}
}

func startBatch(db *sql.DB, c Config, path string) int64 {
	return startBatchWithSource(db, c, []string{path}, "inbox")
}

func startBatchMany(db *sql.DB, c Config, paths []string) int64 {
	return startBatchWithSource(db, c, paths, "inbox")
}

type batchJob struct {
	batchID int64
	ids     []int64
}

var (
	batchCh   chan batchJob
	batchOnce sync.Once
)

// startProcessWorker 单工消费批次：批次间严格 FIFO，批次内也串行。
func startProcessWorker(db *sql.DB, c Config) {
	batchOnce.Do(func() {
		batchCh = make(chan batchJob, 32)
		go func() {
			for job := range batchCh {
				for _, id := range job.ids {
					if err := processDocument(db, c, id); err != nil {
						setStage(db, id, "failed", 100, "失败")
					}
				}
				var left int
				db.QueryRow("SELECT COUNT(*) FROM documents WHERE batch_id=? AND stage NOT IN ('done','failed')", job.batchID).Scan(&left)
				if left == 0 {
					db.Exec("UPDATE batches SET status='finished', finished_at=datetime('now') WHERE id=?", job.batchID)
				}
			}
		}()
	})
}

func resetProcessWorker() {
	batchOnce = sync.Once{}
	batchCh = nil
}

// startBatchMany 建批次、入库，然后交给单工队列；total 按实际新建数量回写。
func startBatchWithSource(db *sql.DB, c Config, paths []string, source string) int64 {
	var batchID int64
	createdBatch := false
	if source == "simpread" {
		if err := db.QueryRow("SELECT id FROM batches WHERE source='simpread' AND status='running' ORDER BY id DESC LIMIT 1").Scan(&batchID); err != nil {
			batchID = 0
		}
	}
	if batchID == 0 {
		res, _ := db.Exec("INSERT INTO batches (source, total) VALUES (?, 0)", source)
		batchID, _ = res.LastInsertId()
		createdBatch = true
	}
	var ids []int64
	for _, p := range paths {
		id, created, err := importFile(db, c, p)
		if err != nil {
			continue
		}
		if created {
			db.Exec("UPDATE documents SET batch_id=?, stage='queued', stage_pct=5, stage_detail='排队中' WHERE id=?", batchID, id)
			ids = append(ids, id)
		}
		archiveInput(p)
	}
	var total int
	db.QueryRow("SELECT COUNT(*) FROM documents WHERE batch_id=?", batchID).Scan(&total)
	db.Exec("UPDATE batches SET total=? WHERE id=?", total, batchID)
	if len(ids) == 0 {
		if createdBatch {
			db.Exec("UPDATE batches SET status='finished', finished_at=datetime('now') WHERE id=?", batchID)
		}
		return batchID
	}
	startProcessWorker(db, c)
	if batchCh != nil {
		batchCh <- batchJob{batchID: batchID, ids: ids}
	}
	return batchID
}

// archiveInput 把已入库的输入文件搬到 archive；搬不动也不影响导入。
func archiveInput(path string) {
	if path == "" {
		return
	}
	if err := os.MkdirAll(archiveDir(), 0o755); err != nil {
		return
	}
	dest := filepath.Join(archiveDir(), filepath.Base(path))
	if _, err := os.Stat(dest); err == nil {
		dest = filepath.Join(archiveDir(), fmt.Sprintf("%s-%d%s", strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)), time.Now().UnixNano(), filepath.Ext(path)))
	}
	os.Rename(path, dest)
}

func progressHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writes := pendingWriteCount(db)
		var batch struct {
			ID     int64  `json:"id"`
			Status string `json:"status"`
			Total  int    `json:"total"`
		}
		var qerr error
		if b := r.URL.Query().Get("batch"); b != "" {
			id, convErr := strconv.ParseInt(b, 10, 64)
			if convErr != nil {
				json.NewEncoder(w).Encode(map[string]any{"batch": nil, "writes": writes})
				return
			}
			qerr = db.QueryRow("SELECT id, status, total FROM batches WHERE id=? LIMIT 1", id).Scan(&batch.ID, &batch.Status, &batch.Total)
		} else {
			qerr = db.QueryRow("SELECT id, status, total FROM batches ORDER BY id DESC LIMIT 1").Scan(&batch.ID, &batch.Status, &batch.Total)
		}
		if qerr != nil {
			json.NewEncoder(w).Encode(map[string]any{"batch": nil, "writes": writes})
			return
		}
		if batch.Total == 0 {
			json.NewEncoder(w).Encode(map[string]any{"batch": nil, "writes": writes})
			return
		}
		var done int
		db.QueryRow(`SELECT COUNT(*) FROM documents WHERE batch_id=? AND status IN ('terms_ready','pending_class','failed','written')`, batch.ID).Scan(&done)
		var cur struct {
			Title       string `json:"title"`
			StageDetail string `json:"stage_detail"`
			StagePct    int    `json:"stage_pct"`
		}
		db.QueryRow(`SELECT title, COALESCE(stage_detail,''), COALESCE(stage_pct,0) FROM documents
			WHERE batch_id=? AND stage NOT IN ('done','failed') ORDER BY id LIMIT 1`, batch.ID).
			Scan(&cur.Title, &cur.StageDetail, &cur.StagePct)
		pct := 0
		if batch.Total > 0 {
			pct = done * 100 / batch.Total
		} else if batch.Status == "finished" {
			pct = 100
		}
		json.NewEncoder(w).Encode(map[string]any{"writes": writes, "batch": map[string]any{
			"id": batch.ID, "status": batch.Status, "total": batch.Total, "done": done, "pct": pct,
			"current": cur,
		}})
	}
}

func urlQueryEscape(s string) string { return url.QueryEscape(s) }
