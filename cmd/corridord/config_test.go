package main

import (
	"testing"
)

// Regression test for the day-one Kalshi outage: the default base URL
// pointed at api.elections.kalshi.com, which is Kalshi's AUTHENTICATED
// host — every anonymous market-data request failed and the venue
// ingested nothing. Keyless public market data lives on
// external-api.kalshi.com; this pins the default so a future "tidy-up"
// can't silently point the bot back at an authenticated host.
func TestKalshiDefaultIsKeylessPublicHost(t *testing.T) {
	t.Setenv("DB_URL", "postgres://example/corridor")
	t.Setenv("KALSHI_BASE_URL", "")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	want := "https://external-api.kalshi.com/trade-api/v2"
	if cfg.kalshiBaseURL != want {
		t.Errorf("kalshiBaseURL default = %q, want %q (keyless public host)", cfg.kalshiBaseURL, want)
	}
}

// Regression test for the set-but-empty incident: `KALSHI_BASE_URL=`
// (no value) in .env reaches the process as an empty string, and an
// empty override must mean UNSET — fall back to the default keyless
// host, never call an empty URL or mask the compiled default.
func TestEmptyEnvFallsBackToDefault(t *testing.T) {
	t.Setenv("DB_URL", "postgres://example/corridor")
	for _, key := range []string{
		"KALSHI_BASE_URL", "POLYMARKET_GAMMA_URL", "POLYMARKET_CLOB_URL", "USER_AGENT_CONTACT",
	} {
		t.Setenv(key, "")
	}

	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.kalshiBaseURL != "https://external-api.kalshi.com/trade-api/v2" {
		t.Errorf("empty KALSHI_BASE_URL did not fall back: %q", cfg.kalshiBaseURL)
	}
	if cfg.polymarketGammaURL != "https://gamma-api.polymarket.com" {
		t.Errorf("empty POLYMARKET_GAMMA_URL did not fall back: %q", cfg.polymarketGammaURL)
	}
	if cfg.polymarketClobURL != "https://clob.polymarket.com" {
		t.Errorf("empty POLYMARKET_CLOB_URL did not fall back: %q", cfg.polymarketClobURL)
	}
	if cfg.userAgentContact == "" {
		t.Error("empty USER_AGENT_CONTACT did not fall back")
	}
}

func TestEnvOverridesKalshiHost(t *testing.T) {
	t.Setenv("DB_URL", "postgres://example/corridor")
	t.Setenv("KALSHI_BASE_URL", "http://localhost:9101/kalshi")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.kalshiBaseURL != "http://localhost:9101/kalshi" {
		t.Errorf("override not honored: %q", cfg.kalshiBaseURL)
	}
}
