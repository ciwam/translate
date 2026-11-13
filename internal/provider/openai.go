package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type endpointInfo struct {
	Endpoint    string
	Model       string
	DisplayName string
	// PromptStyle selects how the pipeline phrases requests. "mt" models
	// are translation-specialized (Hunyuan-MT): per their docs they take
	// no system prompt — instructions belong in the user message, in the
	// official template "把下面的文本翻译成<lang>，不要额外解释。".
	// "" is the general instruct style (system+user).
	PromptStyle string
}

// Keep in sync with the Provider enum (Zhipu=0, SiliconFlow=1, Hunyuan=2).
var endpoints = []endpointInfo{
	{
		Endpoint:    "https://open.bigmodel.cn/api/paas/v4/chat/completions",
		Model:       "glm-4-flash",
		DisplayName: "Zhipu · GLM-4-Flash",
	},
	{
		Endpoint:    "https://api.siliconflow.cn/v1/chat/completions",
		Model:       "Qwen/Qwen2.5-7B-Instruct",
		DisplayName: "SiliconFlow · Qwen2.5-7B-Instruct",
	},
	{
		Endpoint:    "https://api.siliconflow.cn/v1/chat/completions",
		Model:       "tencent/Hunyuan-MT-7B",
		DisplayName: "SiliconFlow · Hunyuan-MT-7B",
		PromptStyle: "mt",
	},
}

// PromptStyle returns how a provider's requests should be phrased:
// "" (general instruct) or "mt" (translation-specialized, user-message
// template).
func PromptStyle(p Provider) string {
	idx := int(p)
	if idx < 0 || idx >= len(endpoints) {
		idx = 0
	}
	return endpoints[idx].PromptStyle
}

// MTTargetName maps a pipeline Target to the language name Hunyuan-MT's
// official template expects.
func MTTargetName(t Target) string {
	switch t {
	case English:
		return "英文"
	case Chinese:
		return "简体中文"
	case Japanese:
		return "日文"
	case Korean:
		return "韩文"
	case French:
		return "法文"
	case German:
		return "德文"
	case Spanish:
		return "西班牙文"
	}
	return "简体中文"
}

// Adaptive total-operation timeout: base allowance plus per-KB headroom
// for generation. The streaming stall guard is the per-read deadline in
// streamSSE; this total is only a slow-but-alive backstop.
const (
	timeoutBase    = 60 * time.Second
	timeoutPerKB   = 20 * time.Second
	timeoutMax     = 900 * time.Second
	readStallLimit = 90 * time.Second // no bytes for this long aborts the stream
)

