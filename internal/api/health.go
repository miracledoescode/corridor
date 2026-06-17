package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/miracledoescode/corridor/internal/ingest"
	"github.com/miracledoescode/corridor/internal/store"
)

// healthyLag is how stale a venue's heartbeat may be before /healthz reports
// it degraded. Matches the acceptance criterion (lag < 120s).
const healthyLag = 120 * time.Second

type VenueHealth struct {
	Slug         string     `json:"slug"`
	LagSeconds   *int64     `json:"lag_seconds"`    // since last poll; null until first poll
	LastPolledAt *time.Time `json:"last_polled_at"` // liveness heartbeat; null until first poll
	LastQuoteAt  *time.Time `json:"last_quote_at"`  // last PRICE CHANGE; old if prices are stable
	Healthy      bool       `json:"healthy"`
}

type HealthResponse struct {
	Status string        `json:"status"` // "ok" | "degraded"
	Venues []VenueHealth `json:"venues"`
}

// StatusSource is the supervisor's view of venue loops; split out as an
// interface so the handler is testable without a real supervisor.
type StatusSource interface {
	Status() map[string]ingest.VenueStatus
}

// LagSource is the DB's view of quote freshness.
type LagSource interface {
	VenueLags(ctx context.Context) ([]store.VenueLag, error)
}

// healthz combines both views: the DB heartbeat (last_polled_at) says when the
// venue loop last completed a cycle (survives restarts), the supervisor says
// whether the loop is alive right now. WHY the heartbeat and not MAX(quote
// time): quotes are deduped to price changes, so a healthy venue with stable
// prices writes no quotes for minutes — the heartbeat advances regardless.
func healthz(lags LagSource, sup StatusSource, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		vlags, err := lags.VenueLags(r.Context())
		if err != nil {
			log.Error("healthz lag query failed", "err", err)
			http.Error(w, `{"status":"error"}`, http.StatusInternalServerError)
			return
		}
		loopStatus := sup.Status()

		resp := HealthResponse{Status: "ok"}
		now := time.Now()
		for _, vl := range vlags {
			vh := VenueHealth{Slug: vl.Slug, LastPolledAt: vl.LastPolledAt, LastQuoteAt: vl.LastQuoteAt}
			if vl.LastPolledAt != nil {
				lag := int64(now.Sub(*vl.LastPolledAt).Seconds())
				vh.LagSeconds = &lag
				vh.Healthy = now.Sub(*vl.LastPolledAt) < healthyLag
			}
			// A venue with no running loop is unhealthy even if it polled
			// seconds ago — it is about to go stale.
			if st, ok := loopStatus[vl.Slug]; ok && !st.Running {
				vh.Healthy = false
			}
			if !vh.Healthy {
				resp.Status = "degraded"
			}
			resp.Venues = append(resp.Venues, vh)
		}

		// WHY always 200: orchestrators kill containers on failing health
		// probes; one degraded venue must not get the whole binary —
		// including the healthy venues — restarted. "degraded" in the body
		// is for alerting, not for the scheduler.
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}
}
