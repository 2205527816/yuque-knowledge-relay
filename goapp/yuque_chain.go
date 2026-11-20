package main

import (
	"strings"
	"sync"
	"time"
)

// 语雀 429 多是秒/分钟级突发限制，静一下就能好；连续撞才拉长。
var yuqueBlockSteps = []time.Duration{60 * time.Second, 300 * time.Second, 900 * time.Second}

type yuqueBreaker struct {
	mu    sync.Mutex
	until time.Time
	hits  int
	last  time.Time
}

var yuqueB yuqueBreaker

func (b *yuqueBreaker) blocked() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return time.Now().Before(b.until)
}

func (b *yuqueBreaker) trip() time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	if now.Sub(b.last) > time.Hour {
		b.hits = 0
	}
	idx := b.hits
	if idx >= len(yuqueBlockSteps) {
		idx = len(yuqueBlockSteps) - 1
	}
	d := yuqueBlockSteps[idx]
	b.hits++
	b.last = now
	b.until = now.Add(d)
	return d
}

func (b *yuqueBreaker) reset() {
	b.mu.Lock()
	b.until = time.Time{}
	b.hits = 0
	b.last = time.Time{}
	b.mu.Unlock()
}

// fallbackClient 让 cookie 与 token 互为备份：只对 401/403 切换，429/网络错误不切。
type fallbackClient struct{ clients []YuqueClient }

func (f *fallbackClient) call(op func(YuqueClient) error) error {
	var last error
	for i, c := range f.clients {
		err := op(c)
		if err == nil {
			return nil
		}
		last = err
		if !isAuthError(err) || i == len(f.clients)-1 {
			return err
		}
	}
	return last
}

func (f *fallbackClient) CreateDoc(ns, repo, title, body, slug string) (DocRef, error) {
	var out DocRef
	err := f.call(func(c YuqueClient) error {
		var e error
		out, e = c.CreateDoc(ns, repo, title, body, slug)
		return e
	})
	return out, err
}

func (f *fallbackClient) FindDocBySlug(ns, repo, slug string) (int64, error) {
	var last error
	for _, c := range f.clients {
		finder, ok := c.(interface {
			FindDocBySlug(string, string, string) (int64, error)
		})
		if !ok {
			continue
		}
		id, err := finder.FindDocBySlug(ns, repo, slug)
		if err == nil {
			return id, nil
		}
		last = err
	}
	if last == nil {
		last = &YuqueError{"当前语雀通道不支持按 slug 查找"}
	}
	return 0, last
}

func (f *fallbackClient) GetDoc(ns, repo string, id int64) (RemoteDoc, error) {
	// cookie 通道读不到正文；优先返回带正文的通道，冲突检测才有依据。
	var out RemoteDoc
	var last error
	for _, c := range f.clients {
		doc, err := c.GetDoc(ns, repo, id)
		if err != nil {
			last = err
			if !isAuthError(err) {
				break
			}
			continue
		}
		if doc.Body != nil && strings.TrimSpace(*doc.Body) != "" {
			return doc, nil
		}
		if out.ID == 0 {
			out = doc
		}
	}
	if out.ID != 0 {
		return out, nil
	}
	if last == nil {
		last = &YuqueError{"语雀文档读取失败"}
	}
	return out, last
}

func (f *fallbackClient) UpdateDoc(ns, repo string, id int64, title, body string) error {
	return f.call(func(c YuqueClient) error { return c.UpdateDoc(ns, repo, id, title, body) })
}

func (f *fallbackClient) DeleteDoc(ns, repo string, id int64) error {
	return f.call(func(c YuqueClient) error { return c.DeleteDoc(ns, repo, id) })
}

func (f *fallbackClient) GetTOC(ns, repo string) ([]TOCNode, error) {
	var out []TOCNode
	err := f.call(func(c YuqueClient) error {
		var e error
		out, e = c.GetTOC(ns, repo)
		return e
	})
	return out, err
}

func (f *fallbackClient) PrependTOC(ns, repo string, id int64) error {
	return f.call(func(c YuqueClient) error { return c.PrependTOC(ns, repo, id) })
}

// newYuqueClients cookie 优先，登录态失效时回落个人 token。
func newYuqueClients(c Config) YuqueClient {
	var clients []YuqueClient
	if c.Yuque.Session != "" && c.Yuque.Ctoken != "" {
		clients = append(clients, newYuqueWeb("https://www.yuque.com", c.Yuque.Session, c.Yuque.Ctoken))
	}
	if c.Yuque.Token != "" {
		clients = append(clients, newYuqueAPI(c.Yuque.BaseURL, c.Yuque.Token))
	}
	var base YuqueClient
	switch {
	case len(clients) == 0:
		return nil
	case len(clients) == 1:
		base = clients[0]
	default:
		base = &fallbackClient{clients: clients}
	}
	return breakerGuard{inner: base}
}

// breakerGuard 统一在客户端层触发 429 熔断，避免只有建档路径才 trip。
type breakerGuard struct{ inner YuqueClient }

func (g breakerGuard) note(err error) error {
	if err != nil && strings.Contains(err.Error(), "429") {
		yuqueB.trip()
	}
	return err
}

func (g breakerGuard) CreateDoc(ns, repo, title, body, slug string) (DocRef, error) {
	ref, err := g.inner.CreateDoc(ns, repo, title, body, slug)
	return ref, g.note(err)
}

func (g breakerGuard) FindDocBySlug(ns, repo, slug string) (int64, error) {
	finder, ok := g.inner.(interface {
		FindDocBySlug(string, string, string) (int64, error)
	})
	if !ok {
		return 0, &YuqueError{"当前语雀通道不支持按 slug 查找"}
	}
	id, err := finder.FindDocBySlug(ns, repo, slug)
	return id, g.note(err)
}
func (g breakerGuard) GetDoc(ns, repo string, id int64) (RemoteDoc, error) {
	doc, err := g.inner.GetDoc(ns, repo, id)
	return doc, g.note(err)
}
func (g breakerGuard) UpdateDoc(ns, repo string, id int64, title, body string) error {
	return g.note(g.inner.UpdateDoc(ns, repo, id, title, body))
}
func (g breakerGuard) DeleteDoc(ns, repo string, id int64) error {
	return g.note(g.inner.DeleteDoc(ns, repo, id))
}
func (g breakerGuard) GetTOC(ns, repo string) ([]TOCNode, error) {
	nodes, err := g.inner.GetTOC(ns, repo)
	return nodes, g.note(err)
}
func (g breakerGuard) PrependTOC(ns, repo string, id int64) error {
	return g.note(g.inner.PrependTOC(ns, repo, id))
}

// remaining 仅供测试观察当前熔断剩余时间。
func (b *yuqueBreaker) remaining() time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	return time.Until(b.until)
}
