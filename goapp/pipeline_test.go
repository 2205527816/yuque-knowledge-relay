package main

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := openDB(filepath.Join(t.TempDir(), "kb.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := initDB(db); err != nil {
		t.Fatal(err)
	}
	if len(pages) == 0 {
		if err := loadTemplates(); err != nil {
			t.Fatal(err)
		}
	}
	db.Exec("INSERT INTO libraries (id,name,yuque_namespace,yuque_slug) VALUES (1,'编程','ns','repo')")
	t.Cleanup(func() { db.Close() })
	return db
}

func seedDoc(t *testing.T, db *sql.DB, body string) int64 {
	t.Helper()
	res, err := db.Exec(`INSERT INTO documents (title, raw_md, content_hash, units, status, library_id)
		VALUES ('测试', ?, ?, 10, 'terms_ready', 1)`, body, contentHash(body))
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

func testConfig() Config {
	var c Config
	c.Yuque.Session = "sess"
	c.Yuque.Ctoken = "ctok"
	c.Yuque.PendingLibrary = "待定"
	return c
}

type pipeStub struct {
	remote      RemoteDoc
	createErr   error
	created     int
	prepended   int
	updated     string
	createdBody string
}

func (s *pipeStub) CreateDoc(ns, repo, title, body, slug string) (DocRef, error) {
	s.createdBody = body
	s.created++
	if s.createErr != nil {
		return DocRef{}, s.createErr
	}
	return DocRef{ID: 100}, nil
}
func (s *pipeStub) GetDoc(ns, repo string, id int64) (RemoteDoc, error) { return s.remote, nil }
func (s *pipeStub) UpdateDoc(ns, repo string, id int64, title, body string) error {
	s.updated = body
	return nil
}
func (s *pipeStub) DeleteDoc(ns, repo string, id int64) error { return nil }
func (s *pipeStub) GetTOC(ns, repo string) ([]TOCNode, error) {
	return []TOCNode{{DocID: 100}}, nil
}
func (s *pipeStub) PrependTOC(ns, repo string, id int64) error { s.prepended++; return nil }

func withStub(t *testing.T, stub *pipeStub) {
	t.Helper()
	old := clientsFor
	clientsFor = func(Config) YuqueClient { return stub }
	t.Cleanup(func() { clientsFor = old; yuqueB.reset() })
}

func TestCreateDocWritesIDAndTOC(t *testing.T) {
	db := testDB(t)
	body := "机器学习很重要。"
	id := seedDoc(t, db, body)
	stub := &pipeStub{remote: RemoteDoc{ID: 100, Format: "markdown", Body: &body}}
	withStub(t, stub)

	docID, err := createDoc(db, testConfig(), id)
	if err != nil {
		t.Fatal(err)
	}
	if docID != 100 {
		t.Fatalf("doc id %d", docID)
	}
	doc, _ := getDocument(db, id)
	if !doc.YuqueDocID.Valid || doc.YuqueDocID.Int64 != 100 || doc.Status != "created" {
		t.Fatalf("数据库状态不对: %+v", doc)
	}
	if stub.created != 1 || stub.prepended != 1 {
		t.Fatalf("接口调用次数不对: create=%d toc=%d", stub.created, stub.prepended)
	}
}

func TestWriteBackDetectsRemoteEdit(t *testing.T) {
	db := testDB(t)
	id := seedDoc(t, db, "机器学习很重要。")
	db.Exec("UPDATE documents SET yuque_doc_id=100, yuque_body_hash=? WHERE id=?", contentHash("原始正文"), id)
	changed := "用户改过的正文"
	stub := &pipeStub{remote: RemoteDoc{ID: 100, Format: "markdown", Body: &changed}}
	withStub(t, stub)

	_, err := writeBack(db, testConfig(), id, []Annotation{{Term: "机器学习", Explanation: "从数据学规律"}}, false)
	if _, ok := err.(*YuqueConflict); !ok {
		t.Fatalf("应报冲突，得到 %v", err)
	}

	if _, err := writeBack(db, testConfig(), id, []Annotation{{Term: "机器学习", Explanation: "从数据学规律"}}, true); err != nil {
		t.Fatalf("强制覆盖失败: %v", err)
	}
	doc, _ := getDocument(db, id)
	if doc.Status != "written" || !strings.Contains(stub.updated, "***机器学习***") {
		t.Fatalf("写回结果不对: status=%s body=%s", doc.Status, stub.updated)
	}
}

func TestWriteBackCookieModeSkipsBodyCheck(t *testing.T) {
	db := testDB(t)
	id := seedDoc(t, db, "机器学习很重要。")
	db.Exec("UPDATE documents SET yuque_doc_id=100, yuque_body_hash=? WHERE id=?", contentHash("完全不同的正文"), id)
	stub := &pipeStub{remote: RemoteDoc{ID: 100, Format: "markdown", Body: nil}}
	withStub(t, stub)
	if _, err := writeBack(db, testConfig(), id, []Annotation{{Term: "机器学习", Explanation: "x"}}, false); err != nil {
		t.Fatalf("cookie 模式不应报冲突: %v", err)
	}
}

func TestCreateDocSoftlyTripsBreaker(t *testing.T) {
	db := testDB(t)
	id := seedDoc(t, db, "机器学习很重要。")
	yuqueB.reset()
	stub := &pipeStub{createErr: &YuqueError{"429 Too Many Requests"}}
	oldClients := clientsFor
	clientsFor = func(Config) YuqueClient { return breakerGuard{inner: stub} }
	t.Cleanup(func() { clientsFor = oldClients; yuqueB.reset() })

	note := createDocSoftly(db, testConfig(), id)
	if !strings.Contains(note, "限流") {
		t.Fatalf("应提示限流: %s", note)
	}
	if !yuqueB.blocked() {
		t.Fatal("应按 429 熔断")
	}
	before := stub.created
	if note2 := createDocSoftly(db, testConfig(), id); !strings.Contains(note2, "限流") {
		t.Fatalf("熔断期内也应提示: %s", note2)
	}
	if stub.created != before {
		t.Fatal("熔断期内不应再调语雀")
	}
}
