package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"qq-rwkv-bot/internal/bot"
	"qq-rwkv-bot/internal/config"
	"qq-rwkv-bot/internal/qq"
	"qq-rwkv-bot/internal/rwkv"
	"qq-rwkv-bot/internal/session"
	"syscall"
	"time"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("service stopped", "error", err)
		os.Exit(1)
	}
}
func run(logger *slog.Logger) error {
	if err := config.LoadDotEnv(".env"); err != nil {
		return err
	}
	c, err := config.Load()
	if err != nil {
		return err
	}
	lm := &rwkv.Client{URL: c.URL, Model: c.Model, AccessID: c.AccessID, AccessSecret: c.AccessSecret, Generation: c.Generation, HTTP: rwkv.HTTPClient(c.Timeout)}
	service := bot.New(lm, qq.NewClient(c.AppID, c.AppSecret), session.New(c.SessionLimit, c.SessionTTL), bot.Options{Workers: c.Workers, Queue: c.Queue, ReplyRunes: c.ReplyRunes, Timeout: c.Timeout, Model: c.Model}, logger)
	mux := http.NewServeMux()
	mux.Handle("/webhook/qq", &qq.Webhook{AppID: c.AppID, Secret: c.AppSecret, Submit: service.Submit, Logger: logger})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok\n"))
	})
	server := &http.Server{Addr: c.Listen, Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	signals, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	failed := make(chan error, 1)
	listener, listenErr := net.Listen("tcp", c.Listen)
	if listenErr != nil {
		_ = service.Close(context.Background())
		return listenErr
	}
	go func() { failed <- server.Serve(listener) }()
	logger.Info("service listening", "address", listener.Addr().String(), "model", c.Model, "webhook_path", "/webhook/qq", "mode", "webhook")
	select {
	case <-signals.Done():
	case err = <-failed:
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_ = server.Shutdown(shutdown)
	cancel()
	_ = server.Close()
	drain, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	if e := service.Close(drain); e != nil {
		logger.Warn("shutdown canceled remaining work")
	}
	return err
}
