package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
)

const bingUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0 Safari/537.36"

var (
	reTags        = regexp.MustCompile(`<[^>]+>`)
	reExplainPref = regexp.MustCompile(`^释义[：:]\s*`)
)

type BingRow struct {
	Title   string `json:"title"`
	Snippet string `json:"snippet"`
	Link    string `json:"link"`
}

type BaikeResult struct {
	Title string `json:"title"`
	Text  string `json:"text"`
	URL   string `json:"url"`
}

func plainText(fragment string) string {
	return strings.Join(strings.Fields(html.UnescapeString(reTags.ReplaceAllString(fragment, ""))), " ")
}

const bingSearchURL = "https://cn.bing.com/search"
const explainSearchTimeout = 8 * time.Second

func parseBing(page []byte) []BingRow {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(page))
	if err != nil {
		return nil
	}
	var rows []BingRow
	doc.Find("li.b_algo").EachWithBreak(func(_ int, item *goquery.Selection) bool {
		heading := item.Find("h2 a").First()
		link, _ := heading.Attr("href")
		snippet := item.Find(".b_caption p").First()
		if snippet.Length() == 0 {
			snippet = item.Find("p").First()
		}
		rows = appendBingRow(rows, normalizedText(heading.Text()), normalizedText(snippet.Text()), link)
		return len(rows) < 12
	})
	return rows
}

func parseBingRSS(page []byte) []BingRow {
	var feed struct {
		XMLName xml.Name `xml:"rss"`
		Items   []struct {
			Title       string `xml:"title"`
			Description string `xml:"description"`
			Link        string `xml:"link"`
		} `xml:"channel>item"`
	}
	if err := xml.Unmarshal(page, &feed); err != nil {
		return nil
	}
	var rows []BingRow
	for _, item := range feed.Items {
		rows = appendBingRow(rows, plainText(item.Title), plainText(item.Description), item.Link)
		if len(rows) == 12 {
			break
		}
	}
	return rows
}

func normalizedText(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

func appendBingRow(rows []BingRow, title, snippet, rawLink string) []BingRow {
	link := bingResultLink(rawLink)
	if title == "" || snippet == "" || link == "" {
		return rows
	}
	for _, row := range rows {
		if row.Link == link {
			return rows
		}
	}
	return append(rows, BingRow{Title: title, Snippet: snippet, Link: link})
}

func bingResultLink(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if !u.IsAbs() {
		base, _ := url.Parse(bingSearchURL)
		u = base.ResolveReference(u)
	}
	host := strings.ToLower(u.Hostname())
	if (host == "bing.com" || strings.HasSuffix(host, ".bing.com")) && u.Path == "/ck/a" {
		target := u.Query().Get("u")
		if strings.HasPrefix(target, "a1") {
			decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(target[2:], "="))
			if err != nil {
				return ""
			}
			target = string(decoded)
		}
		u, err = url.Parse(target)
		if err != nil {
			return ""
		}
	}
	if (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || u.User != nil {
		return ""
	}
	u.Fragment, u.RawFragment = "", ""
	return u.String()
}

func httpClient(proxy string, timeout time.Duration) *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if proxy != "" {
		if u, err := url.Parse(proxy); err == nil {
			tr.Proxy = http.ProxyURL(u)
		}
	}
	return &http.Client{Timeout: timeout, Transport: tr}
}

func bingPage(ctx context.Context, client *http.Client, query string, rss bool) []byte {
	params := url.Values{"q": {query}, "setlang": {"zh-CN"}}
	if rss {
		params.Set("format", "rss")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, bingSearchURL+"?"+params.Encode(), nil)
	if err != nil {
		return nil
	}
	req.Header.Set("User-Agent", bingUA)
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil
	}
	return raw
}

func bingSearch(ctx context.Context, client *http.Client, query string, accept func(BingRow) bool) []BingRow {
	filter := func(rows []BingRow) []BingRow {
		matches := rows[:0]
		for _, row := range rows {
			if accept(row) {
				matches = append(matches, row)
			}
		}
		return matches
	}
	rows := filter(parseBing(bingPage(ctx, client, query, false)))
	if len(rows) == 0 && ctx.Err() == nil {
		// Bing 有时返回正常的结果页，却只搜索了术语的一部分。
		return filter(parseBingRSS(bingPage(ctx, client, query, true)))
	}
	return rows
}

func asciiWordByte(b byte) bool {
	return b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b == '_'
}

func fullTermMatcher(term string) *regexp.Regexp {
	pattern := regexp.QuoteMeta(term)
	if asciiWordByte(term[0]) {
		pattern = `(^|[^a-zA-Z0-9_])` + pattern
	}
	if asciiWordByte(term[len(term)-1]) {
		pattern += `($|[^a-zA-Z0-9_])`
	}
	return regexp.MustCompile("(?i)" + pattern)
}

func isBaikeArticle(link string) bool {
	u, err := url.Parse(link)
	return err == nil && strings.EqualFold(u.Hostname(), "baike.baidu.com") &&
		strings.HasPrefix(u.Path, "/item/") && u.Path != "/item/"
}

