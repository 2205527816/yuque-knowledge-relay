package main

import (
	"strings"
	"testing"
)

func TestWithSourceCite(t *testing.T) {
	body := `# 标题

正文。
`
	got := withSourceCite(body, "https://www.example.com/a?x=1")
	if !strings.Contains(got, "本文由") || !strings.Contains(got, "[www.example.com]") {
		t.Fatalf("出处没插对: %s", got)
	}
	if !strings.HasPrefix(got, "# 标题") || !strings.Contains(got, "标题\n\n> 本文由") {
		t.Fatalf("应紧跟在标题后: %q", got)
	}
	if withSourceCite(body, "") != body {
		t.Fatal("无来源时不应改动")
	}
	noH1 := withSourceCite("正文。", "https://a.com/")
	if !strings.HasPrefix(noH1, "> 本文由") {
		t.Fatalf("无标题应放开头: %s", noH1)
	}
}

func TestReflowParagraphs(t *testing.T) {
	in := "第一行。\n第二行接上。\n\n## 小标题\n- 列表项\n\n结尾。\n"
	out := reflowParagraphs(in)
	if !strings.Contains(out, "第一行。 第二行接上。") {
		t.Fatalf("段落没合并: %q", out)
	}
	if !strings.Contains(out, "## 小标题") || !strings.Contains(out, "- 列表项") {
		t.Fatalf("结构被改动: %q", out)
	}
}

func TestHTMLIngestReflows(t *testing.T) {
	html := `<html><head><title>测试</title></head><body><p>第一句。
第二句。
第三句。</p></body></html>`
	doc := parseSimpread(html, "a.html")
	if strings.Contains(doc.BodyMD, "第二句。\n第三句") {
		t.Fatalf("软换行没被合并: %q", doc.BodyMD)
	}
}
