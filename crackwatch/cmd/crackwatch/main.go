// Command crackwatch runs the crack-monitoring backend service.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"crackwatch/internal/app"
	"crackwatch/internal/httpapi"
	"crackwatch/internal/store"
)

func main() {
	addr := env("ADDR", ":8080")
	dsn := env("DATABASE_URL",
		"postgres://crackwatch:crackwatch@localhost:5432/crackwatch?sslmode=disable")

	ctx, cancel := signalContext()
	defer cancel()

	st, err := store.OpenPostgres(ctx, dsn)
	if err != nil {
		log.Fatalf("open postgres: %v", err)
	}
	defer st.Close()

	svc := app.New(st)
	srv := httpapi.NewServer(svc)

	httpServer := &http.Server{
		Addr:              addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		log.Printf("crackwatch listening on %s", addr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()
	<-ctx.Done()
	shutdownCtx, c := context.WithTimeout(context.Background(), 10*time.Second)
	defer c()
	_ = httpServer.Shutdown(shutdownCtx)
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func signalContext() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-ch
		cancel()
	}()
	return ctx, cancel
}
