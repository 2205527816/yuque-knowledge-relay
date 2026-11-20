package main

import (
	"encoding/base64"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestParseBingResultMarkup(t *testing.T) {
	article := "https://baike.baidu.com/item/%E8%B7%AF%E5%BE%84%E4%BE%9D%E8%B5%96/247793"
	encoded := base64.RawURLEncoding.EncodeToString([]byte(article))
	page := `<ol id="b_results"><li class="b_algo"><h2><span class="result-icon"></span>
		<a href="/ck/a?p=123&amp;u=a1` + encoded + `"><strong>路径依赖</strong>_百度百科</a></h2>
		<div class="b_caption"><p>历史选择会影响后续的路径依赖。</p></div></li>
		<li class="b_algo"><h2><a href="` + article + `">重复结果</a></h2><p>重复摘要</p></li></ol>`
	rows := parseBing([]byte(page))
	want := BingRow{Title: "路径依赖_百度百科", Snippet: "历史选择会影响后续的路径依赖。", Link: article}
	if len(rows) != 1 || rows[0] != want {
		t.Fatalf("Bing rows = %+v, want one row %+v", rows, want)
	}
	if baike := findBaike(rows, fullTermMatcher("路径依赖")); baike == nil || baike.URL != article {
		t.Fatalf("Baike article = %+v", baike)
	}
}

type searchTestTransport func(*http.Request) (*http.Response, error)

func (f searchTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestLookupSourcesRejectsPartialMatchesAndUsesRSS(t *testing.T) {
	const baikeURL = "https://baike.baidu.com/item/path-dependence"
	const bingURL = "https://example.com/path-dependence"
	const pathOnly = `<li class="b_algo"><h2><a href="https://example.com/path">路径</a></h2><p>目录的路径</p></li>`
	const rssOffTopic = `<rss version="2.0"><channel><item><title>路径</title><description>目录的路径</description><link>https://example.com/path</link></item></channel></rss>`
	const rssBing = `<rss version="2.0"><channel><item><title>路径依赖理论</title><description><![CDATA[制度的<b>路径依赖</b>与延续。]]></description><link>` + bingURL + `</link></item></channel></rss>`
	const rssBaike = `<rss version="2.0"><channel><item><title>路径依赖</title><description>既有选择限制后续方向。</description><link>` + baikeURL + `</link></item></channel></rss>`
	const pretendBaike = `<li class="b_algo"><h2><a href="https://baike.baidu.com.example.com/item/path">路径依赖</a></h2><p>非百度百科结果</p></li>
	<li class="b_algo"><h2><a href="https://baike.baidu.com/user/path">路径依赖</a></h2><p>非百科词条</p></li>`
	client := &http.Client{Transport: searchTestTransport(func(r *http.Request) (*http.Response, error) {
		query := r.URL.Query().Get("q")
		rss := r.URL.Query().Get("format") == "rss"
		var body string
		switch query {
		case `"路径依赖"`:
			body = pathOnly
			if rss {
				body = rssOffTopic
			}
		case "路径依赖":
			body = pathOnly
			if rss {
				body = rssBing
			}
		case `site:baike.baidu.com "路径依赖"`:
			body = pretendBaike
			if rss {
				body = rssBaike
			}
		default:
			t.Fatalf("unexpected Bing query %q", query)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	baike, rows := lookupSources(" 路径依赖 ", client)
	if len(rows) != 1 || rows[0].Link != bingURL || rows[0].Snippet != "制度的路径依赖与延续。" {
		t.Fatalf("Bing rows = %+v", rows)
	}
	if baike == nil || baike.URL != baikeURL || baike.Text != "既有选择限制后续方向。" {
		t.Fatalf("Baike article = %+v", baike)
	}
}

func TestFullTermMatcherKeepsLatinWordBoundaries(t *testing.T) {
	match := fullTermMatcher("SIMD")
	if !match.MatchString("单指令流多数据流（simd）") || match.MatchString("SIMDesign 和 PSIMD") {
		t.Fatal("SIMD should match as a complete term, case insensitively")
	}
}
