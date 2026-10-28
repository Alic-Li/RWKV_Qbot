package rwkv

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
	"unicode/utf8"
)

type Generation struct {
	MaxTokens      int     `json:"max_tokens"`
	Temperature    float64 `json:"temperature"`
	TopK           int     `json:"top_k"`
	TopP           float64 `json:"top_p"`
	AlphaPresence  float64 `json:"alpha_presence"`
	AlphaFrequency float64 `json:"alpha_frequency"`
	AlphaDecay     float64 `json:"alpha_decay"`
	ChunkSize      int     `json:"chunk_size"`
}

func Defaults() Generation { return Generation{2048, 1, 50, .3, 1, .1, .99, 1} }

type Client struct {
	URL, Model, AccessID, AccessSecret string
	Generation                         Generation
	HTTP                               *http.Client
}

func HTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

var ErrUpstream = errors.New("rwkv upstream failure")

// Complete retains raw reasoning in history; onText is the extension point for streaming replies.
func (c *Client) Complete(ctx context.Context, messages []Message, onText func(string)) (string, error) {
	body := struct {
		Contents []string `json:"contents"`
		Stream   bool     `json:"stream"`
		Generation
	}{[]string{Prompt(Context(messages, c.Model))}, true, c.Generation}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL, bytes.NewReader(JSON(body)))
	if err != nil {
		return "", ErrUpstream
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("User-Agent", "Mozilla/5.0")
	if c.AccessID != "" {
		req.Header.Set("CF-Access-Client-Id", c.AccessID)
		req.Header.Set("CF-Access-Client-Secret", c.AccessSecret)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", ErrUpstream
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || !strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		return "", fmt.Errorf("%w (HTTP %d)", ErrUpstream, resp.StatusCode)
	}
	return ReadStream(resp.Body, onText)
}

// ReadStream mirrors sse.ts + continuationStream: slot zero, CRLF, DONE,
// first delimiter removal, and stop markers spanning arbitrary provider chunks.
func ReadStream(r io.Reader, onText func(string)) (string, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	var out strings.Builder
	pending := ""
	first := true
	emit := func(s string) error {
		if s == "" {
			return nil
		}
		if out.Len()+len(s) > 80000 {
			return ErrUpstream
		}
		out.WriteString(s)
		if onText != nil {
			onText(s)
		}
		return nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(line[5:])
		if data == "" {
			continue
		}
		if data == "[DONE]" {
			if err := emit(pending); err != nil {
				return "", err
			}
			return out.String(), nil
		}
		var event struct {
			Error   json.RawMessage `json:"error"`
			Choices []struct {
				Index *int `json:"index"`
				Delta struct {
					Content *string `json:"content"`
				} `json:"delta"`
				Text *string `json:"text"`
			} `json:"choices"`
		}
		if json.Unmarshal([]byte(data), &event) != nil || (len(event.Error) > 0 && string(event.Error) != "null" && string(event.Error) != "false" && string(event.Error) != `""`) {
			return "", ErrUpstream
		}
		chunk := ""
		for _, choice := range event.Choices {
			if choice.Index != nil && *choice.Index != 0 {
				continue
			}
			if choice.Delta.Content != nil {
				chunk = *choice.Delta.Content
			} else if choice.Text != nil {
				chunk = *choice.Text
			}
			break
		}
		if chunk == "" {
			continue
		}
		if first {
			first = false
			chunk = strings.TrimPrefix(chunk, ">")
		}
		pending += chunk
		hit := -1
		for _, stop := range []string{"\n\nUser:", "\n\nAssistant:", "<|endoftext|>"} {
			i := strings.Index(pending, stop)
			if i >= 0 && (hit < 0 || i < hit) {
				hit = i
			}
		}
		if hit >= 0 {
			if err := emit(pending[:hit]); err != nil {
				return "", err
			}
			return out.String(), nil
		}
		// Retain the longest possible ASCII stop prefix, without cutting UTF-8.
		if len(pending) > 13 {
			cut := len(pending) - 13
			for cut > 0 && !utf8.RuneStart(pending[cut]) {
				cut--
			}
			if err := emit(pending[:cut]); err != nil {
				return "", err
			}
			pending = pending[cut:]
		}
	}
	return "", ErrUpstream // EOF without DONE (or stop marker) is a truncated response.
}
