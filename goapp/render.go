package main

import (
	"net/url"
	"regexp"
	"sort"
	"strings"
)

// 注释硬规则：***词*** 标记 + 段后引用块；跳过标题/代码/表格/引用/HTML/链接文字/已有强调。

var (
	reProtected = regexp.MustCompile("(`[^`\\n]*`" +
		`|!\[[^\]]*\]\([^)]*\)` +
		`|\[[^\]]*\]\([^)]*\)` +
		`|<https?://[^>]+>` +
		`|</?[A-Za-z][^>]*>` +
		`|\*\*\*[^*\n]+\*\*\*` +
		`|\*\*[^*\n]+\*\*` +
		`|~~[^~\n]+~~)`)
	reWordChar = regexp.MustCompile(`[A-Za-z0-9_]`)
	reList     = regexp.MustCompile(`^\s*([-+*]|\d+[.)])\s+`)
	reHeading  = regexp.MustCompile(`^\s{0,3}#{1,6}\s`)
)

type mdBlock struct {
	kind  string
	lines []string
}

// isHR 对应 Python 的 ^\s{0,3}([-*_])(?:\s*\1){2,}\s*$；
// RE2 不支持反向引用，改成手写扫描。
func isHR(line string) bool {
	s := strings.TrimSpace(line)
	if s == "" {
		return false
	}
	var ch byte
	count := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case ' ', '\t':
			continue
		case '-', '*', '_':
			if ch == 0 {
				ch = c
			} else if ch != c {
				return false
			}
			count++
		default:
			return false
		}
	}
	return count >= 3
}

func startsBlock(line string) bool {
	s := strings.TrimLeft(line, " \t")
	return strings.HasPrefix(s, "```") || strings.HasPrefix(s, "~~~") ||
		strings.HasPrefix(s, ">") || strings.HasPrefix(s, "|") || strings.HasPrefix(s, "<") ||
		reHeading.MatchString(line) || isHR(line) || reList.MatchString(line)
}

func countIndent(line string) int { return len(line) - len(strings.TrimLeft(line, " \t")) }

func takeList(lines []string, i int) ([]string, int) {
	n := len(lines)
	first := lines[i]
	indent := countIndent(first)
	block := []string{first}
	i++
	for i < n {
		line := lines[i]
		if strings.TrimSpace(line) == "" {
			j := i + 1
			if j < n && reList.MatchString(lines[j]) && countIndent(lines[j]) >= indent {
				block = append(block, line)
				i++
				continue
			}
			break
		}
		cur := countIndent(line)
		if reList.MatchString(line) && cur >= indent {
			block = append(block, line)
			i++
			continue
		}
		if cur > indent && !startsBlock(line) {
			block = append(block, line)
			i++
			continue
		}
		break
	}
	return block, i
}

// splitBlocks 把 Markdown 切成块；只有 para / list 可注。
func splitBlocks(lines []string) []mdBlock {
	var out []mdBlock
	i, n := 0, len(lines)
	for i < n {
		line := lines[i]
		if strings.TrimSpace(line) == "" {
			out = append(out, mdBlock{"blank", []string{line}})
			i++
			continue
		}
		s := strings.TrimLeft(line, " \t")
		switch {
		case strings.HasPrefix(s, "```") || strings.HasPrefix(s, "~~~"):
			fence := s[:3]
			block := []string{line}
			i++
			for i < n {
				block = append(block, lines[i])
				if strings.HasPrefix(strings.TrimLeft(lines[i], " \t"), fence) {
					i++
					break
				}
				i++
			}
			out = append(out, mdBlock{"code", block})
		case reHeading.MatchString(line):
			out = append(out, mdBlock{"heading", []string{line}})
			i++
		case isHR(line):
			out = append(out, mdBlock{"hr", []string{line}})
			i++
		case strings.HasPrefix(s, ">"):
			block := []string{line}
			i++
			for i < n && strings.HasPrefix(strings.TrimLeft(lines[i], " \t"), ">") {
				block = append(block, lines[i])
				i++
			}
			out = append(out, mdBlock{"quote", block})
		case strings.HasPrefix(s, "|"):
			block := []string{line}
			i++
			for i < n && strings.HasPrefix(strings.TrimLeft(lines[i], " \t"), "|") {
				block = append(block, lines[i])
				i++
			}
			out = append(out, mdBlock{"table", block})
		case strings.HasPrefix(s, "<"):
			block := []string{line}
			i++
			for i < n && strings.TrimSpace(lines[i]) != "" {
				block = append(block, lines[i])
				i++
			}
			out = append(out, mdBlock{"html", block})
		case reList.MatchString(line):
			block, next := takeList(lines, i)
			i = next
			out = append(out, mdBlock{"list", block})
		default:
			block := []string{line}
			i++
			for i < n && strings.TrimSpace(lines[i]) != "" && !startsBlock(lines[i]) {
				block = append(block, lines[i])
				i++
			}
			out = append(out, mdBlock{"para", block})
		}
	}
	return out
}

