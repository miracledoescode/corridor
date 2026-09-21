package spread

import "testing"

const kalshiFees = `{"taker_coefficient":"0.07","round_up_to_cent":true}`
const polyFees = `{"taker_coefficient":"0.07"}`

func row(matchID, marketID, outcomeID int64, venue, fees, label, ask, liq string) QuoteRow {
	return QuoteRow{
		MatchID:       matchID,
		MarketID:      marketID,
		OutcomeID:     outcomeID,
		Title:         "Will X happen?",
		VenueSlug:     venue,
		FeeModel:      []byte(fees),
		Label:         label,
		AskText:       ask,
		LiquidityText: liq,
	}
}

// The venues disagree on case: Kalshi emits "yes"/"no", Polymarket "Yes"/"No".
// A plain == would resolve one venue's legs and silently price nothing.
func TestAssembleNormalizesLabelCase(t *testing.T) {
	rows := []QuoteRow{
		row(1, 10, 100, "polymarket", polyFees, "Yes", "0.45", "5000"),
		row(1, 10, 101, "polymarket", polyFees, "No", "0.56", "5000"),
		row(1, 20, 200, "kalshi", kalshiFees, "yes", "0.47", "5000"),
		row(1, 20, 201, "kalshi", kalshiFees, "no", "0.50", "5000"),
	}

	pairs, problems := Assemble(rows)
	if len(problems) != 0 {
		t.Fatalf("unexpected problems: %v", problems)
	}
	if len(pairs) != 1 {
		t.Fatalf("got %d pairs, want 1", len(pairs))
	}

	for i, side := range pairs[0].Sides {
		if side.Yes == nil {
			t.Errorf("side %d (%s) has no YES leg", i, side.VenueSlug)
		}
		if side.No == nil {
			t.Errorf("side %d (%s) has no NO leg", i, side.VenueSlug)
		}
	}
}

func TestAssembleSkipsUnusableFeeModel(t *testing.T) {
	// '{}' is exactly what venues.fee_model ships seeded as. Pricing against
	// it would treat the venue as free and invent edges.
	rows := []QuoteRow{
		row(1, 10, 100, "polymarket", `{}`, "Yes", "0.45", "5000"),
		row(1, 20, 200, "kalshi", kalshiFees, "no", "0.50", "5000"),
	}

	pairs, problems := Assemble(rows)
	if len(pairs) != 0 {
		t.Errorf("got %d pairs, want 0 — a venue with no fee model is unpriceable", len(pairs))
	}
	if len(problems) == 0 {
		t.Error("expected a reported problem for the empty fee model")
	}
}

func TestAssembleSkipsMatchMissingASide(t *testing.T) {
	rows := []QuoteRow{
		row(1, 10, 100, "polymarket", polyFees, "Yes", "0.45", "5000"),
		row(1, 10, 101, "polymarket", polyFees, "No", "0.56", "5000"),
	}

	pairs, problems := Assemble(rows)
	if len(pairs) != 0 {
		t.Errorf("got %d pairs, want 0 — one venue is not a cross-venue pair", len(pairs))
	}
	if len(problems) == 0 {
		t.Error("expected a reported problem for the one-sided match")
	}
}

// A venue that has not quoted an outcome yet is normal, not an error.
func TestAssembleToleratesNullQuote(t *testing.T) {
	rows := []QuoteRow{
		row(1, 10, 100, "polymarket", polyFees, "Yes", "0.45", "5000"),
		row(1, 10, 101, "polymarket", polyFees, "No", "", ""), // never quoted
		row(1, 20, 200, "kalshi", kalshiFees, "yes", "0.47", "5000"),
		row(1, 20, 201, "kalshi", kalshiFees, "no", "0.50", "5000"),
	}

	pairs, problems := Assemble(rows)
	if len(problems) != 0 {
		t.Fatalf("a missing quote should not be a problem, got %v", problems)
	}
	if len(pairs) != 1 {
		t.Fatalf("got %d pairs, want 1", len(pairs))
	}
	if pairs[0].Sides[0].No != nil {
		t.Error("unquoted outcome should have no leg")
	}
}

func TestAssembleGroupsMultipleMatches(t *testing.T) {
	rows := []QuoteRow{
		row(1, 10, 100, "polymarket", polyFees, "Yes", "0.45", "5000"),
		row(1, 20, 200, "kalshi", kalshiFees, "no", "0.50", "5000"),
		row(2, 30, 300, "polymarket", polyFees, "Yes", "0.30", "5000"),
		row(2, 40, 400, "kalshi", kalshiFees, "no", "0.60", "5000"),
	}

	pairs, problems := Assemble(rows)
	if len(problems) != 0 {
		t.Fatalf("unexpected problems: %v", problems)
	}
	if len(pairs) != 2 {
		t.Fatalf("got %d pairs, want 2", len(pairs))
	}
	if pairs[0].MatchID != 1 || pairs[1].MatchID != 2 {
		t.Errorf("match ids = %d,%d, want 1,2", pairs[0].MatchID, pairs[1].MatchID)
	}
}

// Both directions must be checked: YES@A+NO@B is a different trade from
// YES@B+NO@A, and usually only one is profitable.
func TestOpportunitiesChecksBothDirections(t *testing.T) {
	// polymarket YES 0.45 + kalshi NO 0.50 = 0.95 gross -> arb after fees.
	// kalshi YES 0.60 + polymarket NO 0.56 = 1.16 gross -> never an arb.
	rows := []QuoteRow{
		row(1, 10, 100, "polymarket", polyFees, "Yes", "0.45", "50000"),
		row(1, 10, 101, "polymarket", polyFees, "No", "0.56", "50000"),
		row(1, 20, 200, "kalshi", kalshiFees, "yes", "0.60", "50000"),
		row(1, 20, 201, "kalshi", kalshiFees, "no", "0.50", "50000"),
	}

	pairs, _ := Assemble(rows)
	if len(pairs) != 1 {
		t.Fatalf("got %d pairs, want 1", len(pairs))
	}

	opps := pairs[0].Opportunities()
	if len(opps) != 1 {
		t.Fatalf("got %d opportunities, want exactly 1 profitable direction", len(opps))
	}
	if opps[0].YesLeg.VenueSlug != "polymarket" || opps[0].NoLeg.VenueSlug != "kalshi" {
		t.Errorf("profitable direction = YES@%s/NO@%s, want YES@polymarket/NO@kalshi",
			opps[0].YesLeg.VenueSlug, opps[0].NoLeg.VenueSlug)
	}
}

func TestOpportunitiesReturnsNothingWhenNoEdge(t *testing.T) {
	rows := []QuoteRow{
		row(1, 10, 100, "polymarket", polyFees, "Yes", "0.55", "50000"),
		row(1, 10, 101, "polymarket", polyFees, "No", "0.46", "50000"),
		row(1, 20, 200, "kalshi", kalshiFees, "yes", "0.56", "50000"),
		row(1, 20, 201, "kalshi", kalshiFees, "no", "0.45", "50000"),
	}

	pairs, _ := Assemble(rows)
	if got := pairs[0].Opportunities(); len(got) != 0 {
		t.Errorf("got %d opportunities, want 0", len(got))
	}
}
