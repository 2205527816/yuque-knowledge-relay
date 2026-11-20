package main

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func backupCfg(dir string) Config {
	c := testConfig()
	c.Backup = map[string]any{
		"enabled":  true,
		"dry_run":  true,
		"dir":      dir,
		"repo":     "me/repo",
		"required": false,
		"order":    "before",
	}
	return c
}

func TestBackupPathAndMarkdown(t *testing.T) {
	doc := Document{Title: "测试/标题:引号", ContentHash: "0123456789abcdef", SourceURL: "https://x.com/a"}
	path := backupPath(doc, "编程")
	if path != "kb/编程/01234567-测试-标题-引号.md" {
		t.Fatalf("路径不对: %s", path)
	}
	md := buildBackupMarkdown(doc, "编程", "正文\n", "https://www.yuque.com/ns/repo/kb-0123456789", 3)
	for _, want := range []string{"title: 测试/标题:引号", "library: 编程", "terms: 3", "yuque_url:", "正文"} {
		if !strings.Contains(md, want) {
			t.Fatalf("备份内容缺 %q:\n%s", want, md)
		}
	}
}

func TestBackupDryRunWritesFileAndRow(t *testing.T) {
	db := testDB(t)
	id := seedDoc(t, db, "机器学习很重要。")
	doc, _ := getDocument(db, id)
	dir := t.TempDir()
	res := backupArticle(db, backupCfg(dir), doc, "编程", "渲染后的正文\n", "https://www.yuque.com/x/y/kb-1", 2)
	if res == nil || res["status"] != "dry_run" {
		t.Fatalf("dry_run 结果不对: %v", res)
	}
	full := filepath.Join(dir, res["path"].(string))
	raw, err := os.ReadFile(full)
	if err != nil || !strings.Contains(string(raw), "渲染后的正文") {
		t.Fatalf("备份文件没写对: %v", err)
	}
	var status string
	if err := db.QueryRow("SELECT status FROM backups WHERE document_id=?", id).Scan(&status); err != nil || status != "dry_run" {
		t.Fatalf("backups 表没记录: %v status=%s", err, status)
	}
}

func TestWriteBackHonorsBackupOrder(t *testing.T) {
	db := testDB(t)
	id := seedDoc(t, db, "机器学习很重要。")
	db.Exec("UPDATE documents SET yuque_doc_id=100 WHERE id=?", id)
	stub := &pipeStub{remote: RemoteDoc{ID: 100, Format: "markdown", Body: nil}}
	withStub(t, stub)
	dir := t.TempDir()
	if _, err := writeBack(db, backupCfg(dir), id, []Annotation{{Term: "机器学习", Explanation: "释义"}}, false); err != nil {
		t.Fatalf("写回失败: %v", err)
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "kb", "*", "*.md"))
	if len(matches) != 1 {
		t.Fatalf("应该生成 1 个备份文件，实际 %d", len(matches))
	}
}

func TestRecoverInterruptedJobs(t *testing.T) {
	db := testDB(t)
	id := seedDoc(t, db, "机器学习很重要。")
	db.Exec("UPDATE documents SET stage='classifying', stage_pct=20 WHERE id=?", id)
	if n := recoverInterruptedJobs(db); n != 1 {
		t.Fatalf("应恢复 1 篇，实际 %d", n)
	}
	doc, _ := getDocument(db, id)
	if doc.Status != "failed" || !doc.Stage.Valid || doc.Stage.String != "failed" {
		t.Fatalf("恢复结果不对: status=%s stage=%v", doc.Status, doc.Stage)
	}
}

// 相同内容不应产生新提交；内容变化才 PUT。
func TestGithubPutSkipsUnchanged(t *testing.T) {
	content := "正文内容"
	encoded := base64.StdEncoding.EncodeToString([]byte(content))
	puts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Write([]byte(`{"sha":"old","content":"` + encoded + `"}`))
			return
		}
		puts++
		w.Write([]byte(`{"commit":{"sha":"newsha"}}`))
	}))
	defer srv.Close()
	old := githubAPIBase
	githubAPIBase = srv.URL
	defer func() { githubAPIBase = old }()

	commit, err := githubPut("tok", "me/repo", "main", "a.md", content, "msg")
	if err != nil || commit != "" || puts != 0 {
		t.Fatalf("内容未变应跳过 PUT: commit=%q err=%v puts=%d", commit, err, puts)
	}
	commit, err = githubPut("tok", "me/repo", "main", "a.md", content+"改", "msg")
	if err != nil || commit != "newsha" || puts != 1 {
		t.Fatalf("内容变了应 PUT: commit=%q err=%v puts=%d", commit, err, puts)
	}
}
