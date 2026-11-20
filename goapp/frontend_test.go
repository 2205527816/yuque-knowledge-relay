package main

import (
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// Render the real forms with representative data to guard their route and field contracts.
func TestProductionInterfaceRoutes(t *testing.T) {
	if err := loadTemplates(); err != nil {
		t.Fatal(err)
	}
	doc := Document{ID: 42, Title: "验收文章", LibraryName: "研学", Status: "terms_ready", Retention: "active"}
	cases := []struct {
		page     string
		data     pageData
		required []string
	}{
		{"index", pageData{Docs: []Document{doc}, Retention: "active", HasFailed: true}, []string{
			"action=\"/upload\"", "action=\"/scan\"", "action=\"/docs/batch\"", "name=\"doc_ids\"",
			"href=\"/docs/42\"", "seals.svg#seal-12", "name=\"status\"",
		}},
		{"doc", pageData{Doc: doc, Conflict: true}, []string{
			"id=\"review-reader-scroll\"", "id=\"review-notes-scroll\"", "aria-controls=\"review-reader-scroll review-notes-scroll\"",
			"formaction=\"/docs/42/save\"", "action=\"/docs/42/terms/add\"", "name=\"force\" value=\"1\"",
			"action=\"/docs/42/write\"", "action=\"/docs/42/library\"",
		}},
		{"libraries", pageData{Libraries: []Library{{ID: 13, Name: "研学"}}}, []string{
			"href=\"/libraries/13/profile\"", "seals.svg#seal-12",
		}},
		{"profile", pageData{Library: &Library{ID: 13, Name: "研学"}}, []string{
			"action=\"/libraries/13/profile\"", "action=\"/libraries/13\"", "name=\"multiplier\"", "name=\"content\"",
		}},
	}
	for _, tc := range cases {
		t.Run(tc.page, func(t *testing.T) {
			w := httptest.NewRecorder()
			render(w, tc.page, tc.data)
			html := w.Body.String()
			for _, marker := range tc.required {
				if !strings.Contains(html, marker) {
					t.Errorf("missing %q", marker)
				}
			}
			if !strings.Contains(html, "</html>") {
				t.Fatal("template stopped before rendering the full page")
			}
		})
	}
}

// 回归：网页逐行换行要合回段落，但空行分段、列表、代码块必须保留。
func TestIcmReflowFixture(t *testing.T) {
	raw, err := os.ReadFile("testdata/icm_wrapped.md")
	if err != nil {
		t.Fatal(err)
	}
	out := reflowParagraphs(string(raw))
	if strings.Contains(out, "Mathematicians，\n简称 ICM") {
		t.Fatalf("同一句被拆成两行: %q", out)
	}
	paras := strings.Split(strings.TrimSpace(out), "\n\n")
	if len(paras) < 7 {
		t.Fatalf("段落被误合并，只剩 %d 段", len(paras))
	}
	if !strings.Contains(out, "- 列表项一\n- 列表项二") {
		t.Fatal("列表结构被改动")
	}
	if !strings.Contains(out, "code line one\ncode line two") {
		t.Fatal("代码块被合并")
	}
}

// 出处行必须只插一次，且无标题时放开头。
func TestSourceCiteIdempotentShape(t *testing.T) {
	body := "# 标题\n\n正文。\n"
	once := withSourceCite(body, "https://ex.com/a")
	twice := withSourceCite(once, "https://ex.com/a")
	if strings.Count(once, "本文由") != 1 || strings.Count(twice, "本文由") != 1 {
		t.Fatalf("出处重复插入: once=%d twice=%d", strings.Count(once, "本文由"), strings.Count(twice, "本文由"))
	}
}

// 建档就带出处（不只写回）
func TestCreateDocIncludesCitation(t *testing.T) {
	db := testDB(t)
	db.Exec("INSERT INTO documents (title, source_url, raw_md, content_hash, units, status, library_id) VALUES ('标题','https://ex.com/a','# 标题\n\n正文。\n','hash-cite-0123456789',10,'terms_ready',1)")
	var id int64
	db.QueryRow("SELECT id FROM documents WHERE content_hash='hash-cite-0123456789'").Scan(&id)
	stub := &pipeStub{remote: RemoteDoc{ID: 7, Format: "markdown"}}
	stub.createdBody = ""
	withStub(t, stub)
	if _, err := createDoc(db, testConfig(), id); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stub.createdBody, "本文由") {
		t.Fatalf("建档正文缺出处: %q", stub.createdBody)
	}
}

// 缩进代码与列表续行不能被合并
func TestReflowKeepsIndentedBlocks(t *testing.T) {
	in := "普通行一\n普通行二\n\n    indented code 1\n    indented code 2\n\n- 列表项\n  续行保留\n"
	out := reflowParagraphs(in)
	if !strings.Contains(out, "普通行一 普通行二") {
		t.Fatalf("普通段落没合并: %q", out)
	}
	if !strings.Contains(out, "    indented code 1\n    indented code 2") {
		t.Fatalf("缩进代码被改动: %q", out)
	}
	if !strings.Contains(out, "- 列表项\n  续行保留") {
		t.Fatalf("列表续行被改动: %q", out)
	}
}

// 出处链接转义与协议校验
func TestSourceCiteEscaping(t *testing.T) {
	body := "# 标题\n正文。"
	got := withSourceCite(body, "https://ex.com/a_(b)?x=1")
	if strings.Contains(got, "]("+"https://ex.com/a_(b)") {
		t.Fatalf("括号未转义: %s", got)
	}
	if !strings.Contains(got, "%28") {
		t.Fatalf("未编码左括号: %s", got)
	}
	if withSourceCite(body, "javascript:alert(1)") != body {
		t.Fatal("非 http(s) 链接应被拒绝")
	}
}
