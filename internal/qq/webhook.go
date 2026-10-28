package qq

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/tencent-connect/botgo/dto"
	"github.com/tencent-connect/botgo/interaction/signature"
	"github.com/tencent-connect/botgo/interaction/webhook"
)

type Message struct {
	ID, User, Group, Content string
	Received                 time.Time
}

func (m Message) Key() string {
	if m.Group != "" {
		return "group:" + m.Group + ":" + m.User
	}
	return "c2c:" + m.User
}
func (m Message) DedupeKey() string { return m.Key() + ":" + m.ID }

type Webhook struct {
	AppID, Secret string
	Submit        func(Message) bool
	Logger        *slog.Logger
}

func (h *Webhook) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	reject := func(status int, reason string) {
		if h.Logger != nil {
			h.Logger.Warn("QQ webhook rejected", "status", status, "reason", reason)
		}
		w.WriteHeader(status)
	}
	if r.Method != http.MethodPost {
		reject(405, "method_not_post")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
	if err != nil {
		reject(413, "body_read_failed_or_too_large")
		return
	}
	if id := r.Header.Get("X-Bot-Appid"); id != "" && id != h.AppID {
		reject(401, "app_id_mismatch")
		return
	}
	ts, err := strconv.ParseInt(r.Header.Get(signature.HeaderTimestamp), 10, 64)
	if err != nil || time.Since(time.Unix(ts, 0)) > 5*time.Minute || time.Until(time.Unix(ts, 0)) > time.Minute {
		reject(401, "timestamp_missing_invalid_or_expired")
		return
	}
	valid, err := signature.Verify(h.Secret, r.Header, body)
	if err != nil || !valid {
		reject(401, "signature_invalid")
		return
	}
	var p struct {
		Op   *int            `json:"op"`
		Type string          `json:"t"`
		Data json.RawMessage `json:"d"`
	}
	if json.Unmarshal(body, &p) != nil || p.Op == nil {
		reject(400, "invalid_payload")
		return
	}
	if *p.Op == 13 {
		var d dto.WHValidationReq
		if json.Unmarshal(p.Data, &d) != nil || d.PlainToken == "" || d.EventTs == "" {
			reject(400, "invalid_payload")
			return
		}
		_, _ = w.Write(webhook.GenValidationACK(&d, r.Header, h.Secret))
		if h.Logger != nil {
			h.Logger.Info("QQ webhook validation succeeded")
		}
		return
	}
	if *p.Op != 0 {
		reject(400, "invalid_payload")
		return
	}
	if p.Type != "C2C_MESSAGE_CREATE" && p.Type != "GROUP_AT_MESSAGE_CREATE" {
		_, _ = io.WriteString(w, webhook.GenDispatchACK(true))
		return
	}
	var d struct {
		ID        string `json:"id"`
		Content   string `json:"content"`
		Group     string `json:"group_openid"`
		Timestamp string `json:"timestamp"`
		Author    struct {
			User   string `json:"user_openid"`
			Member string `json:"member_openid"`
			Bot    bool   `json:"bot"`
		} `json:"author"`
	}
	if json.Unmarshal(p.Data, &d) != nil {
		reject(400, "invalid_payload")
		return
	}
	m := Message{ID: d.ID, User: d.Author.User, Content: strings.TrimSpace(d.Content), Received: time.Now()}
	if p.Type == "GROUP_AT_MESSAGE_CREATE" {
		m.Group = d.Group
		m.User = d.Author.Member
		if m.Group == "" {
			reject(400, "invalid_payload")
			return
		}
	}
	if m.ID == "" || m.User == "" {
		reject(400, "invalid_payload")
		return
	}
	if sent, e := time.Parse(time.RFC3339, d.Timestamp); e == nil {
		m.Received = sent
	}
	// Expired deliveries cannot be replied to reliably. Use the conservative five-minute window.
	if d.Author.Bot || time.Since(m.Received) > 4*time.Minute {
		_, _ = io.WriteString(w, webhook.GenDispatchACK(true))
		return
	}
	accepted := h.Submit(m)
	if h.Logger != nil {
		h.Logger.Info("QQ webhook message received", "event_type", p.Type, "accepted", accepted)
	}
	_, _ = io.WriteString(w, webhook.GenDispatchACK(accepted))
}
