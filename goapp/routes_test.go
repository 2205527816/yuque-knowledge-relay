package main

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRetentionHandlerMovesToTrash(t *testing.T) {
	db := testDB(t)
	id := seedDoc(t, db, "机器学习很重要。")
	req := httptest.NewRequest("POST", "/docs/1/retention", strings.NewReader("retention_status=trash"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("id", fmt.Sprint(id))
	rec := httptest.NewRecorder()
	retentionHandler(db)(rec, req)
	if rec.Code != 303 {
		t.Fatalf("状态码 %d", rec.Code)
	}
	doc, _ := getDocument(db, id)
	if doc.Retention != "trash" {
		t.Fatalf("保留状态没改: %s", doc.Retention)
	}
}

func TestSaveHandlerUpdatesSelection(t *testing.T) {
	db := testDB(t)
	id := seedDoc(t, db, "机器学习很重要。")
	res, _ := db.Exec(`INSERT INTO terms (document_id, term, explanation, selected) VALUES (?, '机器学习', '旧释义', 1)`, id)
	termID, _ := res.LastInsertId()
	form := fmt.Sprintf("explanation_%d=新释义", termID)
	req := httptest.NewRequest("POST", "/docs/1/save", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("id", fmt.Sprint(id))
	rec := httptest.NewRecorder()
	saveHandler(db)(rec, req)
	var explanation string
	var selected int
	db.QueryRow("SELECT explanation, selected FROM terms WHERE id=?", termID).Scan(&explanation, &selected)
	if explanation != "新释义" || selected != 0 {
		t.Fatalf("保存没生效: %q selected=%d", explanation, selected)
	}
}

func TestProgressHandlerJSON(t *testing.T) {
	db := testDB(t)
	id := seedDoc(t, db, "机器学习很重要。")
	db.Exec("INSERT INTO batches (id, status, total) VALUES (7, 'finished', 1)")
	db.Exec("UPDATE documents SET batch_id=7 WHERE id=?", id)
	req := httptest.NewRequest("GET", "/api/progress", nil)
	rec := httptest.NewRecorder()
	progressHandler(db)(rec, req)
	var out struct {
		Batch struct {
			ID    int64 `json:"id"`
			Total int   `json:"total"`
		} `json:"batch"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("JSON 解析失败: %v (%s)", err, rec.Body.String())
	}
	if out.Batch.ID != 7 || out.Batch.Total != 1 {
		t.Fatalf("进度数据不对: %+v", out.Batch)
	}
}
