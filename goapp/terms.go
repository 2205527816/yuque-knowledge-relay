package main

import (
	"encoding/json"
	"sort"
	"strings"
)

// Candidate 是 LLM 抽出来的候选词。
type Candidate struct {
	Term            string  `json:"term"`
	Type            string  `json:"type"`
	Importance      float64 `json:"importance"`
	ContextSentence string  `json:"context_sentence"`
	ParaIndex       int     `json:"-"`
}

const termsSystem = "你是中文文章编辑。找出普通读者阅读时可能需要注释的词，" +
	"包括专业术语、英文缩写、特定人物或机构、学术或商业概念、历史事件、技术或产品名。" +
	"排除日常词汇和纯数字。term 必须与原文用字完全一致。只输出 JSON。"

func termsMessages(title, body string, count int, bodyChars int) []map[string]string {
	runes := []rune(body)
	if len(runes) > bodyChars {
		runes = runes[:bodyChars]
	}
	user := "文章标题：" + title + "\n\n文章正文：\n" + string(runes) + "\n\n" +
		"请给出最多 " + itoa(count) + " 个候选词。输出格式：\n" +
		`{"terms":[{"term":"原词","type":"术语","importance":0.00,"context_sentence":"所在原句"}]}` + "\n" +
		"importance 为 0 到 1；term 不要改写、不要加引号；按重要度从高到低排列。"
	return []map[string]string{
		{"role": "system", "content": termsSystem},
		{"role": "user", "content": user},
	}
}

// extractJSON 从模型输出里挖出第一个 JSON 对象/数组。
func extractJSON(raw string) (string, bool) {
	s := strings.TrimSpace(raw)
	if i := strings.Index(s, "```"); i >= 0 {
		s = s[i+3:]
		if j := strings.Index(s, "```"); j >= 0 {
			s = s[:j]
		}
		s = strings.TrimPrefix(strings.TrimSpace(s), "json")
	}
	start := strings.IndexAny(s, "{[")
	if start < 0 {
		return "", false
	}
	end := strings.LastIndexAny(s, "}]")
	if end <= start {
		return "", false
	}
	return s[start : end+1], true
}

// parseCandidates 校验模型输出的候选词数组，去重并夹紧 importance。
func parseCandidates(raw string) ([]Candidate, error) {
	body, ok := extractJSON(raw)
	if !ok {
		return nil, errInvalid("没有 JSON")
	}
	var obj struct {
		Terms []Candidate `json:"terms"`
	}
	if err := json.Unmarshal([]byte(body), &obj); err != nil {
		var arr []Candidate
		if err2 := json.Unmarshal([]byte(body), &arr); err2 != nil {
			return nil, errInvalid("terms 不是数组")
		}
		obj.Terms = arr
	}
	seen := map[string]bool{}
	var out []Candidate
	for _, c := range obj.Terms {
		c.Term = strings.TrimSpace(c.Term)
		if c.Term == "" || seen[c.Term] {
			continue
		}
		seen[c.Term] = true
		if c.Importance < 0 {
			c.Importance = 0
		}
		if c.Importance > 1 {
			c.Importance = 1
		}
		c.Type = strings.TrimSpace(c.Type)
		c.ContextSentence = strings.TrimSpace(c.ContextSentence)
		out = append(out, c)
	}
	return out, nil
}

type plainError string

func (e plainError) Error() string { return string(e) }
func errInvalid(msg string) error  { return plainError(msg) }

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		return "-" + string(b)
	}
	return string(b)
}

// selectCandidates 只保留正文可注的词，去掉被长词包含的短词，取前 n 个，按段落顺序返回。
func selectCandidates(body string, candidates []Candidate, n int) []Candidate {
	names := make([]string, 0, len(candidates))
	for _, c := range candidates {
		names = append(names, c.Term)
	}
	found := locateTerms(body, names)
	var matched []Candidate
	for _, c := range candidates {
		if _, ok := found[c.Term]; ok {
			matched = append(matched, c)
		}
	}
	sort.SliceStable(matched, func(a, b int) bool { return len(matched[a].Term) > len(matched[b].Term) })
	var kept []Candidate
	for _, cand := range matched {
		skip := false
		for _, k := range kept {
			if cand.Term != k.Term && strings.Contains(k.Term, cand.Term) {
				skip = true
				break
			}
		}
		if !skip {
			kept = append(kept, cand)
		}
	}
	sort.SliceStable(kept, func(a, b int) bool { return kept[a].Importance > kept[b].Importance })
	if len(kept) > n {
		kept = kept[:n]
	}
	order := map[string]int{}
	for i, c := range candidates {
		order[c.Term] = i
	}
	for i := range kept {
		kept[i].ParaIndex = found[kept[i].Term]
	}
	sort.SliceStable(kept, func(a, b int) bool {
		if kept[a].ParaIndex != kept[b].ParaIndex {
			return kept[a].ParaIndex < kept[b].ParaIndex
		}
		return order[kept[a].Term] < order[kept[b].Term]
	})
	return kept
}

// sentenceFor 取包含该词的第一行，作为释义上下文。
func sentenceFor(term, body string) string {
	for _, line := range strings.Split(body, "\n") {
		if strings.Contains(line, term) {
			t := strings.TrimSpace(line)
			r := []rune(t)
			if len(r) > 300 {
				t = string(r[:300])
			}
			return t
		}
	}
	return ""
}
