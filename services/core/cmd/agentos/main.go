package main

import (
	"agentos.local/core/internal/buildinfo"
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"agentos.local/core/internal/bootstrap"
	"agentos.local/core/internal/httpserver"
	"agentos.local/core/internal/webassets"
	"golang.org/x/sync/errgroup"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("core stopped", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		return buildinfo.Write(os.Stdout, "agentos")
	}
	if len(os.Args) == 2 && os.Args[1] == "--oauth" {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		return bootstrap.RunOAuth(ctx, os.Getenv("AGENTOS_CHANNEL_CONFIG"), os.Stdin, os.Stdout)
	}
	if len(os.Args) != 1 {
		return errors.New("agentos accepts --version, --oauth (JSON on stdin), or no arguments")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var channel *bootstrap.Channel
	if path, enabled := os.LookupEnv("AGENTOS_CHANNEL_CONFIG"); enabled {
		var err error
		channel, err = bootstrap.Open(ctx, path, logger)
		if err != nil {
			return err
		}
		defer channel.Close()
	}
	assets, err := webassets.Files()
	if err != nil {
		return err
	}
	address := "127.0.0.1:8080"
	if configured, exists := os.LookupEnv("AGENTOS_HTTP_ADDR"); exists {
		address = configured
		if address == "" {
			return errors.New("AGENTOS_HTTP_ADDR must not be empty")
		}
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.Handle("/", httpserver.New(assets))
	if channel != nil {
		mux.Handle("/api/ready", channel.Readiness)
		if channel.Operations != nil {
			mux.Handle("/api/ops/", channel.Operations)
		}
	} else {
		mux.HandleFunc("GET /api/ready", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(503)
			_, _ = w.Write([]byte(`{"ready":false}`))
		})
	}
	if channel != nil && channel.Handler != nil {
		mux.Handle("/api/channels/feishu/events", channel.Handler)
	}
	server := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	group, ctx := errgroup.WithContext(ctx)
	group.Go(func() error {
		err := server.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	})
	if channel != nil {
		group.Go(func() error { return channel.Run(ctx) })
	}
	logger.Info("core listening", "address", listener.Addr().String())
	group.Go(func() error {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	})
	return group.Wait()
}
