// Package spread computes cross-venue price spreads for EXACT market
// matches, net of each venue's trading fees.
//
// Every number here is exact rational arithmetic (math/big.Rat), never
// float64 — the never-float hard rule. A float rounding error of a hundredth
// of a cent is the difference between "arb" and "no arb" on a thin edge, and
// advertising an arb that isn't there is the single biggest trust risk this
// product has.
package spread

import (
	"encoding/json"
	"fmt"
	"math/big"
)

// FeeModel is a venue's trading-fee schedule, parsed from venues.fee_model.
//
// WHY one shape covers both venues: Kalshi and Polymarket independently
// converged on the same formula — fee = contracts x coefficient x p x (1-p).
// The fee peaks at p=0.50 (a coin-flip market is the most expensive to
// trade) and decays toward zero as price approaches 0 or 1. Only the
// coefficient differs, so the model is one number plus rounding behaviour
// rather than a per-venue special case.
type FeeModel struct {
	// TakerCoefficient is the multiplier in fee = C x coeff x p x (1-p).
	// Taker, not maker: an arb is executed by crossing the spread, so the
	// taker rate is the one that applies. Assuming the (lower, often zero)
	// maker rate would understate cost and manufacture phantom arbs.
	TakerCoefficient *big.Rat

	// RoundUpToCent rounds the order's total fee up to the next whole cent.
	// Kalshi does this; it is charged per order, not per contract.
	RoundUpToCent bool
}

// feeModelJSON mirrors the venues.fee_model JSONB document.
//
// WHY the coefficient is a STRING and not a JSON number: encoding/json
// decodes numbers into float64. Reading "0.07" as a float64 would put a
// binary-approximated value at the centre of the fee math and quietly break
// the never-float rule at the very first parse. Text goes straight into
// big.Rat with no precision loss.
type feeModelJSON struct {
	TakerCoefficient string `json:"taker_coefficient"`
	RoundUpToCent    bool   `json:"round_up_to_cent"`
}

// ParseFeeModel reads a venues.fee_model JSONB document.
//
// An empty or absent document is an ERROR, not a zero-fee default. WHY:
// venues.fee_model ships seeded as '{}', and silently treating that as
// "this venue is free" would overstate every edge on that venue and
// advertise arbs that do not exist. A venue with genuinely no taker fee
// must say so explicitly with "0".
func ParseFeeModel(raw []byte) (FeeModel, error) {
	var m FeeModel

	if len(raw) == 0 {
		return m, fmt.Errorf("empty fee model")
	}

	var doc feeModelJSON
	if err := json.Unmarshal(raw, &doc); err != nil {
		return m, fmt.Errorf("parse fee model: %w", err)
	}

	if doc.TakerCoefficient == "" {
		return m, fmt.Errorf("fee model has no taker_coefficient")
	}

	coeff, ok := new(big.Rat).SetString(doc.TakerCoefficient)
	if !ok {
		return m, fmt.Errorf("taker_coefficient %q is not a decimal", doc.TakerCoefficient)
	}
	if coeff.Sign() < 0 {
		return m, fmt.Errorf("taker_coefficient %q is negative", doc.TakerCoefficient)
	}

	m.TakerCoefficient = coeff
	m.RoundUpToCent = doc.RoundUpToCent
	return m, nil
}

// Fee returns the total fee in dollars for taking `contracts` at price p.
//
// fee = contracts x coefficient x p x (1-p), optionally rounded up to the
// next whole cent for the order as a whole.
func (m FeeModel) Fee(contracts int64, p *big.Rat) *big.Rat {
	fee := new(big.Rat).Mul(m.TakerCoefficient, p) // coeff * p
	fee.Mul(fee, new(big.Rat).Sub(oneRat(), p))    // * (1-p)
	fee.Mul(fee, new(big.Rat).SetInt64(contracts)) // * C

	if m.RoundUpToCent {
		fee = ceilToCent(fee)
	}
	return fee
}

func oneRat() *big.Rat { return new(big.Rat).SetInt64(1) }

// ceilToCent rounds a dollar amount up to the next whole cent.
//
// WHY ceil and not nearest: this is a cost. Rounding a cost down flatters
// the edge, and an edge that is only real because we rounded in our own
// favour is exactly the false positive that destroys trust in an alert.
func ceilToCent(v *big.Rat) *big.Rat {
	hundred := new(big.Rat).SetInt64(100)
	cents := new(big.Rat).Mul(v, hundred)

	num, den := cents.Num(), cents.Denom()
	q, r := new(big.Int).QuoRem(num, den, new(big.Int))
	if r.Sign() > 0 {
		q.Add(q, big.NewInt(1))
	}

	return new(big.Rat).SetFrac(q, big.NewInt(100))
}
