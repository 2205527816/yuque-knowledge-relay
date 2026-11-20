package main

import (
	"testing"
	"time"
)

func TestWriteQueueProcessesJob(t *testing.T) {
	db := testDB(t)
	id := seedDoc(t, db, "机器学习很重要。")
	db.Exec("UPDATE documents SET yuque_doc_id=100 WHERE id=?", id)
	stub := &pipeStub{remote: RemoteDoc{ID: 100, Format: "markdown", Body: nil}}
	withStub(t, stub)
	resetWriteWorker()
	startWriteWorker(db, testConfig())

	if !enqueueWrite(writeJob{docID: id, anns: []Annotation{{Term: "机器学习", Explanation: "释义"}}}) {
		t.Fatal("入队失败")
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		doc, _ := getDocument(db, id)
		if doc.Status == "written" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("后台写回超时未完成")
}

func TestWriteQueueMarksFailure(t *testing.T) {
	db := testDB(t)
	id := seedDoc(t, db, "机器学习很重要。")
	db.Exec("UPDATE documents SET yuque_doc_id=100 WHERE id=?", id)
	stub := &pipeStub{remote: RemoteDoc{ID: 100, Format: "markdown", Body: nil}}
	withStub(t, stub)
	yuqueB.reset()
	resetWriteWorker()
	startWriteWorker(db, testConfig())

	// 让远端呈 lake 格式且不 force：写回会报冲突
	stub.remote = RemoteDoc{ID: 100, Format: "lake"}
	if !enqueueWrite(writeJob{docID: id, anns: nil}) {
		t.Fatal("入队失败")
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		doc, _ := getDocument(db, id)
		if doc.Status == "failed" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("失败没有被收口")
}
