package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/wangchaozhi/cyp-agent/internal/app"
	"github.com/wangchaozhi/cyp-agent/internal/config"
)

var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	host := flag.String("host", "127.0.0.1", "HTTP listen host")
	port := flag.Int("port", 8000, "HTTP listen port")
	webDir := flag.String("web-dir", "apps/web/dist", "built React asset directory")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return nil
	}
	if *port < 1 || *port > 65535 {
		return fmt.Errorf("port must be between 1 and 65535")
	}

	settings, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if !isLoopbackHost(*host) && !settings.APIToken.Configured() {
		return errors.New("CYP_API_TOKEN is required when listening on a non-loopback host")
	}
	logger := newLogger(settings.LogLevel)

	rootContext, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	application, err := app.New(rootContext, settings, *webDir, logger)
	if err != nil {
		return fmt.Errorf("build application: %w", err)
	}
	defer application.Close()

	address := net.JoinHostPort(*host, fmt.Sprintf("%d", *port))
	server := &http.Server{
		Addr:              address,
		Handler:           application.API.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       90 * time.Second,
		MaxHeaderBytes:    1 << 20,
		BaseContext: func(net.Listener) context.Context {
			return rootContext
		},
	}

	serveError := make(chan error, 1)
	go func() {
		logger.Info("server_started", "address", address, "version", version,
			"mode", settings.Mode, "execution_venue", settings.ExecutionVenue,
			"live_execution_supported", config.LiveExecutionSupported)
		serveError <- server.ListenAndServe()
	}()

	select {
	case err := <-serveError:
		if !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("serve HTTP: %w", err)
		}
		return nil
	case <-rootContext.Done():
		logger.Info("server_stopping", "reason", rootContext.Err())
	}

	// Closing the application first wakes SSE clients and approval waiters, so
	// Shutdown does not wait for long-lived streams until its deadline.
	application.Close()
	shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownContext); err != nil {
		_ = server.Close()
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	return nil
}

func isLoopbackHost(host string) bool {
	host = strings.Trim(strings.TrimSpace(host), "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	parsed := net.ParseIP(host)
	return parsed != nil && parsed.IsLoopback()
}

func newLogger(level string) *slog.Logger {
	var slogLevel slog.Level
	switch strings.ToUpper(strings.TrimSpace(level)) {
	case "DEBUG":
		slogLevel = slog.LevelDebug
	case "WARN", "WARNING":
		slogLevel = slog.LevelWarn
	case "ERROR":
		slogLevel = slog.LevelError
	default:
		slogLevel = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slogLevel}))
}
