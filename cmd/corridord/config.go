package main

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

type config struct {
	dbURL    string
	redisURL string
	logLevel slog.Level
	port     string

	polymarketMetaEvery time.Duration
	kalshiMetaEvery     time.Duration
	quoteEvery          time.Duration

	quoteRetentionDays int

	// spreadEvery is how often the arb scan runs. Zero disables the engine,
	// which is how it ships until the fee models in migration 005 have been
	// verified against the venues' published schedules — a wrong coefficient
	// there turns thin spreads into phantom arbs.
	spreadEvery time.Duration

	// Telegram delivery. An empty token disables notify entirely, so the
	// engine can run and write alerts without pushing them anywhere.
	telegramToken     string
	telegramProChats  []string
	telegramWatermark string
	notifyEvery       time.Duration

	polymarketGammaURL string
	polymarketClobURL  string
	kalshiBaseURL      string

	userAgentContact string
}

// loadConfig reads everything from the environment. No flags, no files:
// the container, the Makefile, and `make run` all configure the binary
// the same single way.
func loadConfig() (config, error) {
	c := config{
		dbURL:               os.Getenv("DB_URL"),
		redisURL:            envDefault("REDIS_URL", ""),
		port:                envDefault("PORT", "8080"),
		polymarketMetaEvery: envSeconds("POLYMARKET_POLL_INTERVAL_S", 60),
		kalshiMetaEvery:     envSeconds("KALSHI_POLL_INTERVAL_S", 60),
		quoteEvery:          envSeconds("QUOTE_POLL_INTERVAL_S", 10),
		quoteRetentionDays:  envInt("QUOTE_RETENTION_DAYS", 7),
		spreadEvery:         envSeconds("SPREAD_SCAN_INTERVAL_S", 0),
		telegramToken:       os.Getenv("TELEGRAM_BOT_TOKEN"),
		telegramProChats:    envList("TELEGRAM_PRO_CHAT_IDS"),
		telegramWatermark:   envDefault("TELEGRAM_WATERMARK", "corridor.app"),
		notifyEvery:         envSeconds("NOTIFY_INTERVAL_S", 15),
		polymarketGammaURL:  envDefault("POLYMARKET_GAMMA_URL", "https://gamma-api.polymarket.com"),
		polymarketClobURL:   envDefault("POLYMARKET_CLOB_URL", "https://clob.polymarket.com"),
		// WHY external-api and not api.elections: Kalshi's docs put
		// keyless public market data on external-api.kalshi.com
		// ("public endpoints that don't require API keys");
		// api.elections.kalshi.com is the authenticated host that
		// expects key-signed requests — pointing an anonymous bot at it
		// fails every request and ingests nothing.
		kalshiBaseURL:    envDefault("KALSHI_BASE_URL", "https://external-api.kalshi.com/trade-api/v2"),
		userAgentContact: envDefault("USER_AGENT_CONTACT", "miraclesayscode@gmail.com"),
	}
	if c.dbURL == "" {
		return c, fmt.Errorf("DB_URL is required")
	}
	switch envDefault("LOG_LEVEL", "info") {
	case "debug":
		c.logLevel = slog.LevelDebug
	case "warn":
		c.logLevel = slog.LevelWarn
	case "error":
		c.logLevel = slog.LevelError
	default:
		c.logLevel = slog.LevelInfo
	}
	return c, nil
}

func envDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return def
	}
	return n
}

func envSeconds(key string, def int) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return time.Duration(def) * time.Second
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return time.Duration(def) * time.Second
	}
	return time.Duration(n) * time.Second
}

// envList reads a comma-separated list, dropping blanks and surrounding
// whitespace so a trailing comma or a copy-pasted "a, b" does not become an
// empty chat id the dispatcher would then try to send to.
func envList(key string) []string {
	raw := os.Getenv(key)
	if raw == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}
