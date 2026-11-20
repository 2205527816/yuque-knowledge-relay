package main

import (
	"html/template"
	"net/url"
	"regexp"
	"strings"

	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
)

var (
	mdConv = goldmark.New(goldmark.WithExtensions(extension.Table))
	policy = buildPolicy()
	reImg  = regexp.MustCompile(`(?i)(<img[^>]*\bsrc=")([^"]+)(")`)
)

func buildPolicy() *bluemonday.Policy {
	p := bluemonday.NewPolicy()
	p.AllowElements("p", "br", "hr", "h1", "h2", "h3", "h4", "h5", "h6",
		"ul", "ol", "li", "blockquote", "pre", "code",
		"strong", "em", "del", "table", "thead", "tbody", "tr", "th", "td")
	p.AllowElements("a", "img")
	p.AllowAttrs("href", "title").OnElements("a")
	p.AllowAttrs("src", "alt", "title").OnElements("img")
	return p
}

// markdownToHTML 把正文转成安全 HTML，并把外链图片换成 /img 代理。
func markdownToHTML(md, referer string) template.HTML {
	var buf strings.Builder
	if err := mdConv.Convert([]byte(md), &buf); err != nil {
		return template.HTML(template.HTMLEscapeString(md))
	}
	html := buf.String()
	html = reImg.ReplaceAllStringFunc(html, func(m string) string {
		parts := reImg.FindStringSubmatch(m)
		src := parts[2]
		if !strings.HasPrefix(src, "http://") && !strings.HasPrefix(src, "https://") {
			return m
		}
		q := url.Values{"u": {src}}
		if referer != "" {
			q.Set("r", referer)
		}
		return parts[1] + "/img?" + q.Encode() + parts[3]
	})
	return template.HTML(policy.Sanitize(html))
}
