package qq

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tencent-connect/botgo/interaction/signature"
)

const testSecret = "unit-test-only-secret"

func signed(body string, age time.Duration) *http.Request {
	r := httptest.NewRequest("POST", "/webhook/qq", strings.NewReader(body))
	r.Header.Set(signature.HeaderTimestamp, strconv.FormatInt(time.Now().Add(age).Unix(), 10))
	sig, _ := signature.Generate(testSecret, r.Header, []byte(body))
	r.Header.Set(signature.HeaderSig, sig)
	return r
}
func TestWebhook(t *testing.T) {
	calls := 0
	accept := true
	h := Webhook{AppID: "test", Secret: testSecret, Submit: func(m Message) bool {
		calls++
		if m.User != "u" || m.Group != "g" || m.Content != "hello" {
			t.Error(m)
		}
		return accept
	}}
	body := `{"op":0,"t":"GROUP_AT_MESSAGE_CREATE","d":{"id":"m","content":" hello ","group_openid":"g","author":{"member_openid":"u"}}}`
	for _, ok := range []bool{true, false} {
		accept = ok
		w := httptest.NewRecorder()
		h.ServeHTTP(w, signed(body, 0))
		want := `{"op":12,"d":0}`
		if !ok {
			want = `{"op":12,"d":1}`
		}
		if w.Body.String() != want {
			t.Fatal(w.Body.String())
		}
	}
	for _, r := range []*http.Request{signed(body, -6*time.Minute), httptest.NewRequest("POST", "/", strings.NewReader(body)), signed(body, time.Hour)} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 401 {
			t.Fatal(w.Code)
		}
	}
	r := signed(body, 0)
	r.Header.Set("X-Bot-Appid", "other")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("appid")
	}
	r = signed(body, 0)
	r.Body = http.NoBody
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("tampering")
	}
	r = signed(strings.Repeat("x", 65537), 0)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 413 {
		t.Fatal("body limit")
	}
	if calls != 2 {
		t.Fatal("invalid event submitted")
	}
	challenge := `{"op":13,"d":{"plain_token":"challenge","event_ts":"12345"}}`
	w = httptest.NewRecorder()
	h.ServeHTTP(w, signed(challenge, 0))
	var result struct {
		Token     string `json:"plain_token"`
		Signature string `json:"signature"`
	}
	if json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Token != "challenge" {
		t.Fatal(w.Body.String())
	}
	seed := testSecret
	for len(seed) < 32 {
		seed += seed
	}
	key := ed25519.NewKeyFromSeed([]byte(seed[:32]))
	sig, _ := hex.DecodeString(result.Signature)
	if !ed25519.Verify(key.Public().(ed25519.PublicKey), []byte("12345challenge"), sig) {
		t.Fatal("challenge signature")
	}
}
func TestTokenCachingRefreshAndReply(t *testing.T) {
	var tokenCalls atomic.Int32
	var replyCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/app/getAppAccessToken" {
			n := tokenCalls.Add(1)
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["appId"] != "app" || body["clientSecret"] != "test-secret" {
				t.Error("token request")
			}
			if n == 1 {
				fmt.Fprint(w, `{"access_token":"token1","expires_in":"7200"}`)
			} else {
				fmt.Fprint(w, `{"access_token":"token2","expires_in":7200}`)
			}
			return
		}
		replyCalls.Add(1)
		if r.URL.Path != "/v2/users/u/messages" && r.URL.Path != "/v2/groups/g/messages" {
			t.Error(r.URL.Path)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["msg_id"] != "m" || body["msg_seq"] != float64(1) || body["msg_type"] != float64(0) {
			t.Error(body)
		}
		if r.Header.Get("Authorization") == "QQBot token1" {
			w.WriteHeader(401)
			fmt.Fprint(w, `{"code":11244}`)
			return
		}
		fmt.Fprint(w, `{"id":"reply"}`)
	}))
	defer server.Close()
	c := NewClient("app", "test-secret")
	c.BaseURL = server.URL
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.accessToken(context.Background()); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if tokenCalls.Load() != 1 {
		t.Fatal("token stampede")
	}
	for _, m := range []Message{{ID: "m", User: "u"}, {ID: "m", User: "u", Group: "g"}} {
		if err := c.Reply(context.Background(), m, "hello", 1); err != nil {
			t.Fatal(err)
		}
	}
	if tokenCalls.Load() != 2 || replyCalls.Load() != 3 {
		t.Fatal("refresh retry")
	}
	c.mu.Lock()
	c.expiry = time.Now()
	c.mu.Unlock()
	if _, err := c.accessToken(context.Background()); err != nil || tokenCalls.Load() != 3 {
		t.Fatal("expiry refresh")
	}
}
func TestTokenErrorSanitized(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"code":100016,"message":"sensitive-provider-data"}`)
	}))
	defer server.Close()
	c := NewClient("app", "test-secret")
	c.BaseURL = server.URL
	if _, e := c.accessToken(context.Background()); e == nil || strings.Contains(e.Error(), "sensitive") {
		t.Fatal(e)
	}
}
