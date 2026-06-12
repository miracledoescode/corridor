// Package api is the HTTP surface of corridord. Phase 1: /healthz only.
package api

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// NewServer wires the chi router with explicit timeouts.
func NewServer(addr string, lags LagSource, sup StatusSource, log *slog.Logger) *http.Server {
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Get("/healthz", healthz(lags, sup, log))

	return &http.Server{
		Addr:              addr,
		Handler:           r,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
}
