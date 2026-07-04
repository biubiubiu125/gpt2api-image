package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"gpt2api-image/internal/app"
)

func main() {
	mode := strings.TrimSpace(os.Getenv("GPT2API_IMAGE_MODE"))
	if len(os.Args) > 1 && strings.TrimSpace(os.Args[1]) != "" {
		mode = strings.TrimSpace(os.Args[1])
	}
	if mode == "" {
		mode = "serve"
	}
	if mode != "serve" && mode != "worker" && mode != "all" {
		log.Fatalf("invalid GPT2API_IMAGE_MODE %q, expected serve, worker, or all", mode)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if mode == "worker" {
		if err := app.RunWorker(ctx, "."); err != nil {
			log.Fatal(err)
		}
		return
	}
	if mode == "all" {
		if err := app.ValidateWorkerConfig("."); err != nil {
			log.Fatal(err)
		}
		go func() {
			if err := app.RunWorker(ctx, "."); err != nil {
				log.Fatalf("worker stopped: %v", err)
			}
		}()
	}
	srv, err := app.NewServer(".")
	if err != nil {
		log.Fatal(err)
	}
	addr := os.Getenv("GPT2API_IMAGE_ADDR")
	if addr == "" {
		addr = ":3000"
	}
	httpServer := &http.Server{
		Addr:              addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       120 * time.Second,
		WriteTimeout:      10 * time.Minute,
		IdleTimeout:       120 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()
	log.Printf("gpt2api-image listening on %s", addr)
	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
