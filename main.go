package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/jeethsuresh/newtond/internal/pilot"
	"github.com/jeethsuresh/newtond/server"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "tui" {
		server.StartServer()
		return
	}
	if err := run(); err != nil {
		slog.Error("newtonD exited", "error", err)
		os.Exit(1)
	}
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func run() error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	keys, err := pilot.DecodeKeys(os.Getenv("NEWTOND_UDP_KEYS"))
	if err != nil {
		return err
	}
	auth, err := pilot.NewAuth(os.Getenv("NEWTOND_PUBLIC_ORIGIN"), env("NEWTOND_ISSUER", "https://gravitas.cool"),
		os.Getenv("NEWTOND_SESSION_KEY"))
	if err != nil {
		return err
	}
	store, err := pilot.Open(env("NEWTOND_DB", "./newtond.db"))
	if err != nil {
		return fmt.Errorf("open SQLite: %w", err)
	}
	defer store.Close()
	if err := store.Prune(ctx, time.Now()); err != nil {
		return err
	}
	udp, err := pilot.ListenUDP(env("NEWTOND_UDP", ":5514"))
	if err != nil {
		return fmt.Errorf("open UDP listener: %w", err)
	}
	defer udp.Close()
	web := &http.Server{Addr: env("NEWTOND_HTTP", ":8080"), Handler: pilot.Handler(store, auth),
		ReadHeaderTimeout: 5 * time.Second, WriteTimeout: 12 * time.Second,
		IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16384}
	webErrors := make(chan error, 1)
	var workers sync.WaitGroup
	workers.Go(func() { pilot.Receive(ctx, udp, store, keys) })
	go func() { webErrors <- web.ListenAndServe() }()
	slog.Info("newtonD listeners ready", "http", web.Addr, "udp", udp.LocalAddr())
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case err := <-webErrors:
			cancel()
			workers.Wait()
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			return err
		case <-ctx.Done():
			shutdown, stop := context.WithTimeout(context.Background(), 5*time.Second)
			defer stop()
			err := web.Shutdown(shutdown)
			workers.Wait()
			return err
		case now := <-ticker.C:
			if err := store.Prune(ctx, now); err != nil {
				slog.Error("retention pruning failed", "error", err)
			}
		}
	}
}
