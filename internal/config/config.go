package config

import (
	"bufio"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"qq-rwkv-bot/internal/rwkv"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	AppID, AppSecret, AccessID, AccessSecret, Listen, URL, Model string
	Workers, Queue, SessionLimit, ReplyRunes                     int
	Timeout, SessionTTL                                          time.Duration
	Generation                                                   rwkv.Generation
}

// LoadDotEnv handles KEY=value and single/double quoted values, without expansion.
// Exported environment variables take precedence; values never appear in errors.
func LoadDotEnv(path string) error {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return errors.New("cannot open env file")
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 4096), 65536)
	line := 0
	for s.Scan() {
		line++
		v := strings.TrimSpace(s.Text())
		if v == "" || strings.HasPrefix(v, "#") {
			continue
		}
		v = strings.TrimPrefix(v, "export ")
		k, v, ok := strings.Cut(v, "=")
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if !ok || k == "" {
			return fmt.Errorf("invalid env line %d", line)
		}
		for _, c := range k {
			if !(c == '_' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9') {
				return fmt.Errorf("invalid env line %d", line)
			}
		}
		if strings.HasPrefix(v, `"`) || strings.HasPrefix(v, "'") {
			if len(v) < 2 || v[len(v)-1] != v[0] {
				return fmt.Errorf("invalid env quoting line %d", line)
			}
			v = v[1 : len(v)-1]
		}
		if _, set := os.LookupEnv(k); !set {
			if os.Setenv(k, v) != nil {
				return fmt.Errorf("invalid env line %d", line)
			}
		}
	}
	if s.Err() != nil {
		return errors.New("cannot read env file")
	}
	return nil
}
func Load() (Config, error) {
	c := Config{AppID: os.Getenv("APP_ID"), AppSecret: os.Getenv("APP_SECRET"), AccessID: os.Getenv("CF_ACCESS_CLIENT_ID"), AccessSecret: os.Getenv("CF_ACCESS_CLIENT_SECRET"), Listen: "127.0.0.1:8080", Model: "rwkv-1.5b", Workers: 4, Queue: 16, SessionLimit: 1000, ReplyRunes: 1500, Timeout: 90 * time.Second, SessionTTL: 24 * time.Hour, Generation: rwkv.Defaults()}
	if c.AppID == "" || c.AppSecret == "" {
		return c, errors.New("APP_ID and APP_SECRET are required")
	}
	if (c.AccessID == "") != (c.AccessSecret == "") {
		return c, errors.New("both Cloudflare Access credentials must be supplied together")
	}
	for k, p := range map[string]*string{"LISTEN_ADDR": &c.Listen, "RWKV_MODEL": &c.Model, "RWKV_API_URL": &c.URL} {
		if s := os.Getenv(k); s != "" {
			*p = s
		}
	}
	origins := map[string]string{"rwkv-1.5b": "https://api-1b5.rwkvos.com", "rwkv-3b": "https://api-3b.rwkvos.com", "rwkv-7b": "https://api-7b.rwkvos.com", "rwkv-13b": "https://api-13b.rwkvos.com"}
	if c.URL == "" {
		if origins[c.Model] == "" {
			return c, errors.New("custom RWKV_MODEL requires RWKV_API_URL")
		}
		c.URL = origins[c.Model] + "/v1/batch/completions"
	}
	u, e := url.Parse(c.URL)
	if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" || u.Hostname() == "::1"))) {
		return c, errors.New("RWKV_API_URL must be HTTPS (or loopback HTTP), without credentials/query/fragment")
	}
	for k, p := range map[string]*int{"WORKERS": &c.Workers, "QUEUE_PER_WORKER": &c.Queue, "SESSION_LIMIT": &c.SessionLimit, "QQ_REPLY_RUNES": &c.ReplyRunes, "RWKV_MAX_TOKENS": &c.Generation.MaxTokens, "RWKV_TOP_K": &c.Generation.TopK, "RWKV_CHUNK_SIZE": &c.Generation.ChunkSize} {
		if s := os.Getenv(k); s != "" {
			v, e := strconv.Atoi(s)
			if e != nil || v < 1 || v > 100000 {
				return c, fmt.Errorf("invalid %s", k)
			}
			*p = v
		}
	}
	for k, p := range map[string]*float64{"RWKV_TEMPERATURE": &c.Generation.Temperature, "RWKV_TOP_P": &c.Generation.TopP, "RWKV_ALPHA_PRESENCE": &c.Generation.AlphaPresence, "RWKV_ALPHA_FREQUENCY": &c.Generation.AlphaFrequency, "RWKV_ALPHA_DECAY": &c.Generation.AlphaDecay} {
		if s := os.Getenv(k); s != "" {
			v, e := strconv.ParseFloat(s, 64)
			if e != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
				return c, fmt.Errorf("invalid %s", k)
			}
			*p = v
		}
	}
	for k, p := range map[string]*time.Duration{"RWKV_TIMEOUT": &c.Timeout, "SESSION_TTL": &c.SessionTTL} {
		if s := os.Getenv(k); s != "" {
			v, e := time.ParseDuration(s)
			if e != nil || v <= 0 {
				return c, fmt.Errorf("invalid %s", k)
			}
			*p = v
		}
	}
	if c.Workers > 64 || c.Queue > 256 || c.SessionLimit > 10000 || c.ReplyRunes > 2000 || c.Timeout > 180*time.Second || c.Generation.TopP > 1 || c.Generation.AlphaDecay > 1 {
		return c, errors.New("configuration exceeds supported limits")
	}
	return c, nil
}
