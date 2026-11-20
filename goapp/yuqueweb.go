package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const browserUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36"

// YuqueWeb 用登录态 cookie 调语雀网页接口；读不到正文（body 恒为空）。
type YuqueWeb struct {
	base   string
	sess   string
	ctoken string
	http   *http.Client
	mu     sync.Mutex
	books  map[string]int64
}

func newYuqueWeb(base, session, ctoken string) *YuqueWeb {
	return &YuqueWeb{
		base:   strings.TrimRight(base, "/"),
		sess:   session,
		ctoken: ctoken,
		http: &http.Client{
			Timeout: 30 * time.Second,
			// 不跟重定向：登录态失效时语雀会 302 到登录页，必须自己看到 3xx
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		books: map[string]int64{},
	}
}

func (w *YuqueWeb) request(method, path string, query url.Values, payload any, out any) error {
	full := w.base + path
	if len(query) > 0 {
		full += "?" + query.Encode()
	}
	var last string
	for attempt := 0; attempt < 3; attempt++ {
		var body io.Reader
		if payload != nil {
			raw, _ := json.Marshal(payload)
			body = bytes.NewReader(raw)
		}
		req, err := http.NewRequest(method, full, body)
		if err != nil {
			return &YuqueError{err.Error()}
		}
		req.Header.Set("Cookie", "_yuque_session="+w.sess+"; yuque_ctoken="+w.ctoken)
		req.Header.Set("x-csrf-token", w.ctoken)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", browserUA)
		req.Header.Set("Referer", w.base+"/")
		resp, err := w.http.Do(req)
		if err != nil {
			last = err.Error()
			time.Sleep(time.Duration(500*(1<<attempt)) * time.Millisecond)
			continue
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			return &YuqueError{"401 语雀登录态已失效（被重定向到登录页），请更新 cookie"}
		}
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

func (w *YuqueWeb) bookID(ns, repo string) (int64, error) {
	key := ns + "/" + repo
	w.mu.Lock()
	if id, ok := w.books[key]; ok {
		w.mu.Unlock()
		return id, nil
	}
	w.mu.Unlock()

	var books []struct {
		ID   int64  `json:"id"`
		Slug string `json:"slug"`
	}
	if err := w.request("GET", "/api/mine/books", url.Values{"limit": {"100"}}, nil, &books); err != nil {
		if isAuthError(err) {
			return 0, err
		}
	} else {
		w.mu.Lock()
		for _, b := range books {
			w.books[ns+"/"+b.Slug] = b.ID
		}
		w.mu.Unlock()
	}
	if id, ok := w.books[key]; ok {
		return id, nil
	}
	var groupBooks []struct {
		ID   int64  `json:"id"`
		Slug string `json:"slug"`
	}
	if err := w.request("GET", "/api/books", url.Values{"namespace": {ns}, "limit": {"100"}}, nil, &groupBooks); err != nil {
		if isAuthError(err) {
			return 0, err
		}
	} else {
		for _, b := range groupBooks {
			w.books[ns+"/"+b.Slug] = b.ID
		}
	}
	if id, ok := w.books[key]; ok {
		return id, nil
	}
	return 0, &YuqueError{"语雀里找不到知识库 " + key}
}

func (w *YuqueWeb) CreateDoc(ns, repo, title, body, slug string) (DocRef, error) {
	bid, err := w.bookID(ns, repo)
	if err != nil {
		return DocRef{}, err
	}
	var out DocRef
	err = w.request("POST", "/api/docs", nil, map[string]any{
		"book_id": bid, "title": title, "slug": slug, "format": "markdown", "body": body, "public": 0,
	}, &out)
	return out, err
}

func (w *YuqueWeb) FindDocBySlug(ns, repo, slug string) (int64, error) {
	bid, err := w.bookID(ns, repo)
	if err != nil {
		return 0, err
	}
	type item struct {
		ID   int64  `json:"id"`
		Slug string `json:"slug"`
	}
	for offset := 0; offset < 1000; offset += 100 {
		var raw json.RawMessage
		err := w.request("GET", fmt.Sprintf("/api/books/%d/docs", bid), url.Values{"offset": {fmt.Sprint(offset)}, "limit": {"100"}}, nil, &raw)
		if err != nil {
			return 0, err
		}
		var items []item
		if err := json.Unmarshal(raw, &items); err != nil {
			var wrap struct {
				Docs []item `json:"docs"`
			}
			if err := json.Unmarshal(raw, &wrap); err != nil {
				return 0, &YuqueError{"语雀网页接口返回结构异常"}
			}
			items = wrap.Docs
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

func (w *YuqueWeb) GetDoc(ns, repo string, id int64) (RemoteDoc, error) {
	var out RemoteDoc
	err := w.request("GET", "/api/docs/doc_with_author_text", url.Values{"id": {fmt.Sprint(id)}}, nil, &out)
	return out, err
}

func (w *YuqueWeb) UpdateDoc(ns, repo string, id int64, title, body string) error {
	return w.request("PUT", fmt.Sprintf("/api/docs/%d", id), nil,
		map[string]any{"title": title, "body": body, "format": "markdown"}, nil)
}

func (w *YuqueWeb) DeleteDoc(ns, repo string, id int64) error {
	return w.request("DELETE", fmt.Sprintf("/api/docs/%d", id), nil, nil, nil)
}

func (w *YuqueWeb) GetTOC(ns, repo string) ([]TOCNode, error) {
	bid, err := w.bookID(ns, repo)
	if err != nil {
		return nil, err
	}
	var out struct {
		TOC []TOCNode `json:"toc"`
	}
	if err := w.request("GET", fmt.Sprintf("/api/books/%d/toc", bid), nil, nil, &out); err != nil {
		return nil, err
	}
	return out.TOC, nil
}

func (w *YuqueWeb) PrependTOC(ns, repo string, id int64) error {
	bid, err := w.bookID(ns, repo)
	if err != nil {
		return err
	}
	return w.request("POST", "/api/docs/add_to_catalog", nil, map[string]any{
		"book_id": bid, "ids": []int64{id},
	}, nil)
}
