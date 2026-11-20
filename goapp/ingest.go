package main

import (
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown"
	"gopkg.in/yaml.v3"
)

var (
	reHTMLStart = regexp.MustCompile(`(?i)^\s*(<!doctype|<html)`)
	reTitleTag  = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	reFirstH1   = regexp.MustCompile(`(?m)^\s*#\s+(.+?)\s*$`)
	reFirstURL  = regexp.MustCompile(`https?://[^\s)"'<]+`)
	reHeadBlock = regexp.MustCompile(`(?is)<head[^>]*>.*?</head>`)
)

var dropParams = map[string]bool{"spm": true, "from": true, "share_token": true}

var htmlConverter = htmltomarkdown.NewConverter("", true, nil)

// ParsedDoc 是 parse_simpread 的统一输出。
type ParsedDoc struct {
	Title       string
	SourceURL   string
	Author      string
	PublishedAt string
	BodyMD      string
	ContentHash string
}

// normalizeSpace 对应 Python 的 re.sub(r"\s+", " ", s).strip()，
// 包含 Unicode 空白，所以不用 strings.Fields。
func normalizeSpace(s string) string {
	var b strings.Builder
	prevSpace := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			if !prevSpace && b.Len() > 0 {
				b.WriteByte(' ')
			}
			prevSpace = true
			continue
		}
		prevSpace = false
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}

// contentHash 正文归一化后取 sha256，用于去重和幂等 slug。
func contentHash(body string) string {
	sum := sha256.Sum256([]byte(normalizeSpace(body)))
	return hex.EncodeToString(sum[:])
}

func queryEscapePython(s string) string { return url.QueryEscape(s) }

// normalizeURL 去掉 utm_* 等跟踪参数、fragment 和末尾斜杠。
func normalizeURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	var pairs []string
	if u.RawQuery != "" {
		for _, part := range strings.Split(u.RawQuery, "&") {
			if part == "" {
				continue
			}
			k, v, _ := strings.Cut(part, "=")
			key, err1 := url.QueryUnescape(k)
			val, err2 := url.QueryUnescape(v)
			if err1 != nil || err2 != nil {
				continue
			}
			lower := strings.ToLower(key)
			if strings.HasPrefix(lower, "utm_") || dropParams[lower] {
				continue
			}
			pairs = append(pairs, queryEscapePython(key)+"="+queryEscapePython(val))
		}
	}
	path := strings.TrimRight(u.EscapedPath(), "/")
	if path == "" {
		path = "/"
	}
	out := u.Scheme + "://" + u.Host + path
	if len(pairs) > 0 {
		out += "?" + strings.Join(pairs, "&")
	}
	return out
}

func splitFrontMatter(text string) (map[string]any, string) {
	lines := strings.Split(text, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return map[string]any{}, text
	}
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			var data map[string]any
			if err := yaml.Unmarshal([]byte(strings.Join(lines[1:i], "\n")), &data); err != nil {
				data = map[string]any{}
			}
			body := strings.Join(lines[i+1:], "\n")
			body = strings.TrimLeft(body, "\n")
			return data, body
		}
	}
	return map[string]any{}, text
}

func firstString(data map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := data[k]; ok && v != nil {
			if s := strings.TrimSpace(toString(v)); s != "" {
				return s
			}
		}
	}
	return ""
}

func toString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case int:
		return itoa(t)
	case int64:
		return itoa(int(t))
	case float64:
		return trimFloat(t)
	case time.Time:
		// YAML 的日期在 Go 里是 time.Time，Python 侧 str(date) 就是 2006-01-02
		return t.Format("2006-01-02")
	default:
		return ""
	}
}

func trimFloat(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}

func stripLeadingH1(body, title string) string {
	if title == "" {
		return body
	}
	re := regexp.MustCompile(`^\s*#\s+` + regexp.QuoteMeta(title) + `\s*\n+`)
	return re.ReplaceAllString(body, "")
}

// parseSimpread 解析 SimpRead 导出的 Markdown 或 HTML，返回统一结构。
func parseSimpread(text, filename string) ParsedDoc {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	front, body := splitFrontMatter(text)

	htmlTitle := ""
	if reHTMLStart.MatchString(body) {
		if m := reTitleTag.FindStringSubmatch(body); m != nil {
			htmlTitle = strings.TrimSpace(normalizeSpace(m[1]))
		}
		body = reHeadBlock.ReplaceAllString(body, " ")
		body = reTitleTag.ReplaceAllString(body, " ")
		if md, err := htmlConverter.ConvertString(body); err == nil {
			body = reflowParagraphs(md)
		}
	}

	title := firstString(front, "title", "Title")
	if title == "" {
		title = htmlTitle
	}
	if title == "" {
		if m := reFirstH1.FindStringSubmatch(body); m != nil {
			title = strings.TrimSpace(m[1])
		}
	}
	if title == "" && filename != "" {
		title = strings.TrimSuffix(filepath.Base(filename), filepath.Ext(filename))
	}
	if title == "" {
		title = "未命名"
	}

	srcURL := normalizeURL(firstString(front, "url", "source", "original", "link"))
	if srcURL == "" {
		if m := reFirstURL.FindString(body); m != "" {
			srcURL = normalizeURL(m)
		}
	}
	author := firstString(front, "author", "byline", "creator")
	published := firstString(front, "date", "published", "published_at", "created_at")

	body = strings.TrimSpace(stripLeadingH1(body, title))
	return ParsedDoc{
		Title:       title,
		SourceURL:   srcURL,
		Author:      author,
		PublishedAt: published,
		BodyMD:      body,
		ContentHash: contentHash(body),
	}
}

func parseFile(path string) (ParsedDoc, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ParsedDoc{}, err
	}
	text := strings.TrimPrefix(string(raw), "\ufeff")
	return parseSimpread(text, filepath.Base(path)), nil
}

var reSetext = regexp.MustCompile(`^(=+|-{2,})\s*$`)

var reBlockStart = regexp.MustCompile(`^(#{1,6}\s|[-+*]\s|\d+[.)]\s|>|<|\||` + "```" + `|~~~)`)

// reflowParagraphs 把 HTML 转出来的硬换行合回段落：网页里逐行排版的文本被转成
// Markdown 后，单换行会被语雀当成硬回车，所以把连续普通行用空格拼回一行；
// 标题/列表/引用/表格/代码块保持原样。
func reflowParagraphs(md string) string {
	lines := strings.Split(md, "\n")
	var out []string
	inFence := false
	var para []string
	flush := func() {
		if len(para) > 0 {
			out = append(out, strings.Join(para, " "))
			para = para[:0]
		}
	}
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			flush()
			out = append(out, line)
			inFence = !inFence
			continue
		}
		if inFence {
			out = append(out, line)
			continue
		}
		if indent := len(line) - len(strings.TrimLeft(line, " \t")); indent > 0 || reSetext.MatchString(trimmed) {
			flush() // 缩进块（代码/列表续行）与 setext 分隔不参与合并
			out = append(out, line)
			continue
		}
		if trimmed == "" || reBlockStart.MatchString(trimmed) {
			flush()
			out = append(out, line)
			continue
		}
		para = append(para, strings.TrimRight(trimmed, " "))
	}
	flush()
	return strings.Join(out, "\n")
}