// Throttle/limit faults are transient (retried by the pipeline); auth and
// other provider faults are not. Matched against status and message since
// wording differs per provider.
func isThrottle(status int, msg string) bool {
	if status == http.StatusTooManyRequests {
		return true
	}
	lower := strings.ToLower(msg)
	for _, kw := range []string{"429", "限流", "频繁", "rate", "throttl"} {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	Temperature float64       `json:"temperature"`
	MaxTokens   int           `json:"max_tokens"`
	Stream      bool          `json:"stream"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatStreamResponse struct {
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
	Choices []struct {
		Delta *struct {
			Content *string `json:"content"`
		} `json:"delta"`
		Message *struct {
			Content *string `json:"content"`
		} `json:"message"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
}

// trim mirrors the C++ trim (space, tab, CR, LF).
func trim(s string) string {
	return strings.Trim(s, " \t\r\n")
}

type openaiTranslator struct {
	idx int
}

// New returns the adapter for a provider value.
func New(p Provider) Translator {
	idx := int(p)
	if idx < 0 || idx >= len(endpoints) {
		idx = 0
	}
	return &openaiTranslator{idx: idx}
}

func (t *openaiTranslator) Translate(ctx context.Context, req Request) (<-chan Event, context.CancelFunc) {
	ctx, cancel := context.WithCancel(ctx)
	events := make(chan Event, 16)
	go func() {
		defer close(events)
		err := t.stream(ctx, req, events)
		if err != nil {
			select {
			case events <- Event{Kind: EventError, Error: err.Error()}:
			case <-ctx.Done():
			}
		}
	}()
	return events, cancel
}

func (t *openaiTranslator) stream(ctx context.Context, req Request, events chan<- Event) error {
	ep := endpoints[t.idx]
	payload := chatRequest{
		Model: ep.Model,
		Messages: []chatMessage{
			{Role: "system", Content: req.SystemPrompt},
			{Role: "user", Content: req.UserPrompt},
		},
		Temperature: 0.1,
		MaxTokens:   4095,
		Stream:      true,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	srcKB := int64(len(req.UserPrompt)/1024) + 1
	totalTimeout := timeoutBase + time.Duration(srcKB)*timeoutPerKB
	if totalTimeout > timeoutMax {
		totalTimeout = timeoutMax
	}
	ctx, cancel := context.WithTimeout(ctx, totalTimeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, ep.Endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+req.APIKey)
	httpReq.Header.Set("Accept", "text/event-stream")

	// Own client: no global cookie jar, per-request transport tuning.
	client := &http.Client{}
	resp, err := client.Do(httpReq)
	if err != nil {
		if ctx.Err() != nil {
			return errors.New("API 请求超时")
		}
		return fmt.Errorf("网络错误：%v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		msg := strings.TrimSpace(string(snippet))
		if isThrottle(resp.StatusCode, msg) {
			return fmt.Errorf("API 限流：HTTP %d %s", resp.StatusCode, msg)
		}
		return fmt.Errorf("API 错误：HTTP %d %s", resp.StatusCode, msg)
	}

	result, streamErr, status, err := t.consumeSSE(ctx, resp, events)
	if err != nil {
		if ctx.Err() != nil {
			return errors.New("API 请求超时")
		}
		return err
	}
	if streamErr != "" {
		if isThrottle(status, streamErr) {
			return fmt.Errorf("API 限流：%s", streamErr)
		}
		return fmt.Errorf("API 错误：%s", streamErr)
	}
	if trim(result) == "" {
		return fmt.Errorf("响应异常：未收到内容（HTTP %d）", status)
	}
	select {
	case events <- Event{Kind: EventDone, Text: trim(result)}:
	case <-ctx.Done():
		return ctx.Err()
	}
	return nil
}

// consumeSSE decodes the event stream, forwarding running-result deltas.
// Returns the accumulated result, an in-stream error message (error object
// or finish_reason=length), and the HTTP status.
func (t *openaiTranslator) consumeSSE(ctx context.Context, resp *http.Response, events chan<- Event) (result, streamErr string, status int, err error) {
	status = resp.StatusCode

	// Stall guard: abort when no bytes arrive for readStallLimit.
	src := newStallReader(resp.Body, readStallLimit)
	scanner := bufio.NewScanner(src)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var forwarded int
	for scanner.Scan() {
		if ctx.Err() != nil {
			return result, "", status, ctx.Err()
		}
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(line[len("data:"):])
		if data == "" {
			continue
		}
		if data == "[DONE]" {
			break
		}
		var chunk chatStreamResponse
		if jsonErr := json.Unmarshal([]byte(data), &chunk); jsonErr != nil {
			continue // keep-alive / stray payload
		}
		if chunk.Error != nil {
			return result, chunk.Error.Message, status, nil
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		choice := chunk.Choices[0]
		appended := false
		if choice.Delta != nil && choice.Delta.Content != nil {
			result += *choice.Delta.Content
			appended = true
		} else if choice.Message != nil && choice.Message.Content != nil {
			// Provider ignored "stream": true — whole completion at once.
			result += *choice.Message.Content
			appended = true
		}
		if appended {
			vp := completePrefixLen(result)
			if vp > forwarded {
				forwarded = vp
				select {
				case events <- Event{Kind: EventDelta, Text: result[:vp]}:
				case <-ctx.Done():
					return result, "", status, ctx.Err()
				}
			}
		}
		if choice.FinishReason != nil {
			if *choice.FinishReason == "length" {
				// A truncated reply must fail loudly: silently keeping the
				// partial text drops lines and breaks alignment.
				return result, "输出被截断（finish_reason=length）", status, nil
			}
			break // final chunk; "[DONE]" may not follow
		}
	}
	if scanErr := scanner.Err(); scanErr != nil {
		return result, "", status, scanErr
	}
	return result, "", status, nil
}

// completePrefixLen returns the length of the longest prefix of s that ends
// on a UTF-8 code-point boundary, so deltas never carry a partial character.
func completePrefixLen(s string) int {
	n := len(s)
	if n == 0 {
		return 0
	}
	i := n - 1
	for i >= 0 && i >= n-3 && s[i]&0xC0 == 0x80 {
		i--
	}
	if i < 0 {
		return 0
	}
	c := s[i]
	var l int
	switch {
	case c&0x80 == 0:
		l = 1
	case c&0xE0 == 0xC0:
		l = 2
	case c&0xF0 == 0xE0:
		l = 3
	case c&0xF8 == 0xF0:
		l = 4
	default:
		return i // invalid lead byte: keep only what precedes it
	}
	if i+l <= n {
		return n
	}
	return i
}

// stallReader aborts a read when no data arrives within limit.
type stallReader struct {
	r     io.Reader
	limit time.Duration
}

func newStallReader(r io.Reader, limit time.Duration) *stallReader {
	return &stallReader{r: r, limit: limit}
}

func (s *stallReader) Read(p []byte) (int, error) {
	type result struct {
		n   int
		err error
	}
	ch := make(chan result, 1)
	go func() {
		n, err := s.r.Read(p)
		ch <- result{n, err}
	}()
	timer := time.NewTimer(s.limit)
	defer timer.Stop()
	select {
	case res := <-ch:
		return res.n, res.err
	case <-timer.C:
		return 0, errors.New("API 响应停滞（90 秒无数据），已中断")
	}
}
