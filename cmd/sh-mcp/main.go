package main

import (
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/yh2237/sh-mcp/internal/config"
	"github.com/yh2237/sh-mcp/internal/server"
)

var version = "dev"

func main() {
	if err := run(); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintln(os.Stderr, "sh-mcp:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Parse(os.Args[1:], os.Getenv, os.Stderr)
	if err != nil {
		return err
	}
	if cfg.ShowVersion {
		fmt.Println("sh-mcp", version)
		return nil
	}
	if cfg.GenToken {
		// 26 base32 characters, 128 bits of entropy.
		fmt.Println(rand.Text())
		return nil
	}

	// stdout carries the protocol in stdio mode, so logs always go to stderr.
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	srv, err := server.New(cfg, version, log)
	if err != nil {
		return err
	}
	defer srv.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	go func() {
		for range hup {
			if err := srv.Audit().Reopen(); err != nil {
				log.Error("reopen audit log", "err", err)
			}
		}
	}()

	err = srv.Run(ctx)
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}
