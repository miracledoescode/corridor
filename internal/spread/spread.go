package spread

import (
	"fmt"
	"math/big"
)

// Leg is one side of a cross-venue trade: buying a given outcome on a given
// venue at a given ask price, with the depth the book is showing.
type Leg struct {
	VenueSlug string
	MarketID  int64
	OutcomeID int64
	Label     string // "Yes" / "No" as the venue labels it

	// Ask is what it costs to BUY this outcome right now. An arb is taken,
	// not made, so the ask is the executable price — using last or mid would
	// price a trade nobody can actually get filled at.
	Ask *big.Rat

	// LiquidityUSD is the venue-reported liquidity figure, in DOLLARS.
	//
	// WHY dollars and not contracts: both adapters populate this from a
	// dollar-denominated field — Kalshi's liquidity_dollars (or its legacy
	// cents integer, converted) and Polymarket's Gamma liquidityNum. Treating
	// it as a contract count would overstate size by a factor of roughly 1/price.
	//
	// CAVEAT, and it is a real one: this is a MARKET-level aggregate that the
	// adapters copy onto every outcome of the market. It is not depth resting
	// at this ask. quotes carries bid/ask but no size-at-ask, so the data for
	// a true executable size is not currently ingested. Size derived from this
	// is therefore an upper-bound ESTIMATE, and callers must present it as
	// such rather than as a fillable quantity.
	//
	// Nil or zero means unknown, which is treated as unexecutable.
	LiquidityUSD *big.Rat

	Fees FeeModel
}

// Result is a computed cross-venue opportunity.
type Result struct {
	YesLeg Leg
	NoLeg  Leg

	// Size is the number of contracts the thinner side can actually absorb.
	Size int64

	// GrossEdge is 1 - (yesAsk + noAsk): the edge before fees.
	GrossEdge *big.Rat

	// NetEdge is the per-contract profit after both venues' fees.
	// Positive means an arbitrage exists at this size.
	NetEdge *big.Rat

	// TotalProfit is NetEdge x Size — what the trade is actually worth.
	TotalProfit *big.Rat
}

// IsArb reports whether this opportunity is profitable after fees.
func (r Result) IsArb() bool {
	return r.NetEdge != nil && r.NetEdge.Sign() > 0 && r.Size > 0
}

// Compute evaluates buying YES on one venue and NO on the other.
//
// The trade: buy C contracts of YES at yes.Ask on venue A, and C contracts
// of NO at no.Ask on venue B. Exactly one of them settles at $1.00, so the
// payout is always C. The position is profitable when the combined cost —
// both asks plus both venues' taker fees — is less than that payout.
//
//	net edge per contract = 1 - yesAsk - noAsk - feeYes/C - feeNo/C
//
// WHY the legs must be opposite outcomes on DIFFERENT venues: buying YES and
// NO of the same market on the same venue is not an arb, it is just paying
// the spread to hold both sides of a $1.00 pair.
func Compute(yes, no Leg) (Result, error) {
	if yes.Ask == nil || no.Ask == nil {
		return Result{}, fmt.Errorf("leg missing ask price")
	}
	if yes.VenueSlug == no.VenueSlug {
		return Result{}, fmt.Errorf("both legs on venue %q: not a cross-venue trade", yes.VenueSlug)
	}
	if yes.Fees.TakerCoefficient == nil || no.Fees.TakerCoefficient == nil {
		return Result{}, fmt.Errorf("leg missing fee model")
	}

	// WHY cap at the thinner book: the edge is only real for as many
	// contracts as BOTH sides can fill. Advertising an arb larger than its
	// book is advertising a trade that cannot be executed — the alert would
	// be a lie the moment someone tried to take it.
	size := estimateSize(yes, no)

	gross := new(big.Rat).Sub(oneRat(), new(big.Rat).Add(yes.Ask, no.Ask))

	result := Result{
		YesLeg:      yes,
		NoLeg:       no,
		Size:        size,
		GrossEdge:   gross,
		NetEdge:     new(big.Rat),
		TotalProfit: new(big.Rat),
	}

	if size == 0 {
		// No executable size: report the gross edge for visibility but never
		// call it an arb. NetEdge stays zero so IsArb() is false.
		return result, nil
	}

	cost := new(big.Rat).Add(yes.Ask, no.Ask)
	cost.Mul(cost, new(big.Rat).SetInt64(size))
	cost.Add(cost, yes.Fees.Fee(size, yes.Ask))
	cost.Add(cost, no.Fees.Fee(size, no.Ask))

	payout := new(big.Rat).SetInt64(size)
	profit := new(big.Rat).Sub(payout, cost)

	result.TotalProfit = profit
	result.NetEdge = new(big.Rat).Quo(profit, new(big.Rat).SetInt64(size))
	return result, nil
}

// estimateSize converts each leg's dollar liquidity into a contract count and
// returns the smaller — the most contracts the thinner side could plausibly
// absorb.
//
// Buying C contracts at price P costs C x P dollars, so L dollars of depth
// supports at most L/P contracts. The result is floored: a partial contract
// is not tradable, and rounding a size UP would advertise more than the book
// can take.
//
// This is an upper-bound estimate, not a fill guarantee — see Leg.LiquidityUSD
// for why the underlying figure is a market-level aggregate rather than depth
// at the ask.
func estimateSize(yes, no Leg) int64 {
	y := contractsAffordable(yes)
	n := contractsAffordable(no)
	if n < y {
		return n
	}
	return y
}

func contractsAffordable(l Leg) int64 {
	if l.LiquidityUSD == nil || l.LiquidityUSD.Sign() <= 0 || l.Ask.Sign() <= 0 {
		return 0
	}
	contracts := new(big.Rat).Quo(l.LiquidityUSD, l.Ask)
	// Floor of an exact rational: integer division of numerator by denominator.
	return new(big.Int).Div(contracts.Num(), contracts.Denom()).Int64()
}

// FloatString renders an exact rational as a fixed-precision decimal string
// for display and for the alerts.payload JSONB. Rendering happens only at
// the very edge of the system, after all arithmetic is done.
func FloatString(r *big.Rat, places int) string {
	if r == nil {
		return ""
	}
	return r.FloatString(places)
}
