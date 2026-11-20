package main

import (
	"bytes"
	"crypto/sha1"
	"database/sql"
	"encoding/hex"
	"image"
	"image/jpeg"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

type imageSettings struct {
	Enabled     bool
	Repo        string
	Branch      string
	URLTemplate string
	Quality     int
	MaxWidth    int
	MaxImages   int
	MaxBytes    int64
}

func imageConfig(c Config) imageSettings {
	s := imageSettings{Branch: "main", Quality: 82, MaxWidth: 1600, MaxImages: 30, MaxBytes: 15 << 20}
	raw, ok := c.Backup["images"].(map[string]any)
	if !ok {
		return s
	}
	if v, ok := raw["enabled"].(bool); ok {
		s.Enabled = v
	}
	if v, ok := raw["repo"].(string); ok {
		s.Repo = v
	}
	if v, ok := raw["branch"].(string); ok && v != "" {
		s.Branch = v
	}
	if v, ok := raw["url_template"].(string); ok {
		s.URLTemplate = v
	}
	if v, ok := raw["quality"].(int); ok {
		s.Quality = v
	}
	if v, ok := raw["max_width"].(int); ok {
		s.MaxWidth = v
	}
	if v, ok := raw["max_images"].(int); ok {
		s.MaxImages = v
	}
	if v, ok := raw["max_bytes"].(int); ok {
		s.MaxBytes = int64(v)
	}
	return s
}

var (
	reMDImage = regexp.MustCompile(`!\[([^\]]*)\]\((https?://[^)\s]+)\)`)
	reHTMLImg = regexp.MustCompile(`(?i)<img[^>]*\bsrc="(https?://[^"]+)"`)
)

// encodeResized 解码 → 限宽缩放 → JPEG(q=quality)。
// shortcut: Go 生态只有 lossless WebP 编码器，体积不降反增，这里退化为 JPEG；
// 等接入 libwebp（cgo）再换回 .webp。
func encodeResized(data []byte, maxWidth, quality int) ([]byte, bool) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, false
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if maxWidth > 0 && w > maxWidth {
		nw := maxWidth
		nh := h * maxWidth / w
		if nh < 1 {
			nh = 1
		}
		dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
		draw.CatmullRom.Scale(dst, dst.Bounds(), img, b, draw.Src, nil)
		img = dst
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
		return nil, false
	}
	return buf.Bytes(), true
}

func sha1Hex(data []byte) string {
	sum := sha1.Sum(data)
	return hex.EncodeToString(sum[:])
}

// downloadImageFn 可在测试里替换。
var downloadImageFn = downloadImage

func downloadImage(rawURL, referer string, maxBytes int64) ([]byte, bool) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Hostname() == "" {
		return nil, false
	}
	client, err := safeClient(u.Hostname())
	if err != nil {
		return nil, false
	}
	req, err := http.NewRequest("GET", rawURL, nil)
	if err != nil {
		return nil, false
	}
	req.Header.Set("User-Agent", browserUA)
	if referer != "" {
		req.Header.Set("Referer", referer)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, false
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil || int64(len(data)) > maxBytes {
		return nil, false
	}
	return data, true
}

// localizeImages 把正文外链图片转成 GitHub assets + CDN 地址；失败保留原链。
func localizeImages(c Config, doc Document, rendered string) (string, []string) {
	s := imageConfig(c)
	if !s.Enabled || s.Repo == "" {
		return rendered, nil
	}
	token := backupConfig(c).Token
	if token == "" {
		return rendered, []string{"图片本地化跳过：未配置 GitHub token"}
	}
	seen := map[string]string{}
	var notes []string
	count := 0
	replace := func(rawURL string) (string, bool) {
		if v, ok := seen[rawURL]; ok {
			return v, v != ""
		}
		if count >= s.MaxImages {
			return "", false
		}
		count++
		data, ok := downloadImageFn(rawURL, doc.SourceURL, s.MaxBytes)
		if !ok {
			seen[rawURL] = ""
			return "", false
		}
		webp, ok := encodeResized(data, s.MaxWidth, s.Quality)
		if !ok {
			seen[rawURL] = ""
			return "", false
		}
		path := "img/" + sha1Hex(webp)[:16] + ".jpg"
		if _, err := githubPut(token, s.Repo, s.Branch, path, string(webp), "assets: "+path); err != nil {
			notes = append(notes, "图片上传失败："+truncate(err.Error(), 80))
			seen[rawURL] = ""
			return "", false
		}
		cdn := strings.NewReplacer("{repo}", s.Repo, "{branch}", s.Branch, "{path}", path).Replace(s.URLTemplate)
		seen[rawURL] = cdn
		return cdn, true
	}
	out := reMDImage.ReplaceAllStringFunc(rendered, func(m string) string {
		parts := reMDImage.FindStringSubmatch(m)
		if cdn, ok := replace(parts[2]); ok {
			return "![" + parts[1] + "](" + cdn + ")"
		}
		return m
	})
	out = reHTMLImg.ReplaceAllStringFunc(out, func(m string) string {
		parts := reHTMLImg.FindStringSubmatch(m)
		if cdn, ok := replace(parts[1]); ok {
			return strings.Replace(m, parts[1], cdn, 1)
		}
		return m
	})
	return out, notes
}

var _ = time.Now
var _ = sql.ErrNoRows
