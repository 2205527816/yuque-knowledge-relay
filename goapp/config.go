package main

import (
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// ConfigLibrary 是 config.yaml 中 libraries 的一项。
type ConfigLibrary struct {
	Name       string  `yaml:"name"`
	Namespace  string  `yaml:"namespace"`
	Slug       string  `yaml:"slug"`
	Multiplier float64 `yaml:"multiplier"`
}

// Config 对应 config.yaml，只保留 Go 侧实际用到的字段。
type Config struct {
	Yuque struct {
		Token          string `yaml:"token"`
		BaseURL        string `yaml:"base_url"`
		PendingLibrary string `yaml:"pending_library"`
		Session        string `yaml:"session"`
		Ctoken         string `yaml:"ctoken"`
	} `yaml:"yuque"`
	LLM struct {
		BaseURL       string `yaml:"base_url"`
		APIKey        string `yaml:"api_key"`
		ClassifyModel string `yaml:"classify_model"`
		ExplainModel  string `yaml:"explain_model"`
	} `yaml:"llm"`
	Limits struct {
		Density float64 `yaml:"density"`
		Floor   int     `yaml:"floor"`
		HardCap int     `yaml:"hard_cap"`
	} `yaml:"limits"`
	Classify struct {
		Threshold float64 `yaml:"threshold"`
		BodyChars int     `yaml:"body_chars"`
	} `yaml:"classify"`
	Processing struct {
		DeadlineMinutes int `yaml:"deadline_minutes"`
	} `yaml:"processing"`
	Explain struct {
		Online bool   `yaml:"online"`
		Proxy  string `yaml:"proxy"`
	} `yaml:"explain"`
	AnnotateFirstParaOnly bool `yaml:"annotate_first_para_only"`
	Retention             struct {
		AutoArchiveWritten bool `yaml:"auto_archive_written"`
	} `yaml:"retention"`
	Libraries []ConfigLibrary `yaml:"libraries"`
	Backup map[string]any `yaml:"backup"`
}

var (
	rootDir string
	cfg     Config
)

func dbPath() string      { return filepath.Join(rootDir, "data", "kb.db") }
func inboxDir() string    { return filepath.Join(rootDir, "inbox") }
func archiveDir() string  { return filepath.Join(rootDir, "archive") }
func profilesDir() string { return filepath.Join(rootDir, "profiles") }

// loadConfig 读 config.yaml，并用环境变量覆盖密钥（与 Python 版一致）。
func loadConfig() (Config, error) {
	var c Config
	raw, err := os.ReadFile(filepath.Join(rootDir, "config.yaml"))
	if err != nil {
		return c, err
	}
	if err := yaml.Unmarshal(raw, &c); err != nil {
		return c, err
	}
	override := func(dst *string, key string) {
		if v := os.Getenv(key); v != "" {
			*dst = v
		}
	}
	override(&c.Yuque.Token, "YUQUE_TOKEN")
	override(&c.Yuque.Session, "YUQUE_SESSION")
	override(&c.Yuque.Ctoken, "YUQUE_CTOKEN")
	override(&c.LLM.APIKey, "LLM_API_KEY")
	override(&c.Explain.Proxy, "EXPLAIN_PROXY")
	if c.Yuque.BaseURL == "" {
		c.Yuque.BaseURL = "https://www.yuque.com/api/v2"
	}
	if c.LLM.BaseURL == "" {
		c.LLM.BaseURL = "https://api.openai.com/v1"
	}
	if c.Classify.Threshold == 0 {
		c.Classify.Threshold = 0.5
	}
	if c.Classify.BodyChars == 0 {
		c.Classify.BodyChars = 3000
	}
	if c.Limits.Density == 0 {
		c.Limits.Density = 2.5
	}
	if c.Limits.Floor == 0 {
		c.Limits.Floor = 3
	}
	if c.Limits.HardCap == 0 {
		c.Limits.HardCap = 14
	}
	if c.Yuque.PendingLibrary == "" {
		c.Yuque.PendingLibrary = "SimpRead待定"
	}
	return c, nil
}

// loadDotEnv 把 .env 读进环境变量（已存在的不覆盖）。
func loadDotEnv() {
	raw, err := os.ReadFile(filepath.Join(rootDir, ".env"))
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		i := strings.IndexByte(line, '=')
		if i <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:i])
		val := strings.TrimSpace(line[i+1:])
		if len(val) >= 2 && (val[0] == '"' || val[0] == '\'') && val[len(val)-1] == val[0] {
			val = val[1 : len(val)-1]
		}
		if os.Getenv(key) == "" {
			os.Setenv(key, val)
		}
	}
}