// protectedSpans 返回不可注释区间（代码/链接/HTML/已有强调/单星号斜体）。
// Go 的 RE2 不支持 lookaround，单星号斜体在正则外侧手工扫。
func protectedSpans(line string) [][2]int {
	var spans [][2]int
	last := 0
	for _, m := range reProtected.FindAllStringIndex(line, -1) {
		spans = append(spans, [2]int{m[0], m[1]})
		last = m[1]
		_ = last
	}
	// 在未被保护的间隙里找 *斜体*
	prev := 0
	var gaps [][2]int
	for _, sp := range spans {
		if sp[0] > prev {
			gaps = append(gaps, [2]int{prev, sp[0]})
		}
		prev = sp[1]
	}
	if prev < len(line) {
		gaps = append(gaps, [2]int{prev, len(line)})
	}
	for _, g := range gaps {
		i := g[0]
		for i < g[1] {
			if line[i] != '*' {
				i++
				continue
			}
			if (i > 0 && line[i-1] == '*') || (i+1 < len(line) && line[i+1] == '*') {
				i++
				continue
			}
			j := i + 1
			for j < g[1] {
				if line[j] == '*' && (j+1 >= len(line) || line[j+1] != '*') && line[j-1] != '*' {
					break
				}
				j++
			}
			if j < g[1] && j > i+1 {
				spans = append(spans, [2]int{i, j + 1})
				i = j + 1
				continue
			}
			i++
		}
	}
	sort.Slice(spans, func(a, b int) bool { return spans[a][0] < spans[b][0] })
	return spans
}

func isWordChar(b byte) bool {
	return b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || b == '_'
}

func boundaryOk(text string, start, end int) bool {
	if start > 0 && isWordChar(text[start-1]) && isWordChar(text[start]) {
		return false
	}
	if end < len(text) && isWordChar(text[end-1]) && isWordChar(text[end]) {
		return false
	}
	return true
}

type hit struct {
	start, end int
	term       string
}

// markChunk 给一块文本加标记，返回替换后文本与命中列表。
func markChunk(chunk string, terms []string) (string, []hit) {
	sorted := append([]string(nil), terms...)
	sort.SliceStable(sorted, func(a, b int) bool { return len(sorted[a]) > len(sorted[b]) })
	var hits []hit
	for _, term := range sorted {
		if term == "" {
			continue
		}
		from := 0
		for {
			idx := strings.Index(chunk[from:], term)
			if idx < 0 {
				break
			}
			s := from + idx
			e := s + len(term)
			from = s + 1
			if !boundaryOk(chunk, s, e) {
				continue
			}
			overlap := false
			for _, h := range hits {
				if s < h.end && e > h.start {
					overlap = true
					break
				}
			}
			if overlap {
				continue
			}
			hits = append(hits, hit{s, e, term})
		}
	}
	out := chunk
	sort.Slice(hits, func(a, b int) bool { return hits[a].start > hits[b].start })
	for _, h := range hits {
		out = out[:h.start] + "***" + h.term + "***" + out[h.end:]
	}
	sort.Slice(hits, func(a, b int) bool { return hits[a].start < hits[b].start })
	return out, hits
}

// annotateText 对一段文本注词，返回新文本与本段命中的词（按首次位置排序）。
func annotateText(text string, terms []string) (string, []string) {
	first := map[string]int{}
	var order []string
	var outLines []string
	offset := 0
	for _, line := range strings.Split(text, "\n") {
		spans := protectedSpans(line)
		var sb strings.Builder
		pos := 0
		local := 0
		for _, sp := range spans {
			if sp[0] > pos {
				marked, hits := markChunk(line[pos:sp[0]], terms)
				sb.WriteString(marked)
				for _, h := range hits {
					if _, ok := first[h.term]; !ok {
						first[h.term] = offset + local + pos + h.start
						order = append(order, h.term)
					}
				}
			}
			sb.WriteString(line[sp[0]:sp[1]])
			local += sp[0] - pos
			local += sp[1] - sp[0]
			pos = sp[1]
		}
		if pos < len(line) {
			marked, hits := markChunk(line[pos:], terms)
			sb.WriteString(marked)
			for _, h := range hits {
				if _, ok := first[h.term]; !ok {
					first[h.term] = offset + local + h.start
					order = append(order, h.term)
				}
			}
		}
		outLines = append(outLines, sb.String())
		offset += len(line) + 1
	}
	sort.SliceStable(order, func(a, b int) bool { return first[order[a]] < first[order[b]] })
	return strings.Join(outLines, "\n"), order
}

