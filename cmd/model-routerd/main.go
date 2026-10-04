package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Dionmm/model-classifier/internal/daemon"
	"github.com/Dionmm/model-classifier/internal/telemetry"
	"github.com/Dionmm/model-classifier/internal/wire"
)

func main() {
	os.Exit(run())
}

func run() int {
	var configPath, socketPath string
	flag.StringVar(&configPath, "config", wire.ConfigPath(), "config JSON path")
	flag.StringVar(&socketPath, "socket", wire.SocketPath(), "unix socket path")
	flag.Parse()
	cfg, err := daemon.LoadConfig(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "model-routerd: config: %v\n", err)
		return 1
	}
	ctx := context.Background()
	tel, err := telemetry.Setup(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "model-routerd: telemetry: %v\n", err)
		return 1
	}
	dlog, err := telemetry.NewDecisionLog(wire.DecisionsPath(), telemetry.DefaultDecisionLogMaxBytes, 3, 1024, tel.Metrics)
	if err != nil {
		fmt.Fprintf(os.Stderr, "model-routerd: decision log: %v\n", err)
		return 1
	}
	srv, err := daemon.New(daemon.Options{Config: cfg, Telemetry: tel, DecisionLog: dlog})
	if err != nil {
		fmt.Fprintf(os.Stderr, "model-routerd: init: %v\n", err)
		return 1
	}
	ln, err := srv.Listen(socketPath, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "model-routerd: listen: %v\n", err)
		return 1
	}
	httpSrv := srv.HTTPServer()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	done := make(chan struct{})
	go srv.WarmupLoop(ctx)
	go func() {
		defer close(done)
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutdownCtx)
		_ = dlog.Close()
		otelCtx, cancelOtel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancelOtel()
		_ = tel.Shutdown(otelCtx)
		_ = os.Remove(socketPath)
	}()
	if err := httpSrv.Serve(ln); err != nil && err != http.ErrServerClosed {
		fmt.Fprintf(os.Stderr, "model-routerd: serve: %v\n", err)
		stop()
		<-done
		return 1
	}
	waitForShutdown(ctx, done)
	return 0
}

func waitForShutdown(ctx context.Context, done <-chan struct{}) {
	if ctx.Err() != nil {
		<-done
	}
}
