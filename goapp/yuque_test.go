package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// 开放 API 通道：建文档与插目录的请求形状。
func TestYuqueAPIPayloads(t *testing.T) {
	var gotMethod, gotPath, gotBody, gotToken string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		gotMethod, gotPath, gotBody, gotToken = r.Method, r.URL.Path, string(raw), r.Header.Get("X-Auth-Token")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":{"id":42}}`))
	}))
	defer srv.Close()
	api := newYuqueAPI(srv.URL, "tok")

	if _, err := api.CreateDoc("ns", "repo", "标题", "正文", "kb-x"); err != nil {
		t.Fatal(err)
	}
	if gotMethod != "POST" || gotPath != "/repos/ns/repo/docs" || gotToken != "tok" {
		t.Fatalf("建文档请求不对: %s %s token=%s", gotMethod, gotPath, gotToken)
	}
	var created map[string]any
	json.Unmarshal([]byte(gotBody), &created)
	if created["slug"] != "kb-x" || created["format"] != "markdown" || created["body"] != "正文" {
		t.Fatalf("建文档 payload 不对: %s", gotBody)
	}

	if err := api.PrependTOC("ns", "repo", 42); err != nil {
		t.Fatal(err)
	}
	if gotMethod != "PUT" || gotPath != "/repos/ns/repo/toc" {
		t.Fatalf("插目录请求不对: %s %s", gotMethod, gotPath)
	}
	var toc map[string]any
	json.Unmarshal([]byte(gotBody), &toc)
	if toc["action"] != "prependNode" || toc["action_mode"] != "child" || toc["type"] != "DOC" {
		t.Fatalf("插目录 payload 不对: %s", gotBody)
	}
	if ids, ok := toc["doc_ids"].([]any); !ok || len(ids) != 1 || ids[0].(float64) != 42 {
		t.Fatalf("doc_ids 不对: %v", toc["doc_ids"])
	}
}

// 网页 cookie 通道：先查书 id，再 add_to_catalog。
func TestYuqueWebPayloads(t *testing.T) {
	var paths []string
	var catalog string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		paths = append(paths, r.Method+" "+r.URL.Path)
		if r.Header.Get("Cookie") == "" {
			t.Errorf("缺少 cookie")
		}
		switch r.URL.Path {
		case "/api/mine/books":
			w.Write([]byte(`{"data":[{"id":7,"slug":"repo"}]}`))
		case "/api/docs":
			w.Write([]byte(`{"data":{"id":42}}`))
		case "/api/docs/add_to_catalog":
			catalog = string(raw)
			w.Write([]byte(`{"data":[]}`))
		default:
			w.Write([]byte(`{"data":{}}`))
		}
	}))
	defer srv.Close()
	web := newYuqueWeb(srv.URL, "sess", "ctok")
	if _, err := web.CreateDoc("ns", "repo", "t", "b", "kb-x"); err != nil {
		t.Fatal(err)
	}
	if err := web.PrependTOC("ns", "repo", 42); err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	json.Unmarshal([]byte(catalog), &payload)
	if payload["book_id"].(float64) != 7 {
		t.Fatalf("book_id 不对: %s", catalog)
	}
	if ids, ok := payload["ids"].([]any); !ok || len(ids) != 1 || ids[0].(float64) != 42 {
		t.Fatalf("ids 不对: %s", catalog)
	}
	// 书 id 应缓存，只查一次
	count := 0
	for _, p := range paths {
		if strings.Contains(p, "/api/mine/books") {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("书 id 未缓存，查询 %d 次", count)
	}
}

// 302 跳登录页 = 登录态失效，必须能识别为 401。
func TestYuqueWebRedirectIsAuthError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/login", http.StatusFound)
	}))
	defer srv.Close()
	web := newYuqueWeb(srv.URL, "sess", "ctok")
	_, err := web.CreateDoc("ns", "repo", "t", "b", "s")
	if err == nil || !isAuthError(err) {
		t.Fatalf("期望 401 类错误，得到 %v", err)
	}
}

// 回落只对登录态错误生效，429 不切换。
func TestFallbackOnlyOnAuthError(t *testing.T) {
	yuqueB.reset()
	first := &stubClient{err: &YuqueError{"401 Unauthorized"}}
	second := &stubClient{docID: 9}
	chain := &fallbackClient{clients: []YuqueClient{first, second}}
	ref, err := chain.CreateDoc("ns", "repo", "t", "b", "s")
	if err != nil || ref.ID != 9 || first.calls != 1 || second.calls != 1 {
		t.Fatalf("401 应回落: ref=%v err=%v first=%d second=%d", ref, err, first.calls, second.calls)
	}

	first2 := &stubClient{err: &YuqueError{"429 Too Many Requests"}}
	second2 := &stubClient{docID: 9}
	chain2 := &fallbackClient{clients: []YuqueClient{first2, second2}}
	if _, err := chain2.CreateDoc("ns", "repo", "t", "b", "s"); err == nil {
		t.Fatal("429 不应该回落成功")
	}
	if second2.calls != 0 {
		t.Fatalf("429 不应调用第二通道，实际 %d", second2.calls)
	}
}

// 熔断阶梯 60s → 300s → 900s，一小时后归零。
func TestBreakerEscalation(t *testing.T) {
	yuqueB.reset()
	d1 := yuqueB.trip()
	d2 := yuqueB.trip()
	d3 := yuqueB.trip()
	d4 := yuqueB.trip()
	if d1 != 60*time.Second || d2 != 300*time.Second || d3 != 900*time.Second || d4 != 900*time.Second {
		t.Fatalf("阶梯不对: %v %v %v %v", d1, d2, d3, d4)
	}
	if !yuqueB.blocked() {
		t.Fatal("应处于熔断中")
	}
	yuqueB.reset()
	if yuqueB.blocked() {
		t.Fatal("reset 后不应熔断")
	}
}

type stubClient struct {
	err   error
	docID int64
	calls int
}

func (s *stubClient) CreateDoc(ns, repo, title, body, slug string) (DocRef, error) {
	s.calls++
	if s.err != nil {
		return DocRef{}, s.err
	}
	return DocRef{ID: s.docID}, nil
}
func (s *stubClient) GetDoc(ns, repo string, id int64) (RemoteDoc, error) {
	s.calls++
	return RemoteDoc{ID: id}, s.err
}
func (s *stubClient) UpdateDoc(ns, repo string, id int64, title, body string) error {
	s.calls++
	return s.err
}
func (s *stubClient) DeleteDoc(ns, repo string, id int64) error { s.calls++; return s.err }
func (s *stubClient) GetTOC(ns, repo string) ([]TOCNode, error) {
	s.calls++
	return nil, s.err
}
func (s *stubClient) PrependTOC(ns, repo string, id int64) error { s.calls++; return s.err }
