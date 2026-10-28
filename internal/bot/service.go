package bot

import (
	"context"
	"hash/fnv"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"qq-rwkv-bot/internal/qq"
	"qq-rwkv-bot/internal/rwkv"
	"qq-rwkv-bot/internal/session"
)

type Completer interface {
	Complete(context.Context, []rwkv.Message, func(string)) (string, error)
}
type Replier interface {
	Reply(context.Context, qq.Message, string, uint32) error
}
type Options struct {
	Workers, Queue, ReplyRunes int
	Timeout                    time.Duration
	Model                      string
}
type Service struct {
	ctx       context.Context
	cancel    context.CancelFunc
	lm        Completer
	qq        Replier
	store     *session.Store
	opts      Options
	log       *slog.Logger
	queues    []chan qq.Message
	wg        sync.WaitGroup
	mu        sync.Mutex
	closed    bool
	seen      map[string]time.Time
	lastSweep time.Time
	nextJob   atomic.Uint64
}

func New(lm Completer, replier Replier, store *session.Store, opts Options, logger *slog.Logger) *Service {
	ctx, cancel := context.WithCancel(context.Background())
	s := &Service{ctx: ctx, cancel: cancel, lm: lm, qq: replier, store: store, opts: opts, log: logger, seen: make(map[string]time.Time)}
	for i := 0; i < opts.Workers; i++ {
		q := make(chan qq.Message, opts.Queue)
		s.queues = append(s.queues, q)
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			for m := range q {
				if s.ctx.Err() != nil {
					return
				}
				s.process(m)
			}
		}()
	}
	return s
}

// Sharding keeps each session FIFO without unbounded goroutines or per-user queues.
// Admission + deduplication is atomic; a full queue gets a negative SDK ACK for redelivery.
func (s *Service) Submit(m qq.Message) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	now := time.Now()
	if now.Sub(s.lastSweep) > time.Minute {
		for k, t := range s.seen {
			if now.Sub(t) > time.Hour {
				delete(s.seen, k)
			}
		}
		s.lastSweep = now
	}
	key := m.DedupeKey()
	if t, ok := s.seen[key]; ok && now.Sub(t) <= time.Hour {
		return true
	}
	if len(s.seen) >= 100000 {
		return false
	}
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(m.Key()))
	q := s.queues[int(hash.Sum32())%len(s.queues)]
	select {
	case q <- m:
		s.seen[key] = now
		return true
	default:
		return false
	}
}
func (s *Service) Close(ctx context.Context) error {
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		for _, q := range s.queues {
			close(q)
		}
	}
	s.mu.Unlock()
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
		s.cancel()
		return nil
	case <-ctx.Done():
		s.cancel()
		<-done
		return ctx.Err()
	}
}
func (s *Service) process(m qq.Message) {
	log := s.log.With("job", s.nextJob.Add(1))
	started := time.Now()
	defer func() {
		if recover() != nil {
			log.Error("message processing panic recovered")
		}
	}()
	// Bound queue waiting as well as inference to preserve QQ's passive reply window.
	if time.Since(m.Received) > 3*time.Minute {
		log.Warn("expired queued message skipped")
		return
	}
	log.Info("message processing started", "message_age_ms", time.Since(m.Received).Milliseconds())
	ctx, cancel := context.WithDeadline(s.ctx, m.Received.Add(4*time.Minute))
	defer cancel()
	send := func(text string) bool {
		log.Info("QQ reply started")
		if err := s.qq.Reply(ctx, m, text, 1); err != nil {
			log.Error("QQ reply failed", "error", err, "elapsed_ms", time.Since(started).Milliseconds())
			return false
		}
		log.Info("QQ reply succeeded", "elapsed_ms", time.Since(started).Milliseconds())
		return true
	}
	input := rwkv.Normalize(m.Content)
	if input == "/reset" {
		s.store.Reset(m.Key())
		send("上下文已清理。")
		return
	}
	if input == "" {
		send("请发送文字消息；使用 /reset 清理上下文。")
		return
	}
	if rwkv.SliceUTF16(input, 8000, false) != input {
		send("消息过长，请控制在 8000 字以内。")
		return
	}
	history := append(s.store.Get(m.Key()), rwkv.Message{Role: "user", Content: input})
	history = rwkv.Context(history, s.opts.Model)
	infer, cancelInfer := context.WithTimeout(ctx, s.opts.Timeout)
	log.Info("RWKV inference started", "timeout_ms", s.opts.Timeout.Milliseconds())
	inferStarted := time.Now()
	firstText := true
	raw, err := s.lm.Complete(infer, history, func(string) {
		if firstText {
			firstText = false
			log.Info("RWKV first text received", "elapsed_ms", time.Since(inferStarted).Milliseconds())
		}
	})
	cancelInfer()
	if err != nil {
		log.Warn("RWKV inference failed", "error", err, "elapsed_ms", time.Since(inferStarted).Milliseconds())
		send("生成失败或超时，请稍后重试。")
		return
	}
	log.Info("RWKV inference completed", "elapsed_ms", time.Since(inferStarted).Milliseconds())
	answer := strings.TrimSpace(rwkv.Answer(raw))
	if answer == "" {
		send("本次没有生成有效回答，请重试或使用 /reset。")
		return
	}
	// One reply avoids consuming QQ's per-message reply quota. Keep full output in context.
	runes := []rune(answer)
	if len(runes) > s.opts.ReplyRunes {
		answer = string(runes[:s.opts.ReplyRunes]) + "\n（回答较长，已截断）"
	}
	if send(answer) {
		history = append(history, rwkv.Message{Role: "assistant", Content: raw})
		s.store.Set(m.Key(), rwkv.Context(history, s.opts.Model))
	}
}