func findBaike(rows []BingRow, match *regexp.Regexp) *BaikeResult {
	var alias *BaikeResult
	for _, row := range rows {
		if !isBaikeArticle(row.Link) {
			continue
		}
		article := &BaikeResult{Title: row.Title, Text: row.Snippet, URL: row.Link}
		if match.MatchString(row.Title) {
			return article
		}
		if alias == nil && match.MatchString(row.Snippet) {
			alias = article
		}
	}
	return alias
}

// 百度百科的摘要来自必应索引。通用结果先复用一次搜索，再为百科单独补查。
func lookupSources(term string, client *http.Client) (*BaikeResult, []BingRow) {
	term = normalizedText(strings.ReplaceAll(term, "\"", " "))
	if term == "" {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), explainSearchTimeout)
	defer cancel()
	phrase := "\"" + term + "\""
	match := fullTermMatcher(term)
	relevant := func(row BingRow) bool {
		return match.MatchString(row.Title) || match.MatchString(row.Snippet)
	}
	rows := bingSearch(ctx, client, phrase, relevant)
	if len(rows) == 0 && ctx.Err() == nil {
		rows = bingSearch(ctx, client, term, relevant)
	}
	baike := findBaike(rows, match)
	if baike == nil && ctx.Err() == nil {
		articles := bingSearch(ctx, client, "site:baike.baidu.com "+phrase, func(row BingRow) bool {
			return isBaikeArticle(row.Link) && relevant(row)
		})
		baike = findBaike(articles, match)
	}
	return baike, rows[:min(3, len(rows))]
}

func squash(text string) string {
	t := strings.Join(strings.Fields(text), " ")
	return strings.Trim(t, "\\\"'“”‘’")
}

// composeExplanation 用百度百科 + Bing 摘要让模型写 40–120 字释义。
func composeExplanation(term, context string, baike *BaikeResult, bing []BingRow, llm *LLM) (string, error) {
	baikeText := "无"
	if baike != nil {
		baikeText = baike.Text
	}
	var lines []string
	for _, item := range bing {
		lines = append(lines, "- "+item.Title+"："+item.Snippet)
	}
	bingText := strings.Join(lines, "\n")
	if bingText == "" {
		bingText = "无"
	}
	runes := []rune(context)
	if len(runes) > 300 {
		runes = runes[:300]
	}
	messages := []map[string]string{
		{"role": "system", "content": "你是术语释义编辑。只输出一条释义，不要输出其他内容。"},
		{"role": "user", "content": "术语：" + term + "\n" +
			"原文上下文：" + string(runes) + "\n" +
			"百度百科摘要：" + baikeText + "\n" +
			"Bing 搜索结果：\n" + bingText + "\n\n" +
			"请写一条 40 到 120 字的中文释义：结合上下文选择义项，" +
			"必要时用“（此处指……）”限定；客观具体；" +
			"不要写“本文”“根据百度百科”等套话；不要换行、引号或角标。"},
	}
	text, err := llm.Chat(messages, "", 0.2)
	if err != nil {
		return "", err
	}
	text = reExplainPref.ReplaceAllString(squash(text), "")
	// shortcut: 模型偶尔超字数时硬截断，后续改成结构化长度约束。
	r := []rune(text)
	if len(r) > 200 {
		text = strings.TrimRight(string(r[:200]), "，。；、") + "。"
	}
	return text, nil
}

// explainOne 先查 glossary 缓存，再联网检索并用模型综合。
func explainOne(db *sql.DB, term, context string, libraryID sql.NullInt64, llm *LLM, useCache bool) (string, map[string]any, bool, error) {
	if useCache {
		var explanation, sourcesJSON string
		err := db.QueryRow("SELECT explanation, COALESCE(sources_json,'{}') FROM glossary WHERE term=? AND library_id=?", term, libraryID).
			Scan(&explanation, &sourcesJSON)
		if err == nil && explanation != "" {
			sources := map[string]any{}
			json.Unmarshal([]byte(sourcesJSON), &sources)
			sources["cached"] = true
			return explanation, sources, true, nil
		}
	}
	var baike *BaikeResult
	var bing []BingRow
	if cfg.Explain.Online {
		client := httpClient(cfg.Explain.Proxy, explainSearchTimeout)
		defer client.CloseIdleConnections()
		baike, bing = lookupSources(term, client)
	}
	explanation, err := composeExplanation(term, context, baike, bing, llm)
	if err != nil {
		return "", nil, false, err
	}
	sources := map[string]any{
		"wiki":         baike,
		"google":       bing,
		"llm_fallback": baike == nil && len(bing) == 0,
	}
	return explanation, sources, false, nil
}

func saveGlossary(db *sql.DB, term string, libraryID sql.NullInt64, explanation string, sources map[string]any) {
	raw, _ := json.Marshal(sources)
	db.Exec(`INSERT INTO glossary (term, library_id, explanation, sources_json, updated_at)
		VALUES (?, ?, ?, ?, datetime('now'))
		ON CONFLICT(term, library_id) DO UPDATE SET
		explanation=excluded.explanation, sources_json=excluded.sources_json, updated_at=datetime('now')`,
		term, libraryID, explanation, string(raw))
}
