package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tencent-connect/botgo/interaction/signature"
	"qq-rwkv-bot/internal/qq"
	"qq-rwkv-bot/internal/rwkv"
	"qq-rwkv-bot/internal/session"
)

type completionFunc func(context.Context, []rwkv.Message, func(string)) (string, error)

func (f completionFunc) Complete(c context.Context, m []rwkv.Message, on func(string)) (string, error) {
	return f(c, m, on)
}

type replyFunc func(context.Context, qq.Message, string, uint32) error

func (f replyFunc) Reply(c context.Context, m qq.Message, text string, seq uint32) error {
	return f(c, m, text, seq)
}
func options() Options {
	return Options{Workers: 1, Queue: 16, ReplyRunes: 1500, Timeout: time.Second, Model: "rwkv-1.5b"}
}
func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }
func closeService(t *testing.T, s *Service) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if e := s.Close(ctx); e != nil {
		t.Fatal(e)
	}
}
func TestEndToEndSignedWebhookToRWKVAndQQ(t *testing.T) {
	var mu sync.Mutex
	var prompts, answers []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Contents []string `json:"contents"`
		}
		if json.NewDecoder(r.Body).Decode(&b) != nil || len(b.Contents) != 1 {
			t.Error("payload")
			return
		}
		mu.Lock()
		prompts = append(prompts, b.Contents[0])
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"text\":\">answer\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer upstream.Close()
	qqAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/app/getAppAccessToken" {
			fmt.Fprint(w, `{"access_token":"fake","expires_in":7200}`)
			return
		}
		var b struct {
			Content string `json:"content"`
		}
		_ = json.NewDecoder(r.Body).Decode(&b)
		mu.Lock()
		answers = append(answers, b.Content)
		mu.Unlock()
		fmt.Fprint(w, `{"id":"sent"}`)
	}))
	defer qqAPI.Close()
	lm := &rwkv.Client{URL: upstream.URL, Model: "rwkv-1.5b", Generation: rwkv.Defaults(), HTTP: rwkv.HTTPClient(time.Second)}
	replier := qq.NewClient("app", "test-only")
	replier.BaseURL = qqAPI.URL
	service := New(lm, replier, session.New(10, time.Hour), options(), quiet())
	h := &qq.Webhook{AppID: "app", Secret: "test-only", Submit: service.Submit}
	dispatch := func(id, user, group, text string) {
		t.Helper()
		kind := "C2C_MESSAGE_CREATE"
		author := map[string]string{"user_openid": user}
		if group != "" {
			kind = "GROUP_AT_MESSAGE_CREATE"
			author = map[string]string{"member_openid": user}
		}
		body := rwkv.JSON(map[string]any{"op": 0, "t": kind, "d": map[string]any{"id": id, "content": text, "group_openid": group, "author": author}})
		r := httptest.NewRequest("POST", "/webhook/qq", strings.NewReader(string(body)))
		r.Header.Set(signature.HeaderTimestamp, strconv.FormatInt(time.Now().Unix(), 10))
		sig, _ := signature.Generate("test-only", r.Header, body)
		r.Header.Set(signature.HeaderSig, sig)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Body.String() != `{"op":12,"d":0}` {
			t.Fatal(w.Body.String())
		}
	}
	dispatch("1", "alice", "", "first")
	dispatch("1", "alice", "", "first")
	dispatch("2", "alice", "", "second")
	dispatch("3", "bob", "", "private")
	dispatch("4", "alice", "g", "group")
	dispatch("5", "alice", "", "/reset")
	dispatch("6", "alice", "", "fresh")
	closeService(t, service)
	mu.Lock()
	defer mu.Unlock()
	if len(prompts) != 5 || len(answers) != 6 {
		t.Fatalf("inferences=%d replies=%d", len(prompts), len(answers))
	}
	if prompts[0] != "User: first\n\nAssistant: <think></think" || !strings.Contains(prompts[1], "Assistant: <think></think>answer\n\nUser: second") {
		t.Fatal(prompts)
	}
	for _, i := range []int{2, 3, 4} {
		if strings.Contains(prompts[i], "first") || strings.Contains(prompts[i], "second") {
			t.Fatal("context leaked across user/group/reset")
		}
	}
	if answers[4] != "上下文已清理。" {
		t.Fatal(answers)
	}
}
func TestQueueDedupAndShutdown(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	lm := completionFunc(func(ctx context.Context, _ []rwkv.Message, _ func(string)) (string, error) {
		once.Do(func() { close(started) })
		select {
		case <-release:
			return "answer", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	})
	opts := options()
	opts.Queue = 1
	s := New(lm, replyFunc(func(context.Context, qq.Message, string, uint32) error { return nil }), session.New(10, time.Hour), opts, quiet())
	m := qq.Message{ID: "1", User: "u", Content: "hello", Received: time.Now()}
	if !s.Submit(m) {
		t.Fatal("admission")
	}
	<-started
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if !s.Submit(m) {
				t.Error("duplicate should ACK")
			}
		}()
	}
	wg.Wait()
	m.ID = "2"
	if !s.Submit(m) {
		t.Fatal("queue admission")
	}
	m.ID = "3"
	if s.Submit(m) {
		t.Fatal("queue overflow accepted")
	}
	if _, ok := s.seen[m.DedupeKey()]; ok {
		t.Fatal("rejected job marked seen")
	}
	close(release)
	closeService(t, s)
	if s.Submit(m) {
		t.Fatal("accepted after shutdown")
	}
}
func TestFailureDoesNotCommitAndShutdownCancels(t *testing.T) {
	store := session.New(10, time.Hour)
	s := New(completionFunc(func(context.Context, []rwkv.Message, func(string)) (string, error) { return "raw", nil }), replyFunc(func(context.Context, qq.Message, string, uint32) error { return errors.New("failed") }), store, options(), quiet())
	m := qq.Message{ID: "1", User: "u", Content: "hi", Received: time.Now()}
	s.Submit(m)
	closeService(t, s)
	if store.Get(m.Key()) != nil {
		t.Fatal("undelivered output committed")
	}
	started := make(chan struct{})
	s = New(completionFunc(func(c context.Context, _ []rwkv.Message, _ func(string)) (string, error) {
		close(started)
		<-c.Done()
		return "", c.Err()
	}), replyFunc(func(context.Context, qq.Message, string, uint32) error { return nil }), store, options(), quiet())
	s.Submit(m)
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if s.Close(ctx) == nil {
		t.Fatal("expected canceled drain")
	}
}
