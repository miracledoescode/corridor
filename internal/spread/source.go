package spread

import (
	"context"
	"fmt"
	"math/big"
	"strings"
)

// QuoteRow is one outcome's latest quote plus the market, venue and match it
// belongs to. It is the flat shape the database returns; Assemble turns a
// batch of these into the paired legs Compute needs.
//
// WHY the numbers arrive as TEXT: the never-float hard rule. The store selects
// ask::text and liquidity::text so an exact decimal string travels from
// Postgres into big.Rat with nothing float-shaped in between — the same reason
// store.NumericFromString takes venue prices as text on the ingest side.
type QuoteRow struct {
	MatchID   int64
	EventID   *int64
	MarketID  int64
	Title     string
	VenueSlug string
	FeeModel  []byte

	OutcomeID int64
	Label     string

	// AskText and LiquidityText are exact decimal text, or "" for SQL NULL.
	AskText       string
	LiquidityText string
}

// Source supplies the matched markets worth pricing.
//
// WHY an interface here rather than taking *store.Store: it keeps this package
// dependency-free and unit-testable without a database, matching how
// internal/api handlers take narrow interfaces instead of the concrete store.
type Source interface {
	// SpreadCandidates returns the latest quote for every outcome of every
	// market in an EXACT, human-reviewed match.
	SpreadCandidates(ctx context.Context) ([]QuoteRow, error)
}

// Pair is one matched market across two venues, with both sides' legs
// resolved and ready to price.
type Pair struct {
	MatchID int64
	EventID *int64
	Sides   [2]Side
}

// Side is one venue's market within a matched pair.
type Side struct {
	MarketID  int64
	Title     string
	VenueSlug string
	Fees      FeeModel

	// Yes and No are that market's two outcomes. Either may be nil when the
	// venue has not quoted it yet.
	Yes *Leg
	No  *Leg
}

// Assemble groups flat quote rows into pairs ready for Compute.
//
// Rows whose fee model is unusable, or whose match does not resolve to exactly
// two venues, are skipped with a reason rather than guessed at — a mispriced
// or half-resolved pair is how a fake arb reaches an alert.
func Assemble(rows []QuoteRow) ([]Pair, []error) {
	type acc struct {
		eventID *int64
		sides   map[int64]*Side
		order   []int64
	}

	byMatch := map[int64]*acc{}
	var matchOrder []int64
	var problems []error

	for _, r := range rows {
		a, ok := byMatch[r.MatchID]
		if !ok {
			a = &acc{eventID: r.EventID, sides: map[int64]*Side{}}
			byMatch[r.MatchID] = a
			matchOrder = append(matchOrder, r.MatchID)
		}

		side, ok := a.sides[r.MarketID]
		if !ok {
			fees, err := ParseFeeModel(r.FeeModel)
			if err != nil {
				problems = append(problems,
					fmt.Errorf("match %d venue %s: %w", r.MatchID, r.VenueSlug, err))
				continue
			}
			side = &Side{
				MarketID:  r.MarketID,
				Title:     r.Title,
				VenueSlug: r.VenueSlug,
				Fees:      fees,
			}
			a.sides[r.MarketID] = side
			a.order = append(a.order, r.MarketID)
		}

		leg, err := r.leg(side)
		if err != nil {
			// A missing or unparseable quote is normal (a venue may simply not
			// have quoted this outcome yet); it makes the side unpriceable, not
			// the run broken.
			continue
		}

		switch normalizeLabel(r.Label) {
		case "yes":
			side.Yes = leg
		case "no":
			side.No = leg
		}
	}

	var out []Pair
	for _, id := range matchOrder {
		a := byMatch[id]
		if len(a.order) != 2 {
			// One side had an unusable fee model, or the match does not span
			// exactly two markets. Either way it cannot be priced.
			problems = append(problems,
				fmt.Errorf("match %d resolved %d sides, want 2", id, len(a.order)))
			continue
		}
		p := Pair{MatchID: id, EventID: a.eventID}
		p.Sides[0] = *a.sides[a.order[0]]
		p.Sides[1] = *a.sides[a.order[1]]
		out = append(out, p)
	}
	return out, problems
}

func (r QuoteRow) leg(side *Side) (*Leg, error) {
	ask, err := ratFromText(r.AskText)
	if err != nil {
		return nil, err
	}

	l := &Leg{
		VenueSlug: side.VenueSlug,
		MarketID:  side.MarketID,
		OutcomeID: r.OutcomeID,
		Label:     r.Label,
		Ask:       ask,
		Fees:      side.Fees,
	}
	// Liquidity is optional: without it the leg simply prices at zero size.
	if liq, err := ratFromText(r.LiquidityText); err == nil {
		l.LiquidityUSD = liq
	}
	return l, nil
}

// normalizeLabel maps a venue's outcome label onto "yes"/"no".
//
// WHY normalization is needed: the venues do not agree on case. Kalshi emits
// "yes"/"no" and Polymarket "Yes"/"No", so a plain == comparison would silently
// fail to resolve one venue's legs and quietly price nothing.
func normalizeLabel(label string) string {
	return strings.ToLower(strings.TrimSpace(label))
}

func ratFromText(s string) (*big.Rat, error) {
	if s == "" {
		return nil, fmt.Errorf("null")
	}
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		return nil, fmt.Errorf("not a decimal: %q", s)
	}
	return r, nil
}

// Opportunities prices both directions of a pair and returns whichever are
// arbs.
//
// WHY both directions: YES on venue A paired with NO on venue B is a different
// trade from YES on B paired with NO on A, and only one of them is usually
// profitable. Checking one direction would miss half the opportunities.
func (p Pair) Opportunities() []Result {
	var out []Result

	directions := [2][2]*Leg{
		{p.Sides[0].Yes, p.Sides[1].No},
		{p.Sides[1].Yes, p.Sides[0].No},
	}

	for _, d := range directions {
		yes, no := d[0], d[1]
		if yes == nil || no == nil {
			continue
		}
		r, err := Compute(*yes, *no)
		if err != nil || !r.IsArb() {
			continue
		}
		out = append(out, r)
	}
	return out
}
