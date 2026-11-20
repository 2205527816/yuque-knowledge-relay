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

// LLMError 对应 Python 的 LLMError。
type LLMError struct{ msg string }

func (e *LLMError) Error() string { return e.msg }

// LLM 是 OpenAI 兼容的 chat/completions 客户端。
type LLM struct {
	URL    string
	Model  string
	APIKey string
	HTTP   *http.Client
}

func newLLM(baseURL, apiKey, model string) *LLM {
	return &LLM{
		URL:    strings.TrimRight(baseURL, "/") + "/chat/completions",
		Model:  model,
		APIKey: apiKey,
		HTTP:   &http.Client{Timeout: 120 * time.Second},
	}
}

func (l *LLM) Chat(messages []map[string]string, model string, temperature float64) (string, error) {
	if model == "" {
		model = l.Model
	}
	payload, _ := json.Marshal(map[string]any{
		"model":       model,
		"messages":    messages,
		"temperature": temperature,
	})
	req, err := http.NewRequest("POST", l.URL, bytes.NewReader(payload))
	if err != nil {
		return "", &LLMError{err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	if l.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+l.APIKey)
	}
	resp, err := l.HTTP.Do(req)
	if err != nil {
		return "", &LLMError{err.Error()}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		preview := string(body)
		if len(preview) > 300 {
			preview = preview[:300]
		}
		return "", &LLMError{fmt.Sprintf("LLM %d: %s", resp.StatusCode, preview)}
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &out); err != nil || len(out.Choices) == 0 {
		return "", &LLMError{"LLM 返回格式异常"}
	}
	return out.Choices[0].Message.Content, nil
}

func (l *LLM) ChatJSON(messages []map[string]string, model string) (map[string]any, error) {
	raw, err := l.Chat(messages, model, 0)
	if err != nil {
		return nil, err
	}
	body, ok := extractJSON(raw)
	if !ok {
		return nil, &LLMError{"输出中没有 JSON 对象"}
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		return nil, &LLMError{err.Error()}
	}
	return out, nil
}

// llmFromConfig 对应 from_config(cfg, key)。
func llmFromConfig(modelKey string) (*LLM, error) {
	base := cfg.LLM.BaseURL
	model := cfg.LLM.ClassifyModel
	if modelKey == "explain_model" {
		model = cfg.LLM.ExplainModel
	}
	if base == "" || model == "" {
		return nil, &LLMError{"未配置 llm.base_url 或 llm." + modelKey}
	}
	return newLLM(base, cfg.LLM.APIKey, model), nil
}
