package ingest

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClientHardening(t *testing.T) {
	tests := []struct {
		name        string
		handler     http.HandlerFunc
		wantErrSub  string
		wantHTTPErr bool
	}{
		{
			name: "happy path json",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				fmt.Fprint(w, `{"ok":true}`)
			},
		},
		{
			name: "html with status 200 rejected", // captive portal / proxy error page
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/html")
				fmt.Fprint(w, `<html>login required</html>`)
			},
			wantErrSub: "unexpected content-type",
		},
		{
			name: "non-2xx returns truncated snippet",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusBadGateway)
				fmt.Fprint(w, strings.Repeat("x", 5000))
			},
			wantHTTPErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(tt.handler)
			defer srv.Close()

			c := NewClient("test-agent", 10000)
			var out map[string]any
			err := c.GetJSON(context.Background(), srv.URL, &out)

			if tt.wantErrSub == "" && !tt.wantHTTPErr {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if tt.wantErrSub != "" && !strings.Contains(err.Error(), tt.wantErrSub) {
				t.Errorf("error %q does not contain %q", err, tt.wantErrSub)
			}
			if tt.wantHTTPErr {
				var he *HTTPError
				if !errors.As(err, &he) {
					t.Fatalf("expected *HTTPError, got %T", err)
				}
				if he.Status != http.StatusBadGateway {
					t.Errorf("status = %d, want 502", he.Status)
				}
				if len(he.Snippet) > 200 {
					t.Errorf("snippet not truncated: %d bytes", len(he.Snippet))
				}
			}
		})
	}
}

func TestClientSendsIdentifiedUserAgent(t *testing.T) {
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()

	ua := UserAgent("ops@example.com")
	c := NewClient(ua, 10000)
	var out map[string]any
	if err := c.GetJSON(context.Background(), srv.URL, &out); err != nil {
		t.Fatal(err)
	}
	if gotUA != "CorridorBot/0.1 (contact: ops@example.com)" {
		t.Errorf("User-Agent = %q", gotUA)
	}
}
