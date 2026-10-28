// Package rwkv ports alic-li.com's raw completion protocol, not chat completions.
package rwkv

import (
	"bytes"
	"encoding/json"
	"strings"
	"unicode"
	"unicode/utf16"
)

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func Normalize(s string) string {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
	lines := strings.Split(s, "\n")
	out := lines[:0]
	for i, line := range lines {
		if i > 0 && i < len(lines)-1 && strings.TrimSpace(line) == "" {
			continue
		}
		out = append(out, line)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// SliceUTF16 follows JavaScript's string length budget without splitting a surrogate pair.
func SliceUTF16(s string, n int, tail bool) string {
	u := utf16.Encode([]rune(s))
	if len(u) <= n {
		return s
	}
	if tail {
		u = u[len(u)-n:]
		if len(u) > 0 && u[0] >= 0xdc00 && u[0] <= 0xdfff {
			u = u[1:]
		}
	} else {
		u = u[:n]
		if len(u) > 0 && u[len(u)-1] >= 0xd800 && u[len(u)-1] <= 0xdbff {
			u = u[:len(u)-1]
		}
	}
	return string(utf16.Decode(u))
}
func JSON(v any) []byte {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	_ = e.Encode(v)
	return bytes.TrimSuffix(b.Bytes(), []byte("\n"))
}
func Context(messages []Message, model string) []Message {
	var normalized []Message
	for _, m := range messages {
		m.Content = SliceUTF16(Normalize(m.Content), 8000, false)
		if m.Content == "" {
			continue
		}
		n := len(normalized)
		if n > 0 && normalized[n-1].Role == m.Role {
			normalized[n-1].Content = SliceUTF16(normalized[n-1].Content+"\n"+m.Content, 8000, true)
		} else {
			normalized = append(normalized, m)
		}
	}
	if len(normalized) > 30 {
		normalized = normalized[len(normalized)-30:]
	}
	selected := []Message{}
	for i := len(normalized) - 1; i >= 0; i-- {
		candidate := append([]Message{normalized[i]}, selected...)
		body := struct {
			Model    string    `json:"model"`
			Messages []Message `json:"messages"`
			Thinking bool      `json:"thinking"`
		}{model, candidate, false}
		if len(JSON(body)) > 32768 {
			break
		}
		selected = candidate
	}
	for len(selected) > 0 && selected[0].Role == "assistant" {
		selected = selected[1:]
	}
	return selected
}

// Prompt always selects the blog's fast-thinking continuation to skip reasoning.
func Prompt(messages []Message) string {
	parts := make([]string, 0, len(messages)+1)
	for _, m := range messages {
		s := Normalize(m.Content)
		if m.Role == "user" {
			parts = append(parts, "User: "+s)
			continue
		}
		if strings.HasPrefix(s, "<think>") {
			if !strings.Contains(s, "</think>") {
				s += "</think>"
			}
		} else {
			s = "<think></think>" + s
		}
		parts = append(parts, "Assistant: "+s)
	}
	prefix := "Assistant: <think></think"
	return strings.Join(append(parts, prefix), "\n\n")
}
func Answer(s string) string {
	if !strings.HasPrefix(s, "<think>") {
		return s
	}
	i := strings.Index(s, "</think>")
	if i < 0 {
		return ""
	}
	return strings.TrimLeftFunc(s[i+8:], unicode.IsSpace)
}
