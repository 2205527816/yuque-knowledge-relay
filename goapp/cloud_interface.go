package main

import (
	"bytes"
	"fmt"
	"html/template"
	"image"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"

	"golang.org/x/image/draw"
)

const interfaceAssets = "/static/kr-bauhaus-20261118/"

var approvedSealIDs = map[string]int{
	"文史": 0, "策论": 1, "四方": 2, "时评": 3, "浪潮": 4,
	"娱乐": 5, "冗谈": 6, "奇偶": 7, "数理": 8, "编程": 9,
	"技法": 10, "人物": 11, "研学": 12, "功利": 13, "经营": 14,
}

var libraryDescriptions = map[string]string{
	"文史": "文学、历史与思想研究", "策论": "制度、政策与治理分析", "四方": "旅行、地理与城市观察",
	"时评": "公共事件与社会评论", "浪潮": "技术趋势与产业变迁", "娱乐": "游戏、影视与娱乐作品",
	"冗谈": "生活见闻与个人杂感", "奇偶": "存在、情感与价值思考", "数理": "数学、科学与可检验机制",
	"编程": "软件构建、调试与维护", "技法": "可操作的方法与实践", "人物": "人物经历、性格与选择",
	"研学": "学习、研究与求职成长", "功利": "利益、权力与关系博弈", "经营": "生意、赚钱与经营策略",
}

func approvedSeal(name string) template.HTML {
	id, ok := approvedSealIDs[name]
	if !ok {
		return libraryIcon(name)
	}
	return template.HTML(fmt.Sprintf(`<span class="seal" aria-hidden="true"><svg viewBox="0 0 100 100"><use href="%sseals.svg#seal-%d"></use></svg></span>`, interfaceAssets, id))
}

func formatCount(n int) string {
	digits := strconv.Itoa(n)
	for index := len(digits) - 3; index > 0; index -= 3 {
		digits = digits[:index] + "," + digits[index:]
	}
	return digits
}

type statusOption struct{ Key, Label string }

var queueStatuses = []statusOption{
	{"pending_class", "待归类"}, {"terms_ready", "待审核"}, {"written", "已写回"},
	{"conflict", "远端有改动"}, {"failed", "失败"}, {"imported", "已导入"},
	{"classified", "已分类"}, {"created", "已入语雀"},
}

const remoteConflictBody = "远端正文已被修改，确认后可强制覆盖"
const remoteConflictLake = "远端文档已转为 lake 格式，确认后可强制覆盖"

func rowStatus(d Document) string {
	if d.Error.String == remoteConflictBody || d.Error.String == remoteConflictLake {
		return "conflict"
	}
	return d.Status
}

func queueStatus(value string) string {
	for _, status := range queueStatuses {
		if status.Key == value {
			return value
		}
	}
	return ""
}

func keepQueueStatus(target, status string) string {
	if status == "" {
		return target
	}
	u, _ := url.Parse(target) // target is constructed by listURL.
	params := u.Query()
	params.Set("status", status)
	u.RawQuery = params.Encode()
	return u.String()
}

func queueCover(d Document) string {
	var src string
	if md := reMDImage.FindStringSubmatch(d.RawMD); len(md) == 3 {
		src = md[2]
	} else if html := reHTMLImg.FindStringSubmatch(d.RawMD); len(html) == 2 {
		src = html[1]
	}
	parsed, err := url.Parse(src)
	if err != nil || parsed.Hostname() == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return ""
	}
	q := url.Values{"u": {src}, "view": {"queue"}}
	if d.SourceURL != "" {
		q.Set("r", d.SourceURL)
	}
	return "/img?" + q.Encode()
}

func writeQueueImage(w http.ResponseWriter, cacheFile string, raw []byte) {
	if thumb, err := os.ReadFile(cacheFile); err == nil {
		serveQueueImage(w, thumb)
		return
	}
	dimensions, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil || dimensions.Width <= 0 || dimensions.Height <= 0 || int64(dimensions.Width)*int64(dimensions.Height) > 24_000_000 {
		http.Error(w, "无可用缩略图", http.StatusNotFound)
		return
	}
	photo, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		http.Error(w, "无可用缩略图", http.StatusNotFound)
		return
	}
	bounds := photo.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if width > 960 {
		height, width = max(1, height*960/width), 960
	}
	if height > 640 {
		width, height = max(1, width*640/height), 640
	}
	gray := image.NewGray(image.Rect(0, 0, width, height))
	draw.CatmullRom.Scale(gray, gray.Bounds(), photo, bounds, draw.Src, nil)
	var compressed bytes.Buffer
	if err := jpeg.Encode(&compressed, gray, &jpeg.Options{Quality: 58}); err != nil {
		http.Error(w, "无可用缩略图", http.StatusNotFound)
		return
	}
	if temp, err := os.CreateTemp(filepath.Dir(cacheFile), ".queue-image-*"); err == nil {
		defer os.Remove(temp.Name())
		_, writeErr := temp.Write(compressed.Bytes())
		closeErr := temp.Close()
		if writeErr == nil && closeErr == nil {
			_ = os.Rename(temp.Name(), cacheFile)
		}
	}
	serveQueueImage(w, compressed.Bytes())
}

func serveQueueImage(w http.ResponseWriter, jpg []byte) {
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "private, max-age=86400")
	w.Write(jpg)
}
