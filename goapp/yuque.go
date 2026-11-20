package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// YuqueError 对应 Python 的 YuqueError；AuthError 靠消息前缀 401/403 判定。
type YuqueError struct{ Msg string }

func (e *YuqueError) Error() string { return e.Msg }

func isAuthError(err error) bool {
	msg := err.Error()
	return strings.HasPrefix(msg, "401") || strings.HasPrefix(msg, "403")
}

type DocRef struct {
	ID int64 `json:"id"`
}

type RemoteDoc struct {
	ID     int64   `json:"id"`
	Title  string  `json:"title"`
	Format string  `json:"format"`
	Body   *string `json:"body"`
}

type TOCNode struct {
	Type  string `json:"type"`
	DocID int64  `json:"doc_id"`
	Title string `json:"title"`
	UUID  string `json:"uuid"`
}

// YuqueClient 是两条通道的共同接口。
type YuqueClient interface {
	CreateDoc(ns, repo, title, body, slug string) (DocRef, error)
	GetDoc(ns, repo string, id int64) (RemoteDoc, error)
	UpdateDoc(ns, repo string, id int64, title, body string) error
	DeleteDoc(ns, repo string, id int64) error
	GetTOC(ns, repo string) ([]TOCNode, error)
	PrependTOC(ns, repo string, id int64) error
}

// YuqueAPI 是开放平台 v2 通道（个人 token）。
type YuqueAPI struct {
	base  string
	token string
	http  *http.Client
}

func newYuqueAPI(base, token string) *YuqueAPI {
	return &YuqueAPI{base: strings.TrimRight(base, "/"), token: token, http: &http.Client{Timeout: 30 * time.Second}}
}

func (y *YuqueAPI) request(method, path string, payload any, out any) error {
	var last string
	for attempt := 0; attempt < 3; attempt++ {
		var body io.Reader
		if payload != nil {
			raw, _ := json.Marshal(payload)
			body = bytes.NewReader(raw)
		}
		req, err := http.NewRequest(method, y.base+path, body)
		if err != nil {
			return &YuqueError{err.Error()}
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Auth-Token", y.token)
		resp, err := y.http.Do(req)
		if err != nil {
			last = err.Error()
			time.Sleep(time.Duration(500*(1<<attempt)) * time.Millisecond)
			continue
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == 429 || resp.StatusCode >= 500 {
			last = fmt.Sprintf("%d %s", resp.StatusCode, truncate(string(raw), 200))
			time.Sleep(time.Duration(500*(1<<attempt)) * time.Millisecond)
			continue
		}
		if resp.StatusCode >= 400 {
			return &YuqueError{fmt.Sprintf("%d %s", resp.StatusCode, truncate(string(raw), 300))}
		}
		var envelope struct {
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(raw, &envelope); err != nil {
			return &YuqueError{"语雀响应不是 JSON：" + truncate(string(raw), 120)}
		}
		if out != nil {
			if len(envelope.Data) == 0 || string(envelope.Data) == "null" {
				return &YuqueError{"语雀响应缺少 data"}
			}
			if err := json.Unmarshal(envelope.Data, out); err != nil {
				return &YuqueError{"语雀响应结构异常：" + truncate(string(envelope.Data), 120)}
			}
		}
		return nil
	}
	return &YuqueError{"语雀请求失败：" + last}
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

func (y *YuqueAPI) CreateDoc(ns, repo, title, body, slug string) (DocRef, error) {
	var out DocRef
	err := y.request("POST", fmt.Sprintf("/repos/%s/%s/docs", ns, repo), map[string]any{
		"title": title, "slug": slug, "format": "markdown", "body": body, "public": 0,
	}, &out)
	return out, err
}

type docListItem struct {
	ID   int64  `json:"id"`
	Slug string `json:"slug"`
}

// FindDocBySlug 按 slug 找库里的已有文档；CreateDoc 撞 slug_exists 时用它接管旧文档。
func (y *YuqueAPI) FindDocBySlug(ns, repo, slug string) (int64, error) {
	for offset := 0; offset < 1000; offset += 100 {
		var items []docListItem
		err := y.request("GET", fmt.Sprintf("/repos/%s/%s/docs?offset=%d&limit=100", ns, repo, offset), nil, &items)
		if err != nil {
			return 0, err
		}
		for _, it := range items {
			if it.Slug == slug {
				return it.ID, nil
			}
		}
		if len(items) < 100 {
			break
		}
	}
	return 0, &YuqueError{"语雀库里找不到 slug " + slug}
}

func (y *YuqueAPI) GetDoc(ns, repo string, id int64) (RemoteDoc, error) {
	var out RemoteDoc
	err := y.request("GET", fmt.Sprintf("/repos/%s/%s/docs/%d", ns, repo, id), nil, &out)
	return out, err
}

func (y *YuqueAPI) UpdateDoc(ns, repo string, id int64, title, body string) error {
	return y.request("PUT", fmt.Sprintf("/repos/%s/%s/docs/%d", ns, repo, id),
		map[string]any{"title": title, "body": body, "format": "markdown"}, nil)
}

func (y *YuqueAPI) DeleteDoc(ns, repo string, id int64) error {
	return y.request("DELETE", fmt.Sprintf("/repos/%s/%s/docs/%d", ns, repo, id), nil, nil)
}

func (y *YuqueAPI) GetTOC(ns, repo string) ([]TOCNode, error) {
	var nodes []TOCNode
	err := y.request("GET", fmt.Sprintf("/repos/%s/%s/toc", ns, repo), nil, &nodes)
	return nodes, err
}

func (y *YuqueAPI) PrependTOC(ns, repo string, id int64) error {
	return y.request("PUT", fmt.Sprintf("/repos/%s/%s/toc", ns, repo), map[string]any{
		"action": "prependNode", "action_mode": "child", "type": "DOC", "doc_ids": []int64{id},
	}, nil)
}
