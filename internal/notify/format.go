package notify

import (
	"fmt"
	"html"
	"strings"
)

// Alert is the stored alert as the dispatcher needs it.
type Alert struct {
	ID      int64
	Kind    string
	Payload AlertPayload
}

// AlertPayload mirrors the spread engine's alerts.payload document.
type AlertPayload struct {
	MatchID   int64    `json:"match_id"`
	Title     string   `json:"title"`
	GrossEdge string   `json:"gross_edge"`
	NetEdge   string   `json:"net_edge"`
	Yes       AlertLeg `json:"yes"`
	No        AlertLeg `json:"no"`
}

type AlertLeg struct {
	Venue    string `json:"venue"`
	MarketID int64  `json:"market_id"`
	Label    string `json:"label"`
	Ask      string `json:"ask"`
}

// Format renders an alert as terse, numeric, screenshot-friendly copy.
//
// WHY every field is HTML-escaped: the market title comes from a venue's API,
// which is to say from outside. Telegram parses this message as HTML, so a
// title containing < or & would break rendering at best and inject markup at
// worst. Venue data is untrusted input, even when the venue is reputable.
//
// WHY no size line: the engine deliberately publishes no size (quotes carries
// no depth-at-ask), and inventing one here would be the same lie one layer
// further out.
func Format(a Alert, watermark string) string {
	var b strings.Builder

	edge := percent(a.Payload.NetEdge)

	fmt.Fprintf(&b, "<b>ARB %s</b>\n", edge)
	fmt.Fprintf(&b, "%s\n\n", html.EscapeString(a.Payload.Title))

	fmt.Fprintf(&b, "BUY YES  %s  @ %s\n",
		html.EscapeString(a.Payload.Yes.Venue), html.EscapeString(a.Payload.Yes.Ask))
	fmt.Fprintf(&b, "BUY NO   %s  @ %s\n",
		html.EscapeString(a.Payload.No.Venue), html.EscapeString(a.Payload.No.Ask))

	fmt.Fprintf(&b, "\ngross %s · net of fees %s\n",
		percent(a.Payload.GrossEdge), edge)

	if watermark != "" {
		fmt.Fprintf(&b, "\n%s", html.EscapeString(watermark))
	}
	return b.String()
}

// percent renders a per-contract edge like "0.020175" as "2.02%".
//
// The value is a share of $1.00 per contract, so a percentage is just the
// decimal shifted two places. Parsing is string-only: no float ever holds an
// edge, consistent with the exact arithmetic that produced it.
func percent(decimal string) string {
	neg := strings.HasPrefix(decimal, "-")
	s := strings.TrimPrefix(decimal, "-")

	intPart, frac, _ := strings.Cut(s, ".")
	frac += "0000"

	// Shift two places: the first two fractional digits join the integer part.
	shifted := strings.TrimLeft(intPart+frac[:2], "0")
	if shifted == "" {
		shifted = "0"
	}
	out := shifted + "." + frac[2:4] + "%"
	if neg {
		out = "-" + out
	}
	return out
}
