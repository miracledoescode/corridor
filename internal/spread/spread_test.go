package spread

import (
	"math/big"
	"testing"
)

// leg builds a test leg. liquidityUSD is dollars of book depth, matching what
// the adapters actually store; "" means the venue reported none.
func leg(t *testing.T, venue, ask, coeff, liquidityUSD string) Leg {
	t.Helper()
	l := Leg{
		VenueSlug: venue,
		Ask:       rat(t, ask),
		Fees:      FeeModel{TakerCoefficient: rat(t, coeff)},
	}
	if liquidityUSD != "" {
		l.LiquidityUSD = rat(t, liquidityUSD)
	}
	return l
}

func TestComputeRejectsBadInput(t *testing.T) {
	good := leg(t, "kalshi", "0.50", "0.07", "1000")

	tests := []struct {
		name    string
		yes, no Leg
	}{
		{
			name: "same venue is not a cross-venue trade",
			yes:  leg(t, "kalshi", "0.40", "0.07", "1000"),
			no:   leg(t, "kalshi", "0.40", "0.07", "1000"),
		},
		{
			name: "missing ask",
			yes:  Leg{VenueSlug: "polymarket", Fees: good.Fees, LiquidityUSD: rat(t, "1000")},
			no:   good,
		},
		{
			name: "missing fee model",
			yes:  Leg{VenueSlug: "polymarket", Ask: rat(t, "0.40"), LiquidityUSD: rat(t, "1000")},
			no:   good,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Compute(tt.yes, tt.no); err == nil {
				t.Error("Compute() = nil error, want error")
			}
		})
	}
}

func TestComputeArbDetection(t *testing.T) {
	tests := []struct {
		name         string
		yesAsk       string
		noAsk        string
		yesCoeff     string
		noCoeff      string
		liquidityUSD string
		wantArb      bool
		wantNetEdge  string
	}{
		{
			// 0.45 + 0.50 = 0.95 gross. Fees: 0.07*.45*.55=0.017325 and
			// 0.05*.50*.50=0.0125 -> 0.029825. Net 0.05 - 0.029825.
			name:   "clear arb survives fees",
			yesAsk: "0.45", noAsk: "0.50",
			yesCoeff: "0.07", noCoeff: "0.05",
			liquidityUSD: "100000", wantArb: true, wantNetEdge: "0.020175",
		},
		{
			// The case that matters most: gross edge looks positive, fees eat
			// it. Shipping this as an alert would be advertising a fake arb.
			// gross +0.01, but fees are 0.07*.49*.51 + 0.05*.50*.50
			// = 0.017493 + 0.0125 = 0.029993 per contract.
			name:   "thin gross edge is destroyed by fees",
			yesAsk: "0.49", noAsk: "0.50",
			yesCoeff: "0.07", noCoeff: "0.05",
			liquidityUSD: "100000", wantArb: false, wantNetEdge: "-0.019993",
		},
		{
			name:   "exactly 1.00 gross is not an arb",
			yesAsk: "0.50", noAsk: "0.50",
			yesCoeff: "0", noCoeff: "0",
			liquidityUSD: "100000", wantArb: false, wantNetEdge: "0",
		},
		{
			name:   "over 1.00 gross is never an arb",
			yesAsk: "0.55", noAsk: "0.50",
			yesCoeff: "0", noCoeff: "0",
			liquidityUSD: "100000", wantArb: false, wantNetEdge: "-0.05",
		},
		{
			name:   "zero-fee venues keep the whole gross edge",
			yesAsk: "0.45", noAsk: "0.50",
			yesCoeff: "0", noCoeff: "0",
			liquidityUSD: "100000", wantArb: true, wantNetEdge: "0.05",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			yes := leg(t, "polymarket", tt.yesAsk, tt.yesCoeff, tt.liquidityUSD)
			no := leg(t, "kalshi", tt.noAsk, tt.noCoeff, tt.liquidityUSD)

			got, err := Compute(yes, no)
			if err != nil {
				t.Fatalf("Compute() unexpected error: %v", err)
			}
			if got.IsArb() != tt.wantArb {
				t.Errorf("IsArb() = %v, want %v (net edge %s)",
					got.IsArb(), tt.wantArb, got.NetEdge.FloatString(6))
			}
			if got.NetEdge.Cmp(rat(t, tt.wantNetEdge)) != 0 {
				t.Errorf("NetEdge = %s, want %s",
					got.NetEdge.FloatString(6), tt.wantNetEdge)
			}
		})
	}
}

