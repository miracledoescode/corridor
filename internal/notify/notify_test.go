package notify

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func sampleAlert() Alert {
	return Alert{
		ID:   7,
		Kind: "cross_venue_arb",
		Payload: AlertPayload{
			MatchID:   42,
			Title:     "Will the Fed cut rates in December?",
			GrossEdge: "0.050000",
			NetEdge:   "0.020175",
			Yes:       AlertLeg{Venue: "polymarket", Ask: "0.4500", Label: "Yes"},
			No:        AlertLeg{Venue: "kalshi", Ask: "0.5000", Label: "no"},
		},
	}
}

func TestPercent(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"0.020175", "2.01%"},
		{"0.050000", "5.00%"},
		{"0.005000", "0.50%"},
		{"0.000000", "0.00%"},
		{"-0.019993", "-1.99%"},
		{"0.100000", "10.00%"},
		{"1.000000", "100.00%"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := percent(tt.in); got != tt.want {
				t.Errorf("percent(%s) = %s, want %s", tt.in, got, tt.want)
			}
		})
	}
}

func TestFormatIncludesBothLegsAndEdge(t *testing.T) {
	got := Format(sampleAlert(), "corridor.app")

	for _, want := range []string{
		"2.01%", "polymarket", "kalshi", "0.4500", "0.5000", "corridor.app",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("formatted alert missing %q:\n%s", want, got)
		}
	}
}

// The engine deliberately publishes no size. Inventing one in the copy would
// be the same lie one layer further out.
func TestFormatClaimsNoSize(t *testing.T) {
	got := strings.ToLower(Format(sampleAlert(), ""))
	for _, banned := range []string{"contract", "size", "shares", "up to"} {
		if strings.Contains(got, banned) {
			t.Errorf("alert copy makes a size claim (%q):\n%s", banned, got)
		}
	}
}

// Market titles come from venue APIs — untrusted input rendered as HTML.
func TestFormatEscapesVenueSuppliedTitle(t *testing.T) {
	a := sampleAlert()
	a.Payload.Title = `<script>alert("x")</script> & co`

	got := Format(a, "")

	if strings.Contains(got, "<script>") {
		t.Errorf("unescaped markup from venue title reached the message:\n%s", got)
	}
	if !strings.Contains(got, "&amp; co") {
		t.Errorf("ampersand not escaped:\n%s", got)
	}
}

// The bot token lives in the request PATH, so stdlib errors embed it. Any
// error returned from here would otherwise write the credential to the logs.
func TestSendNeverLeaksTokenInErrors(t *testing.T) {
	const token = "123456:SUPERSECRETTOKENVALUE"

	t.Run("http failure", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		srv.Close() // refuse connections

		tg := NewTelegram(token, quietLog())
		tg.baseURL = srv.URL

		err := tg.Send(context.Background(), "chat1", "hi")
		if err == nil {
			t.Fatal("expected an error from a closed server")
		}
		if strings.Contains(err.Error(), token) {
			t.Errorf("bot token leaked into error: %v", err)
		}
	})

	t.Run("api rejection", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(telegramResponse{OK: false, Description: "chat not found"})
		}))
		defer srv.Close()

		tg := NewTelegram(token, quietLog())
		tg.baseURL = srv.URL

		err := tg.Send(context.Background(), "chat1", "hi")
		if err == nil {
			t.Fatal("expected an error when Telegram returns ok:false")
		}
		if strings.Contains(err.Error(), token) {
			t.Errorf("bot token leaked into error: %v", err)
		}
		if !strings.Contains(err.Error(), "chat not found") {
			t.Errorf("lost Telegram's reason: %v", err)
		}
	})
}

func TestSendPostsToSendMessage(t *testing.T) {
	var gotPath, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		json.NewEncoder(w).Encode(telegramResponse{OK: true})
	}))
	defer srv.Close()

	tg := NewTelegram("tok", quietLog())
	tg.baseURL = srv.URL

	if err := tg.Send(context.Background(), "chat1", "hello"); err != nil {
		t.Fatalf("Send() error: %v", err)
	}
	if gotPath != "/bottok/sendMessage" {
		t.Errorf("path = %s, want /bottok/sendMessage", gotPath)
	}
	if !strings.Contains(gotBody, `"chat_id":"chat1"`) {
		t.Errorf("body missing chat_id: %s", gotBody)
	}
}

type fakeStore struct {
	alerts    []Alert
	err       error
	claimCall int
}

func (f *fakeStore) ClaimUndispatchedAlerts(context.Context, int) ([]Alert, error) {
	f.claimCall++
	if f.err != nil {
		return nil, f.err
	}
	out := f.alerts
	f.alerts = nil // claiming is one-shot, as the stamp makes it
	return out, nil
}

type fakeSender struct {
	sent []string // chat ids
	err  error
}

func (f *fakeSender) Send(_ context.Context, chatID, _ string) error {
	if f.err != nil {
		return f.err
	}
	f.sent = append(f.sent, chatID)
	return nil
}

func TestTickSendsToEveryProChat(t *testing.T) {
	store := &fakeStore{alerts: []Alert{sampleAlert()}}
	sender := &fakeSender{}
	d := NewDispatcher(store, sender, []string{"chatA", "chatB"}, "", quietLog())

	d.tick(context.Background())

	if len(sender.sent) != 2 {
		t.Fatalf("sent to %d chats, want 2", len(sender.sent))
	}
}

// One blocked chat must not deny everyone else their alert.
func TestTickContinuesPastAFailedChat(t *testing.T) {
	store := &fakeStore{alerts: []Alert{sampleAlert()}}
	sender := &fakeSender{err: errors.New("bot was blocked")}
	d := NewDispatcher(store, sender, []string{"chatA", "chatB"}, "", quietLog())

	d.tick(context.Background()) // must not panic or abort early

	if store.claimCall != 1 {
		t.Errorf("claim called %d times, want 1", store.claimCall)
	}
}

// Claiming is one-shot by design: at-most-once beats re-sending a stale arb
// minutes later, when the window was seconds wide.
func TestTickDoesNotResendClaimedAlerts(t *testing.T) {
	store := &fakeStore{alerts: []Alert{sampleAlert()}}
	sender := &fakeSender{}
	d := NewDispatcher(store, sender, []string{"chatA"}, "", quietLog())

	d.tick(context.Background())
	d.tick(context.Background())

	if len(sender.sent) != 1 {
		t.Errorf("sent %d times across two ticks, want 1", len(sender.sent))
	}
}

func TestTickSurvivesStoreFailure(t *testing.T) {
	store := &fakeStore{err: errors.New("db down")}
	sender := &fakeSender{}
	d := NewDispatcher(store, sender, []string{"chatA"}, "", quietLog())

	d.tick(context.Background()) // must not panic

	if len(sender.sent) != 0 {
		t.Error("sent an alert despite failing to claim any")
	}
}

func TestTickWithNoSubscribersSendsNothing(t *testing.T) {
	store := &fakeStore{alerts: []Alert{sampleAlert()}}
	sender := &fakeSender{}
	d := NewDispatcher(store, sender, nil, "", quietLog())

	d.tick(context.Background())

	if len(sender.sent) != 0 {
		t.Error("sent to a chat with no subscribers configured")
	}
}