type Annotation struct {
	Term        string
	Explanation string
}

func collapseBlanks(lines []string) string {
	var out []string
	inFence := false
	prevBlank := false
	for _, line := range lines {
		s := strings.TrimLeft(line, " \t")
		if strings.HasPrefix(s, "```") || strings.HasPrefix(s, "~~~") {
			inFence = !inFence
		}
		if !inFence && strings.TrimSpace(line) == "" {
			if prevBlank {
				continue
			}
			prevBlank = true
		} else {
			prevBlank = false
		}
		out = append(out, line)
	}
	return strings.Trim(strings.Join(out, "\n"), "\n") + "\n"
}

// locateTerms 返回每个词首次出现的可注段落序号（按 para/list 块计数）。
func locateTerms(body string, terms []string) map[string]int {
	var list []string
	for _, t := range terms {
		if t != "" {
			list = append(list, t)
		}
	}
	found := map[string]int{}
	para := 0
	for _, b := range splitBlocks(strings.Split(body, "\n")) {
		if b.kind != "para" && b.kind != "list" {
			continue
		}
		if len(list) == 0 {
			continue
		}
		_, ordered := annotateText(strings.Join(b.lines, "\n"), list)
		for _, term := range ordered {
			if _, ok := found[term]; !ok {
				found[term] = para
			}
		}
		para++
	}
	return found
}

func quoteLines(ordered []string, byTerm map[string]Annotation) []string {
	var lines []string
	for i, term := range ordered {
		if i > 0 {
			lines = append(lines, ">")
		}
		expl := strings.Join(strings.Fields(byTerm[term].Explanation), " ")
		lines = append(lines, "> "+term+"："+expl)
	}
	return lines
}

// renderMarkdown 渲染注释；每次传原始正文，返回 (Markdown, 未命中的词)。
func renderMarkdown(body string, anns []Annotation, firstParaOnly bool) (string, []string) {
	seen := map[string]bool{}
	var clean []Annotation
	var terms []string
	for _, a := range anns {
		t := strings.TrimSpace(a.Term)
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		clean = append(clean, Annotation{t, a.Explanation})
		terms = append(terms, t)
	}
	byTerm := map[string]Annotation{}
	for _, a := range clean {
		byTerm[a.Term] = a
	}
	annotated := map[string]bool{}
	var out []string
	for _, b := range splitBlocks(strings.Split(body, "\n")) {
		out = append(out, b.lines...)
		if b.kind != "para" && b.kind != "list" {
			continue
		}
		if len(terms) == 0 {
			continue
		}
		var active []string
		for _, t := range terms {
			if !annotated[t] {
				active = append(active, t)
			}
		}
		newText, ordered := annotateText(strings.Join(b.lines, "\n"), active)
		start := len(out) - len(b.lines)
		out = append(out[:start], strings.Split(newText, "\n")...)
		if len(ordered) > 0 {
			out = append(out, "")
			out = append(out, quoteLines(ordered, byTerm)...)
			out = append(out, "")
			for _, t := range ordered {
				annotated[t] = true
			}
		}
	}
	var missing []string
	for _, t := range terms {
		if !annotated[t] {
			missing = append(missing, t)
		}
	}
	return collapseBlanks(out), missing
}

// withSourceCite 在标题下插一行引用式出处，写回语雀时也能看到来源。
func withSourceCite(body, sourceURL string) string {
	if sourceURL == "" || strings.Contains(body, "> 本文由 [简悦 SimpRead](") {
		return body // 已注过出处就不再重复，保证可重入
	}
	u, err := url.Parse(sourceURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return body
	}
	safe := strings.NewReplacer("(", "%28", ")", "%29", " ", "%20", "<", "%3C", ">", "%3E").Replace(sourceURL)
	cite := "> 本文由 [简悦 SimpRead](https://simpread.pro) 转码，原文地址 [" + u.Host + "](" + safe + ")"
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "# ") {
			out := append([]string{}, lines[:i+1]...)
			out = append(out, "", cite)
			out = append(out, lines[i+1:]...)
			return strings.Join(out, "\n")
		}
	}
	return cite + "\n\n" + body
}
