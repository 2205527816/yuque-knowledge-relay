package main

import (
	"math"
	"regexp"
	"strings"
)

var (
	reInlineCode = regexp.MustCompile("`[^`\\n]*`")
	reImage      = regexp.MustCompile(`!\[[^\]]*\]\([^)]*\)`)
	reLink       = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
	reTag        = regexp.MustCompile(`<[^>]+>`)
	reURL        = regexp.MustCompile(`https?://\S+`)
	reCJK        = regexp.MustCompile(`[\x{4e00}-\x{9fff}]`)
	reWord       = regexp.MustCompile(`[A-Za-z]+`)
)

// stripCode 去掉围栏代码块和行内代码，保留其余文本。
func stripCode(md string) string {
	var out []string
	inFence := false
	fence := ""
	for _, line := range strings.Split(md, "\n") {
		s := strings.TrimLeft(line, " \t")
		if !inFence && (strings.HasPrefix(s, "```") || strings.HasPrefix(s, "~~~")) {
			inFence, fence = true, s[:3]
			out = append(out, "")
			continue
		}
		if inFence {
			if strings.HasPrefix(s, fence) {
				inFence = false
			}
			out = append(out, "")
			continue
		}
		out = append(out, line)
	}
	return reInlineCode.ReplaceAllString(strings.Join(out, "\n"), " ")
}

// countUnits 阅读单位 U = 中文字符数 + 英文单词数，排除代码、URL、图片。
func countUnits(md string) int {
	text := stripCode(md)
	text = reImage.ReplaceAllString(text, " ")
	text = reLink.ReplaceAllString(text, "$1")
	text = reTag.ReplaceAllString(text, " ")
	text = reURL.ReplaceAllString(text, " ")
	return len(reCJK.FindAllString(text, -1)) + len(reWord.FindAllString(text, -1))
}

// recommendedCount 推荐注释词数量：密度 × 倍率，夹在 [floor, cap]。
func recommendedCount(units int, multiplier, density float64, floor, capN int) int {
	raw := int(math.Ceil(float64(units) / 1000 * density * multiplier))
	if raw < floor {
		return floor
	}
	if raw > capN {
		return capN
	}
	return raw
}
