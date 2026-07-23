// Command modelgate is a single-binary gateway for OpenAI-compatible
// /v1/chat/completions requests: routes to a configured list of
// providers, falls back to the next one on a retryable failure, and logs
// a metadata-only (never prompt/response content) JSONL audit trail with
// per-request token/cost accounting.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/bharat3645/modelgate/gateway"
)

const version = "0.2.0"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "modelgate:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("modelgate", flag.ContinueOnError)
	configPath := fs.String("config", "", "path to JSON config file (required)")
	showVersion := fs.Bool("version", false, "print version and exit")
	checkOnly := fs.Bool("check", false, "validate configuration and exit")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	if *showVersion {
		fmt.Println("modelgate " + version)
		return nil
	}

	if *configPath == "" {
		return errors.New("--config is required")
	}
	cfg, err := gateway.LoadConfig(*configPath)
	if err != nil {
		return err
	}

	if *checkOnly {
		fmt.Printf("config ok: listen=%s providers=%d\n", cfg.Listen, len(cfg.Providers))
		for _, p := range cfg.Providers {
			fmt.Printf("  %s -> %s\n", p.Name, p.BaseURL)
		}
		return nil
	}

	auditor, err := gateway.NewAuditor(cfg.Audit.Path)
	if err != nil {
		return fmt.Errorf("opening audit log: %w", err)
	}
	defer auditor.Close()

	gw := gateway.New(cfg, auditor)
	if err := gw.EnableScanning(cfg); err != nil {
		return fmt.Errorf("enabling promptproof scanning: %w", err)
	}
	defer gw.Close()
	srv := &http.Server{Addr: cfg.Listen, Handler: gw}

	errCh := make(chan error, 1)
	go func() {
		fmt.Printf("modelgate %s listening on %s (%d provider(s))\n", version, cfg.Listen, len(cfg.Providers))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}
