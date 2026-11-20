package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const classifySystem = "你是中文知识库分类器。根据文章内容，给每个候选知识库打 0 到 1 分，" +
	"表示这篇文章属于该库的程度。只输出 JSON，不要输出其他文字。"

type Profile struct {
	Name        string
	Description string
}

func classifyMessages(title, body string, profiles []Profile, bodyChars int) []map[string]string {
	runes := []rune(body)
	if len(runes) > bodyChars {
		runes = runes[:bodyChars]
	}
	var blocks []string
	for _, p := range profiles {
		blocks = append(blocks, "### "+p.Name+"\n"+strings.TrimSpace(p.Description))
	}
	user := "文章标题：" + title + "\n\n" +
		"文章正文（最多 " + itoa(bodyChars) + " 字）：\n" + string(runes) + "\n\n" +
		"候选知识库（共 " + itoa(len(profiles)) + " 个）：\n\n" + strings.Join(blocks, "\n\n") + "\n\n" +
		`输出格式：{"scores":[{"library":"库名","score":0.00,"reason":"不超过20字"}]}` + "\n" +
		"必须覆盖全部 " + itoa(len(profiles)) + " 个库，分数保留两位小数。"
	return []map[string]string{
		{"role": "system", "content": classifySystem},
		{"role": "user", "content": user},
	}
}

// parseScores 校验并返回 {库名: 分数}；缺库、重库、越界都算无效。
func parseScores(raw string, names []string) (map[string]float64, error) {
	body, ok := extractJSON(raw)
	if !ok {
		return nil, errInvalid("输出中没有 JSON 对象")
	}
	var items []struct {
		Library string  `json:"library"`
		Score   float64 `json:"score"`
	}
	var obj struct {
		Scores []struct {
			Library string  `json:"library"`
			Score   float64 `json:"score"`
		} `json:"scores"`
	}
	if err := json.Unmarshal([]byte(body), &obj); err == nil {
		items = obj.Scores
	} else if err2 := json.Unmarshal([]byte(body), &items); err2 != nil {
		return nil, errInvalid("scores 不是数组")
	}
	known := map[string]bool{}
	for _, n := range names {
		known[n] = true
	}
	scores := map[string]float64{}
	for _, it := range items {
		name := strings.TrimSpace(it.Library)
		if !known[name] {
			return nil, errInvalid("未知库名：" + name)
		}
		if _, dup := scores[name]; dup {
			return nil, errInvalid("重复库名：" + name)
		}
		if it.Score < 0 || it.Score > 1 {
			return nil, errInvalid(name + " 的分数越界")
		}
		scores[name] = it.Score
	}
	var missing []string
	for _, n := range names {
		if _, ok := scores[n]; !ok {
			missing = append(missing, n)
		}
	}
	if len(missing) > 0 {
		return nil, errInvalid("缺少库：" + strings.Join(missing, "、"))
	}
	return scores, nil
}

// pickLibrary 取最高分；低于阈值或并列最高返回空（进待定库）。
func pickLibrary(scores map[string]float64, threshold float64) string {
	best := -1.0
	var bestName []string
	for name, score := range scores {
		if score > best {
			best, bestName = score, []string{name}
		} else if score == best {
			bestName = append(bestName, name)
		}
	}
	if best < threshold {
		return ""
	}
	if len(bestName) != 1 {
		return ""
	}
	return bestName[0]
}

// loadProfiles 读取 profiles/<库名>.md。
func loadProfiles(dir string) ([]Profile, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []Profile
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		out = append(out, Profile{
			Name:        strings.TrimSuffix(e.Name(), ".md"),
			Description: string(raw),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
