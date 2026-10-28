package qq

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tencent-connect/botgo/dto"
	sdklog "github.com/tencent-connect/botgo/log"
	"qq-rwkv-bot/internal/rwkv"
)

// BotGo's default logger emits raw requests/tokens. Never allow SDK logs through.
// The application logs only safe operation names and numeric HTTP/API codes via slog.
type silentLogger struct{}

func (silentLogger) Debug(...interface{})          {}
func (silentLogger) Info(...interface{})           {}
func (silentLogger) Warn(...interface{})           {}
func (silentLogger) Error(...interface{})          {}
func (silentLogger) Debugf(string, ...interface{}) {}
func (silentLogger) Infof(string, ...interface{})  {}
func (silentLogger) Warnf(string, ...interface{})  {}
func (silentLogger) Errorf(string, ...interface{}) {}
func (silentLogger) Sync() error                   { return nil }
func init()                                        { sdklog.DefaultLogger = silentLogger{} }

type Client struct {
	AppID, Secret, BaseURL string
	HTTP                   *http.Client
	mu                     sync.Mutex
	token                  string
	expiry                 time.Time
}

func NewClient(id, secret string) *Client {
	return &Client{AppID: id, Secret: secret, BaseURL: "https://api.bot.qq.com", HTTP: rwkv.HTTPClient(10 * time.Second)}
}
func (c *Client) accessToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && time.Until(c.expiry) > 60*time.Second {
		return c.token, nil
	}
	body := rwkv.JSON(map[string]string{"appId": c.AppID, "clientSecret": c.Secret})
	req, e := http.NewRequestWithContext(ctx, "POST", c.BaseURL+"/app/getAppAccessToken", bytes.NewReader(body))
	if e != nil {
		return "", errors.New("qq token request failed")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, e := c.HTTP.Do(req)
	if e != nil {
		return "", errors.New("qq token transport failed")
	}
	defer resp.Body.Close()
	var d struct {
		Token   string          `json:"access_token"`
		Expires json.RawMessage `json:"expires_in"`
		Code    int             `json:"code"`
	}
	if resp.StatusCode != 200 || json.NewDecoder(io.LimitReader(resp.Body, 65536)).Decode(&d) != nil {
		return "", fmt.Errorf("qq token HTTP %d or invalid response", resp.StatusCode)
	}
	seconds, e := strconv.ParseInt(strings.Trim(string(d.Expires), `"`), 10, 64)
	if e != nil || seconds <= 0 || seconds > 86400 || d.Token == "" || d.Code != 0 {
		return "", fmt.Errorf("qq token rejected (code %d)", d.Code)
	}
	c.token = d.Token
	c.expiry = time.Now().Add(time.Duration(seconds) * time.Second)
	return c.token, nil
}
func (c *Client) invalidate(token string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token == token {
		c.token = ""
	}
}

// Reply uses SDK message DTOs and a small v2 transport for the current official host.
// Never retry ambiguous network failures: an accepted reply may already be visible.
func (c *Client) Reply(ctx context.Context, m Message, text string, seq uint32) error {
	path := "/v2/users/" + url.PathEscape(m.User) + "/messages"
	if m.Group != "" {
		path = "/v2/groups/" + url.PathEscape(m.Group) + "/messages"
	}
	payload := struct {
		dto.MessageToCreate
		MsgType int `json:"msg_type"`
	}{dto.MessageToCreate{Content: text, MsgID: m.ID, MsgSeq: seq}, 0}
	for attempt := 0; attempt < 2; attempt++ {
		token, e := c.accessToken(ctx)
		if e != nil {
			return e
		}
		req, e := http.NewRequestWithContext(ctx, "POST", c.BaseURL+path, bytes.NewReader(rwkv.JSON(payload)))
		if e != nil {
			return errors.New("qq reply request failed")
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "QQBot "+token)
		req.Header.Set("X-Union-Appid", c.AppID)
		resp, e := c.HTTP.Do(req)
		if e != nil {
			return errors.New("qq reply transport failed")
		}
		data, e := io.ReadAll(io.LimitReader(resp.Body, 65536))
		resp.Body.Close()
		if e != nil {
			return errors.New("qq reply read failed")
		}
		var result struct {
			Code    int    `json:"code"`
			ErrCode int    `json:"err_code"`
			ID      string `json:"id"`
		}
		if json.Unmarshal(data, &result) != nil {
			return errors.New("qq reply invalid response")
		}
		if resp.StatusCode == 401 || result.Code == 11244 || result.ErrCode == 11244 {
			c.invalidate(token)
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 || result.Code != 0 || result.ErrCode != 0 || result.ID == "" {
			return fmt.Errorf("qq reply rejected (HTTP %d code %d err_code %d)", resp.StatusCode, result.Code, result.ErrCode)
		}
		return nil
	}
	return errors.New("qq authentication failed")
}
