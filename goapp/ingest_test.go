package main

import (
	"reflect"
	"testing"
)

type mdCase struct {
	Name        string `json:"name"`
	Text        string `json:"text"`
	File        string `json:"file"`
	Title       string `json:"title"`
	SourceURL   string `json:"source_url"`
	Author      string `json:"author"`
	PublishedAt string `json:"published_at"`
	BodyMD      string `json:"body_md"`
	ContentHash string `json:"content_hash"`
}

type urlCase struct {
	In  string `json:"in"`
	Out string `json:"out"`
}

type hashCase struct {
	In  string `json:"in"`
	Out string `json:"out"`
}

type scoreCase struct {
	Name   string             `json:"name"`
	Raw    string             `json:"raw"`
	OK     bool               `json:"ok"`
	Pick   string             `json:"pick"`
	Err    string             `json:"err"`
	Scores map[string]float64 `json:"scores"`
}

type ingestFixture struct {
	MD     []mdCase    `json:"md"`
	URLs   []urlCase   `json:"urls"`
	Hashes []hashCase  `json:"hashes"`
	Scores []scoreCase `json:"scores"`
}

// 与 Python 基线对比解析、URL 归一化、正文 hash、分类校验与选库。
func TestIngestMatchesPythonBaseline(t *testing.T) {
	fx := loadJSON[ingestFixture](t, "testdata/ingest_expected.json")
	for _, c := range fx.MD {
		got := parseSimpread(c.Text, c.File)
		if got.Title != c.Title || got.SourceURL != c.SourceURL || got.Author != c.Author ||
			got.PublishedAt != c.PublishedAt || got.BodyMD != c.BodyMD || got.ContentHash != c.ContentHash {
			t.Errorf("parse %s 不一致:\n got=%+v\nwant=%+v", c.Name, got,
				ParsedDoc{c.Title, c.SourceURL, c.Author, c.PublishedAt, c.BodyMD, c.ContentHash})
		}
	}
	for _, c := range fx.URLs {
		if got := normalizeURL(c.In); got != c.Out {
			t.Errorf("normalizeURL(%q) = %q, want %q", c.In, got, c.Out)
		}
	}
	for _, c := range fx.Hashes {
		if got := contentHash(c.In); got != c.Out {
			t.Errorf("contentHash(%q) = %q, want %q", c.In, got, c.Out)
		}
	}
	names := []string{"甲", "乙", "丙"}
	for _, c := range fx.Scores {
		scores, err := parseScores(c.Raw, names)
		if !c.OK {
			if err == nil {
				t.Errorf("case %s 应该失败却通过: %v", c.Name, scores)
			}
			continue
		}
		if err != nil {
			t.Errorf("case %s 应该通过却失败: %v", c.Name, err)
			continue
		}
		if !reflect.DeepEqual(scores, c.Scores) {
			t.Errorf("case %s 分数不一致: %v vs %v", c.Name, scores, c.Scores)
		}
		if got := pickLibrary(scores, 0.5); got != c.Pick {
			t.Errorf("case %s 选库 %q != %q", c.Name, got, c.Pick)
		}
	}
}
