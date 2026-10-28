package rwkv

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestBlogTemplate(t *testing.T) {
	h := []Message{{"user", " first\r\n\r\nline\n \t\nend "}, {"assistant", "answer\n\nparagraph"}, {"user", "next"}}
	prefix := "User: first\nline\nend\n\nAssistant: <think></think>answer\nparagraph\n\nUser: next\n\nAssistant: "
	if got := Prompt(h); got != prefix+"<think></think" {
		t.Fatalf("template mismatch: %q", got)
	}
	if got := Prompt([]Message{{"assistant", "<think>partial"}}); !strings.Contains(got, "partial</think>\n\n") {
		t.Fatal(got)
	}
	if Answer("<think>reason</think>\nanswer") != "answer" || Answer("<think>partial") != "" {
		t.Fatal("reasoning split")
	}
}
func TestContextBudgets(t *testing.T) {
	merged := Context([]Message{{"assistant", "orphan"}, {"user", "failed request"}, {"user", "retry\n\nplease"}}, "rwkv-7b")
	if len(merged) != 1 || merged[0].Content != "failed request\nretry\nplease" {
		t.Fatal(merged)
	}
	var h []Message
	for i := 0; i < 30; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		h = append(h, Message{role, strings.Repeat("中", 8000)})
	}
	h = append(h, Message{"user", "latest"})
	got := Context(h, "rwkv-13b")
	if len(got) > 30 || got[0].Role != "user" || got[len(got)-1].Content != "latest" || len(JSON(map[string]any{"model": "rwkv-13b", "messages": got, "thinking": false})) > 32768 {
		t.Fatal("budget or ordering")
	}
	if !utf8.ValidString(SliceUTF16(strings.Repeat("😀", 4001), 8000, false)) {
		t.Fatal("invalid unicode")
	}
}

type byteReader struct{ io.Reader }

func (r byteReader) Read(b []byte) (int, error) { return r.Reader.Read(b[:1]) }
func events(chunks ...string) string {
	var s strings.Builder
	for _, c := range chunks {
		fmt.Fprintf(&s, "data: %s\r\n\r\n", JSON(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]string{"content": c}}}}))
	}
	return s.String()
}
func TestStreamingParity(t *testing.T) {
	for _, stop := range []string{"\n\nUser:", "\n\nAssistant:", "<|endoftext|>"} {
		chunks := []string{">", "你好"}
		for _, r := range stop {
			chunks = append(chunks, string(r))
		}
		chunks = append(chunks, "must not leak")
		got, err := ReadStream(byteReader{strings.NewReader(events(chunks...))}, nil)
		if err != nil || got != "你好" {
			t.Fatalf("%q %v", got, err)
		}
	}
	source := `data: {"choices":[{"index":1,"text":"wrong"}]}` + "\n\n" + events(">", "answer\n\n", "paragraph") + "data: [DONE]"
	var emitted strings.Builder
	got, err := ReadStream(byteReader{strings.NewReader(source)}, func(s string) { emitted.WriteString(s) })
	if err != nil || got != "answer\n\nparagraph" || emitted.String() != got {
		t.Fatalf("%q %v", got, err)
	}
	for _, s := range []string{events("partial"), `data: {"error":"private details"}`, "data: invalid\n", "data: " + strings.Repeat("x", 1024*1024)} {
		if _, err := ReadStream(strings.NewReader(s), nil); err == nil || strings.Contains(err.Error(), "private") {
			t.Fatal("missing sanitized failure")
		}
	}
}
func TestRequestAndRedirect(t *testing.T) {
	// Legacy environment settings cannot enable reasoning for outbound requests.
	t.Setenv("RWKV_THINKING", "true")
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "/leak", 302)
			return
		}
		if r.URL.Path != "/v1/batch/completions" {
			t.Error("unexpected path")
		}
		if r.Header.Get("CF-Access-Client-Id") != "test-id" || r.Header.Get("CF-Access-Client-Secret") != "test-secret" || r.Header.Get("Authorization") != "" || r.Header.Get("User-Agent") != "Mozilla/5.0" {
			t.Error("headers")
		}
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Error("json")
		}
		if body["model"] != nil || body["top_k"] != float64(50) || body["alpha_presence"] != 1.0 || body["alpha_frequency"] != .1 || body["contents"].([]any)[0] != "User: hello\n\nAssistant: <think></think" {
			t.Error("request parity")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, events(">hello")+"data: [DONE]\n")
	}))
	defer server.Close()
	c := &Client{URL: server.URL + "/v1/batch/completions", Model: "rwkv-1.5b", AccessID: "test-id", AccessSecret: "test-secret", Generation: Defaults(), HTTP: HTTPClient(time.Second)}
	got, err := c.Complete(context.Background(), []Message{{"user", "hello"}}, nil)
	if err != nil || got != "hello" {
		t.Fatal(got, err)
	}
	c.URL = server.URL + "/redirect"
	if _, err = c.Complete(context.Background(), nil, nil); err == nil {
		t.Fatal("redirect accepted")
	}
	if calls != 2 {
		t.Fatal("credentials could follow redirect")
	}
}
func TestCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	c := &Client{URL: server.URL, Generation: Defaults(), HTTP: HTTPClient(time.Second)}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := c.Complete(ctx, nil, nil); err == nil {
		t.Fatal("timeout accepted")
	}
}
