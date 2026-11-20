package main

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPreviewHandlerReturnsFragment(t *testing.T) {
	db := testDB(t)
	id := seedDoc(t, db, "机器学习很重要。")
	res, _ := db.Exec(`INSERT INTO terms (document_id, term, explanation, selected) VALUES (?, '机器学习', '释义', 1)`, id)
	termID, _ := res.LastInsertId()
	req := httptest.NewRequest("POST", "/docs/1/preview", strings.NewReader(fmt.Sprintf("term_ids=%d", termID)))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("id", fmt.Sprint(id))
	rec := httptest.NewRecorder()
	previewHandler(db, testConfig())(rec, req)
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, "<article") || !strings.Contains(body, "机器学习") || !strings.Contains(body, "<strong>") {
		t.Fatalf("预览片段不对 (%d): %.200s", rec.Code, body)
	}
}

func TestProfilePageAndSave(t *testing.T) {
	db := testDB(t)
	t.Setenv("KR_PROFILES", t.TempDir())
	t.Setenv("KR_ROOT", t.TempDir())
	// 直接用测试库的库名，profile 写在临时目录
	req := httptest.NewRequest("GET", "/libraries/1/profile", nil)
	req.SetPathValue("id", "1")
	rec := httptest.NewRecorder()
	profilePageHandler(db)(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "分类档案") {
		t.Fatalf("档案页不对 (%d)", rec.Code)
	}
}

func TestLibraryMultiplierValidation(t *testing.T) {
	db := testDB(t)
	req := httptest.NewRequest("POST", "/libraries/1", strings.NewReader("multiplier=3"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("id", "1")
	rec := httptest.NewRecorder()
	librarySettingsHandler(db)(rec, req)
	var mult float64
	db.QueryRow("SELECT multiplier FROM libraries WHERE id=1").Scan(&mult)
	if mult != 1.0 {
		t.Fatalf("越界倍率不应写入: %v", mult)
	}
}
