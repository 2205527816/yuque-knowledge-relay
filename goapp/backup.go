package main

import (
	"bytes"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

var githubAPIBase = "https://api.github.com"

var reUnsafeSegment = regexp.MustCompile(`[\\/:*?"<>|\r\n\t]+`)
var reSpacesRun = regexp.MustCompile(`\s+`)

func safeSegment(value, fallback string, limit int) string {
	text := reUnsafeSegment.ReplaceAllString(value, "-")
	text = strings.Trim(text, " .-")
	text = reSpacesRun.ReplaceAllString(text, " ")
	r := []rune(text)
	if len(r) > limit {
		r = r[:limit]
	}
	text = strings.TrimSpace(string(r))
	if text == "" {
		return fallback
	}
	return text
}

func backupPath(doc Document, libraryName string) string {
	digest := "nohash"
	if len(doc.ContentHash) >= 8 {
		digest = doc.ContentHash[:8]
	}
	return "kb/" + safeSegment(libraryName, "未分类", 60) + "/" + digest + "-" + safeSegment(doc.Title, "untitled", 60) + ".md"
}

type backupFront struct {
	Title       string `yaml:"title"`
	SourceURL   string `yaml:"source_url"`
	Library     string `yaml:"library"`
	ContentHash string `yaml:"content_hash"`
	AnnotatedAt string `yaml:"annotated_at"`
	Terms       int    `yaml:"terms"`
	YuqueURL    string `yaml:"yuque_url,omitempty"`
}

func buildBackupMarkdown(doc Document, libraryName, rendered, yuqueURL string, terms int) string {
	front := backupFront{
		Title:       doc.Title,
		SourceURL:   doc.SourceURL,
		Library:     libraryName,
		ContentHash: doc.ContentHash,
		AnnotatedAt: time.Now().Format("2006-01-02T15:04:05-07:00"),
		Terms:       terms,
		YuqueURL:    yuqueURL,
	}
	raw, _ := yaml.Marshal(front)
	return "---\n" + string(raw) + "---\n\n" + strings.TrimRight(rendered, "\n") + "\n"
}

type backupSettings struct {
	Enabled  bool
	Repo     string
	Branch   string
	Token    string
	DryRun   bool
	Dir      string
	Required bool
	Order    string
}

func backupConfig(c Config) backupSettings {
	b := c.Backup
	get := func(key string) any { return b[key] }
	s := backupSettings{Branch: "main", Order: "before"}
	if v, ok := get("enabled").(bool); ok {
		s.Enabled = v
	}
	if v, ok := get("repo").(string); ok {
		s.Repo = v
	}
	if v, ok := get("branch").(string); ok && v != "" {
		s.Branch = v
	}
	s.Token = os.Getenv("GITHUB_TOKEN")
	if v, ok := get("token").(string); ok && v != "" && s.Token == "" {
		s.Token = v
	}
	if v, ok := get("dry_run").(bool); ok {
		s.DryRun = v
	}
	if v, ok := get("dir").(string); ok {
		s.Dir = v
	}
	if v, ok := get("required").(bool); ok {
		s.Required = v
	}
	if v, ok := get("order").(string); ok && v != "" {
		s.Order = v
	}
	return s
}

// githubPut 用 Contents API 幂等写入：内容未变（sha 相同）则不提交。
func githubPut(token, repo, branch, path, content, message string) (string, error) {
	base := githubAPIBase + "/repos/" + repo + "/contents/" + path
	client := &http.Client{Timeout: 30 * time.Second}
	do := func(method, url string, payload any) (*http.Response, []byte, error) {
		var body io.Reader
		if payload != nil {
			raw, _ := json.Marshal(payload)
			body = bytes.NewReader(raw)
		}
		req, err := http.NewRequest(method, url, body)
		if err != nil {
			return nil, nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			return nil, nil, err
		}
		data, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp, data, nil
	}
	encoded := base64.StdEncoding.EncodeToString([]byte(content))
	var sha string
	if resp, data, err := do("GET", base+"?ref="+branch, nil); err == nil && resp.StatusCode == 200 {
		var meta struct {
			SHA     string `json:"sha"`
			Content string `json:"content"`
		}
		json.Unmarshal(data, &meta)
		sha = meta.SHA
		if meta.Content != "" && strings.ReplaceAll(meta.Content, "\n", "") == encoded {
			return "", nil // 内容未变，不产生空提交
		}
	} else if err != nil {
		return "", err
	}
	payload := map[string]any{
		"message": message,
		"content": encoded,
		"branch":  branch,
	}
	if sha != "" {
		payload["sha"] = sha
	}
	for attempt := 0; attempt < 3; attempt++ {
		resp, data, err := do("PUT", base, payload)
		if err != nil {
			time.Sleep(time.Duration(500*(1<<attempt)) * time.Millisecond)
			continue
		}
		if resp.StatusCode == 409 || resp.StatusCode >= 500 {
			time.Sleep(time.Duration(500*(1<<attempt)) * time.Millisecond)
			continue
		}
		if resp.StatusCode >= 400 {
			return "", fmt.Errorf("%d %s", resp.StatusCode, truncate(string(data), 200))
		}
		var out struct {
			Commit struct {
				SHA string `json:"sha"`
			} `json:"commit"`
		}
		json.Unmarshal(data, &out)
		return out.Commit.SHA, nil
	}
	return "", fmt.Errorf("GitHub 备份请求失败")
}

// backupArticle 对应 Python 的 backup_article：写 backups 表并返回状态。
func backupArticle(db *sql.DB, c Config, doc Document, libraryName, rendered, yuqueURL string, terms int) map[string]any {
	s := backupConfig(c)
	if !s.Enabled || s.Repo == "" {
		return nil
	}
	path := backupPath(doc, libraryName)
	content := buildBackupMarkdown(doc, libraryName, rendered, yuqueURL, terms)
	result := map[string]any{"status": "skipped", "path": path}
	if s.DryRun {
		dir := s.Dir
		if dir == "" {
			dir = "backup_preview"
		}
		full := filepath.Join(rootDir, dir, path)
		os.MkdirAll(filepath.Dir(full), 0o755)
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			result["status"] = "failed"
			result["error"] = err.Error()
		} else {
			result["status"] = "dry_run"
		}
	} else if s.Token == "" {
		result["status"] = "failed"
		result["error"] = "未配置 GitHub token"
	} else {
		commit, err := githubPut(s.Token, s.Repo, s.Branch, path, content, "backup: "+doc.Title)
		if err != nil {
			result["status"] = "failed"
			result["error"] = err.Error()
		} else if commit == "" {
			result["status"] = "unchanged"
		} else {
			result["status"] = "succeeded"
			result["commit"] = commit
		}
	}
	db.Exec(`INSERT INTO backups (document_id, provider, path, content_sha, commit_sha, status, error)
		VALUES (?, 'github', ?, ?, ?, ?, ?)
		ON CONFLICT(document_id, provider) DO UPDATE SET path=excluded.path, content_sha=excluded.content_sha,
		commit_sha=excluded.commit_sha, status=excluded.status, error=excluded.error, updated_at=datetime('now')`,
		doc.ID, path, contentHash(content), strOrEmpty(result["commit"]), result["status"], strOrEmpty(result["error"]))
	return result
}

func strOrEmpty(v any) any {
	if s, ok := v.(string); ok && s != "" {
		return s
	}
	return nil
}

// recoverInterruptedJobs 启动时把中断在非终态的任务收口为 failed。
func recoverInterruptedJobs(db *sql.DB) int {
	rows, err := db.Query("SELECT id FROM documents WHERE stage IS NOT NULL AND stage NOT IN ('done','failed')")
	if err != nil {
		return 0
	}
	var ids []int64
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()
	for _, id := range ids {
		markFailed(db, id, errInvalid("服务重启，任务中断"))
		setStage(db, id, "failed", 100, "服务重启，任务中断")
	}
	for _, id := range ids {
		db.Exec("UPDATE batches SET status='finished', finished_at=datetime('now') WHERE id=(SELECT batch_id FROM documents WHERE id=?)", id)
	}
	return len(ids)
}
