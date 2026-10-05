// Command crackstation runs the crack inspection planning backend.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"crackstation/internal/config"
	"crackstation/internal/httpapi"
	"crackstation/internal/recordlog"
	"crackstation/internal/store"
)

func main() {
	cfg := config.Load()

	var repo store.Repository
	if cfg.Driver == "memory" {
		repo = store.NewMemory()
		log.Printf("using in-memory repository (single process, non-persistent)")
	} else {
		pg, err := store.OpenPostgres(cfg.DSN)
		if err != nil {
			log.Fatalf("open postgres: %v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		if err := pg.Ping(ctx); err != nil {
			cancel()
			log.Fatalf("ping postgres: %v", err)
		}
		cancel()
		if err := runMigrations(pg); err != nil {
			log.Fatalf("migrations: %v", err)
		}
		repo = pg
		log.Printf("connected to PostgreSQL and migrations applied")
	}

	svc := recordlog.NewService(repo)
	srv := httpapi.New(repo, svc)

	httpsrv := &http.Server{Addr: cfg.HTTPAddr, Handler: srv.Handler()}
	go func() {
		log.Printf("crackstation listening on %s", cfg.HTTPAddr)
		if err := httpsrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("http: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpsrv.Shutdown(ctx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}
