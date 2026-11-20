package main

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

type caseFile struct {
	Body  string            `json:"body"`
	Terms []string          `json:"terms"`
	Expl  map[string]string `json:"expl"`
}

type expectFile struct {
	MD      string         `json:"md"`
	Missing []string       `json:"missing"`
	Units   int            `json:"units"`
	Located map[string]int `json:"located"`
	Picked  []string       `json:"picked"`
}

func loadJSON[T any](t *testing.T, path string) T {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读 %s: %v", path, err)
	}
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("解析 %s: %v", path, err)
	}
	return v
}

// 与 Python 基线逐例对比：渲染结果、未命中词、字数、定位段落、选中词。
func TestRenderMatchesPythonBaseline(t *testing.T) {
	cases := loadJSON[[]caseFile](t, "testdata/cases.json")
	expected := loadJSON[[]expectFile](t, "testdata/expected.json")
	if len(cases) != len(expected) {
		t.Fatalf("用例数不一致: %d vs %d", len(cases), len(expected))
	}
	for i, c := range cases {
		var anns []Annotation
		for _, term := range c.Terms {
			anns = append(anns, Annotation{Term: term, Explanation: c.Expl[term]})
		}
		md, missing := renderMarkdown(c.Body, anns, false)
		if md != expected[i].MD {
			t.Errorf("case %d 渲染不一致\n--- got ---\n%s\n--- want ---\n%s", i, md, expected[i].MD)
		}
		if len(missing) != len(expected[i].Missing) || (len(missing) > 0 && !reflect.DeepEqual(missing, expected[i].Missing)) {
			t.Errorf("case %d 未命中词不一致: %v vs %v", i, missing, expected[i].Missing)
		}
		if got := countUnits(c.Body); got != expected[i].Units {
			t.Errorf("case %d 字数 %d != %d", i, got, expected[i].Units)
		}
		if got := locateTerms(c.Body, c.Terms); !reflect.DeepEqual(got, expected[i].Located) {
			t.Errorf("case %d 定位不一致: %v vs %v", i, got, expected[i].Located)
		}
		var cands []Candidate
		for _, term := range c.Terms {
			cands = append(cands, Candidate{Term: term, Importance: 1, Type: ""})
		}
		picked := selectCandidates(c.Body, cands, 14)
		var names []string
		for _, p := range picked {
			names = append(names, p.Term)
		}
		if len(names) != len(expected[i].Picked) || (len(names) > 0 && !reflect.DeepEqual(names, expected[i].Picked)) {
			t.Errorf("case %d 选中词不一致: %v vs %v", i, names, expected[i].Picked)
		}
	}
}

func TestRecommendedCount(t *testing.T) {
	if got := recommendedCount(0, 1, 2.5, 3, 14); got != 3 {
		t.Errorf("下限失效: %d", got)
	}
	if got := recommendedCount(50000, 1, 2.5, 3, 14); got != 14 {
		t.Errorf("上限失效: %d", got)
	}
	if got := recommendedCount(1200, 1, 2.5, 3, 14); got != 3 {
		t.Errorf("1200 字应为 3，得到 %d", got)
	}
}

func TestParseCandidatesTolerance(t *testing.T) {
	cands, err := parseCandidates("```json\n{\"terms\":[{\"term\":\"A\",\"importance\":1.5},{\"term\":\"A\"},{\"term\":\"B\"}]}\n```")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(cands) != 2 || cands[0].Importance != 1 || cands[0].Term != "A" {
		t.Fatalf("去重或夹紧失败: %+v", cands)
	}
}