// "Never advertise an arb bigger than its book."
//
// Liquidity arrives in DOLLARS, so depth converts to contracts by dividing by
// the price — $450 at 45c is 1000 contracts, not 450.
func TestComputeCapsSizeAtThinnerBook(t *testing.T) {
	tests := []struct {
		name     string
		yesLiq   string // dollars, at ask 0.45
		noLiq    string // dollars, at ask 0.50
		wantSize int64
	}{
		// $450/0.45 = 1000 contracts; $5000/0.50 = 10000. Yes is thinner.
		{"thinner side is yes", "450", "5000", 1000},
		// $45000/0.45 = 100000; $1000/0.50 = 2000. No is thinner.
		{"thinner side is no", "45000", "1000", 2000},
		// Equal DOLLARS is not equal contracts: $900/0.45 = 2000 but
		// $900/0.50 = 1800, so the pricier leg binds.
		{"equal dollars still converts by price", "900", "900", 1800},
		// Floor, never round up: $100/0.45 = 222.22 -> 222.
		{"partial contracts are floored", "100", "5000", 222},
		{"one side dry is unexecutable", "0", "5000", 0},
		{"both sides dry", "0", "0", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			yes := leg(t, "polymarket", "0.45", "0", tt.yesLiq)
			no := leg(t, "kalshi", "0.50", "0", tt.noLiq)

			got, err := Compute(yes, no)
			if err != nil {
				t.Fatalf("Compute() unexpected error: %v", err)
			}
			if got.Size != tt.wantSize {
				t.Errorf("Size = %d, want %d", got.Size, tt.wantSize)
			}
			// Total profit must track the executable size, not the fat side.
			wantProfit := new(big.Rat).Mul(got.NetEdge, new(big.Rat).SetInt64(tt.wantSize))
			if got.TotalProfit.Cmp(wantProfit) != 0 {
				t.Errorf("TotalProfit = %s, want %s",
					got.TotalProfit.RatString(), wantProfit.RatString())
			}
		})
	}
}

// An unfillable edge is not an opportunity, however good the price looks.
func TestComputeZeroLiquidityIsNeverAnArb(t *testing.T) {
	yes := leg(t, "polymarket", "0.10", "0", "")
	no := leg(t, "kalshi", "0.10", "0", "500")

	got, err := Compute(yes, no)
	if err != nil {
		t.Fatalf("Compute() unexpected error: %v", err)
	}
	if got.IsArb() {
		t.Error("IsArb() = true with zero executable size, want false")
	}
	// The gross edge is still reported, for visibility.
	if got.GrossEdge.Cmp(rat(t, "0.80")) != 0 {
		t.Errorf("GrossEdge = %s, want 0.80", got.GrossEdge.RatString())
	}
}

// Fees are charged on both legs; forgetting one silently doubles the edge.
func TestComputeChargesBothLegs(t *testing.T) {
	both := leg(t, "polymarket", "0.45", "0.07", "100000")
	kalshi := leg(t, "kalshi", "0.50", "0.07", "1000")

	withBoth, err := Compute(both, kalshi)
	if err != nil {
		t.Fatalf("Compute() unexpected error: %v", err)
	}

	freeNo := leg(t, "kalshi", "0.50", "0", "100000")
	withOne, err := Compute(both, freeNo)
	if err != nil {
		t.Fatalf("Compute() unexpected error: %v", err)
	}

	if withBoth.NetEdge.Cmp(withOne.NetEdge) >= 0 {
		t.Errorf("charging both legs (%s) should cost more than one (%s)",
			withBoth.NetEdge.FloatString(6), withOne.NetEdge.FloatString(6))
	}
}
