package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"recommendation_v2/config"
	"recommendation_v2/internal/infrastructure"

	"trpc.group/trpc-go/trpc-agent-go/log"
)

func main() {
	if err := config.Init(); err != nil {
		log.Fatalf("config init failed: %v", err)
	}

	ctx := context.Background()
	telemetryCleanup, err := infrastructure.InitTelemetry(ctx)
	if err != nil {
		log.Fatalf("telemetry init failed: %v", err)
	}
	defer telemetryCleanup()

	deps, err := InitDependencies(ctx)
	if err != nil {
		log.Fatalf("dependencies init failed: %v", err)
	}

	r := SetupRouter(deps)

	addr := config.Cfg.Services.HTTPAddr + ":" + config.Cfg.Services.HTTPPort
	if addr == ":" {
		addr = "127.0.0.1:8082"
	}
	srv := &http.Server{Addr: addr, Handler: r}

	go func() {
		log.Infof("[main] http server listening on %s", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("http server error: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	Shutdown(deps)
	log.Info("[main] shutting down...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Errorf("[main] http server shutdown error: %v", err)
	}
	log.Info("[main] stopped")
}
