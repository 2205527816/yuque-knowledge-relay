package main

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func samplePNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x % 255), uint8(y % 255), 120, 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestEncodeResizedCapsWidth(t *testing.T) {
	out, ok := encodeResized(samplePNG(t, 2000, 400), 800, 80)
	if !ok {
		t.Fatal("编码失败")
	}
	img, err := jpeg.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("输出不是 JPEG: %v", err)
	}
	if img.Bounds().Dx() != 800 || img.Bounds().Dy() != 160 {
		t.Fatalf("尺寸不对: %v", img.Bounds())
	}
}

func TestLocalizeImagesRewritesToCDN(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "tok")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Write([]byte(`{"commit":{"sha":"abc123"}}`))
	}))
	defer srv.Close()
	oldBase, oldDownload := githubAPIBase, downloadImageFn
	githubAPIBase = srv.URL
	downloadImageFn = func(rawURL, referer string, maxBytes int64) ([]byte, bool) {
		return samplePNG(t, 100, 50), true
	}
	defer func() { githubAPIBase, downloadImageFn = oldBase, oldDownload }()

	c := backupCfg(t.TempDir())
	c.Backup["images"] = map[string]any{
		"enabled": true, "repo": "me/assets", "branch": "main",
		"url_template": "https://cdn.jsdelivr.net/gh/{repo}@{branch}/{path}",
		"quality":      80, "max_width": 800, "max_images": 5, "max_bytes": 1 << 20,
	}
	doc := Document{SourceURL: "https://ex.com/a"}
	out, notes := localizeImages(c, doc, "正文\n\n![图](https://ex.com/a.png)\n")
	if len(notes) != 0 {
		t.Fatalf("不该有告警: %v", notes)
	}
	if !strings.Contains(out, "https://cdn.jsdelivr.net/gh/me/assets@main/img/") {
		t.Fatalf("链接没替换: %s", out)
	}
	if !strings.Contains(out, ".jpg") {
		t.Fatalf("应为 jpg: %s", out)
	}
}

func TestLocalizeImagesKeepsOriginalOnFailure(t *testing.T) {
	oldDownload := downloadImageFn
	downloadImageFn = func(rawURL, referer string, maxBytes int64) ([]byte, bool) { return nil, false }
	defer func() { downloadImageFn = oldDownload }()
	c := backupCfg(t.TempDir())
	c.Backup["images"] = map[string]any{"enabled": true, "repo": "me/assets", "url_template": "x/{path}"}
	out, _ := localizeImages(c, Document{}, "![图](https://ex.com/a.png)\n")
	if !strings.Contains(out, "https://ex.com/a.png") {
		t.Fatalf("失败时应保留原链: %s", out)
	}
}
