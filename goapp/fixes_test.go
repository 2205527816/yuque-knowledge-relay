package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 客户端层已 trip，soft wrapper 不应再 trip 一次（否则 60s 直接跳 300s）。
func TestSingle429TripsOnce(t *testing.T) {
	db := testDB(t)
	id := seedDoc(t, db, "机器学习很重要。")
	yuqueB.reset()
	stub := &pipeStub{createErr: &YuqueError{"429 Too Many Requests"}}
	old := clientsFor
	clientsFor = func(Config) YuqueClient { return breakerGuard{inner: stub} }
	t.Cleanup(func() { clientsFor = old; yuqueB.reset() })

	if note := createDocSoftly(db, testConfig(), id); !strings.Contains(note, "429") {
		t.Fatalf("应提示限流: %s", note)
	}
	if rem := yuqueB.remaining(); rem > 70*time.Second {
		t.Fatalf("重复 trip 了：剩余 %v（应约 60s）", rem)
	}
}

// 语雀响应缺少 data 时必须报错，不能落到空 ID。
func TestMissingDataIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	api := newYuqueAPI(srv.URL, "tok")
	if _, err := api.CreateDoc("ns", "repo", "t", "b", "s"); err == nil || !strings.Contains(err.Error(), "缺少 data") {
		t.Fatalf("期望缺少 data 错误，得到 %v", err)
	}
}

// 超时收口：deadline 已过且停在 imported → failed。
func TestFailOverdue(t *testing.T) {
	db := testDB(t)
	id := seedDoc(t, db, "机器学习很重要。")
	db.Exec("UPDATE documents SET status='imported', deadline_at=datetime('now','-5 minutes') WHERE id=?", id)
	if n := failOverdue(db, testConfig()); n != 1 {
		t.Fatalf("应收口 1 篇，实际 %d", n)
	}
	doc, _ := getDocument(db, id)
	if doc.Status != "failed" {
		t.Fatalf("状态没改: %s", doc.Status)
	}
}

// 写回作业落盘后可恢复。
func TestPendingWritesPersistAndReload(t *testing.T) {
	db := testDB(t)
	id := seedDoc(t, db, "机器学习很重要。")
	if err := persistWrite(db, id, true); err != nil {
		t.Fatal(err)
	}
	jobs := loadPendingWrites(db)
	if len(jobs) != 1 || jobs[0].docID != id || !jobs[0].force {
		t.Fatalf("恢复结果不对: %+v", jobs)
	}
	clearPersistedWrite(db, id)
	if jobs := loadPendingWrites(db); len(jobs) != 0 {
		t.Fatalf("清理后不应有作业: %+v", jobs)
	}
}

// SimpRead 批次复用：已有运行中批次时不再新建。
func TestSimpreadBatchReuse(t *testing.T) {
	db := testDB(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "a.md")
	os.WriteFile(path, []byte("---\ntitle: 甲\nurl: https://e.com/a\n---\n\n正文。\n"), 0o644)
	res, _ := db.Exec("INSERT INTO batches (source, total, status) VALUES ('simpread', 1, 'running')")
	existing, _ := res.LastInsertId()
	if got := startBatchWithSource(db, testConfig(), []string{path}, "simpread"); got != existing {
		t.Fatalf("应复用批次 %d，实际 %d", existing, got)
	}
	var source string
	db.QueryRow("SELECT source FROM batches WHERE id=?", existing).Scan(&source)
	if source != "simpread" {
		t.Fatalf("批次 source 不对: %s", source)
	}
}

// force 只能升不能降。
func TestPersistWriteKeepsForce(t *testing.T) {
	db := testDB(t)
	id := seedDoc(t, db, "机器学习很重要。")
	persistWrite(db, id, false)
	persistWrite(db, id, true)
	persistWrite(db, id, false)
	var force int
	db.QueryRow("SELECT force FROM pending_writes WHERE document_id=?", id).Scan(&force)
	if force != 1 {
		t.Fatalf("force 被降级了: %d", force)
	}
}

// 失败时保留作业，达到上限才放弃。
func TestWriteJobKeptOnFailure(t *testing.T) {
	db := testDB(t)
	id := seedDoc(t, db, "机器学习很重要。")
	db.Exec("UPDATE documents SET yuque_doc_id=100 WHERE id=?", id)
	stub := &pipeStub{remote: RemoteDoc{ID: 100, Format: "lake"}}
	withStub(t, stub)
	resetWriteWorker()
	startWriteWorker(db, testConfig())
	persistWrite(db, id, false)
	enqueueWrite(writeJob{docID: id})

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var attempts int
		err := db.QueryRow("SELECT attempts FROM pending_writes WHERE document_id=?", id).Scan(&attempts)
		if err == nil && attempts >= 1 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("失败后作业未保留")
}

// 成功写回后清掉作业行。
func TestWriteJobClearedOnSuccess(t *testing.T) {
	db := testDB(t)
	id := seedDoc(t, db, "机器学习很重要。")
	db.Exec("UPDATE documents SET yuque_doc_id=100 WHERE id=?", id)
	stub := &pipeStub{remote: RemoteDoc{ID: 100, Format: "markdown"}}
	withStub(t, stub)
	resetWriteWorker()
	startWriteWorker(db, testConfig())
	persistWrite(db, id, false)
	enqueueWrite(writeJob{docID: id})

	// 写回先置 written，随后才清作业行；轮询两个条件同时成立
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var n int
		db.QueryRow("SELECT COUNT(*) FROM pending_writes WHERE document_id=?", id).Scan(&n)
		doc, _ := getDocument(db, id)
		if n == 0 && doc.Status == "written" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("写回成功后作业未清理")
}
