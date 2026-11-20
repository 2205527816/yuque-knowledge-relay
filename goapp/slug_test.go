package main

import "testing"

// slugStub 模拟语雀已有同 slug 文档：CreateDoc 返回 slug_exists，FindDocBySlug 返回旧文档。
type slugStub struct{ adopted int64 }

func (s *slugStub) CreateDoc(ns, repo, title, body, slug string) (DocRef, error) {
	return DocRef{}, &YuqueError{`400 {"status":400,"code":"slug_exists","message":"slug already exists"}`}
}
func (s *slugStub) FindDocBySlug(ns, repo, slug string) (int64, error)  { return s.adopted, nil }
func (s *slugStub) GetDoc(ns, repo string, id int64) (RemoteDoc, error) { return RemoteDoc{}, nil }
func (s *slugStub) UpdateDoc(ns, repo string, id int64, title, body string) error {
	return nil
}
func (s *slugStub) DeleteDoc(ns, repo string, id int64) error  { return nil }
func (s *slugStub) GetTOC(ns, repo string) ([]TOCNode, error)  { return nil, nil }
func (s *slugStub) PrependTOC(ns, repo string, id int64) error { return nil }

func TestFindOrCreateDocAdoptsSlug(t *testing.T) {
	stub := &slugStub{adopted: 291919586}
	ref, err := findOrCreateDoc(stub, "example-space", "example-repo", "title", "body", "slugForTest")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ref.ID != stub.adopted {
		t.Fatalf("want adopted id %d, got %d", stub.adopted, ref.ID)
	}
}
