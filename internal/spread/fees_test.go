package spread

import (
	"math/big"
	"testing"
)

func rat(t *testing.T, s string) *big.Rat {
	t.Helper()
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		t.Fatalf("bad rational literal %q", s)
	}
	return r
}

func TestParseFeeModel(t *testing.T) {
	tests := []struct {
		name      string
		raw       string
		wantErr   bool
		wantCoeff string
		wantRound bool
	}{
		{
			name:      "kalshi taker",
			raw:       `{"taker_coefficient":"0.07","round_up_to_cent":true}`,
			wantCoeff: "0.07",
			wantRound: true,
		},
		{
			name:      "explicit zero fee is allowed",
			raw:       `{"taker_coefficient":"0"}`,
			wantCoeff: "0",
		},
		// The seeded default is '{}'. Treating that as free would overstate
		// every edge on that venue, so it must fail loudly instead.
		{name: "seeded empty object is rejected", raw: `{}`, wantErr: true},
		{name: "empty document is rejected", raw: ``, wantErr: true},
		{name: "negative coefficient is rejected", raw: `{"taker_coefficient":"-0.07"}`, wantErr: true},
		{name: "non-decimal coefficient is rejected", raw: `{"taker_coefficient":"free"}`, wantErr: true},
		{name: "malformed json is rejected", raw: `{"taker_coefficient":`, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseFeeModel([]byte(tt.raw))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseFeeModel(%q) = nil error, want error", tt.raw)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseFeeModel(%q) unexpected error: %v", tt.raw, err)
			}
			if got.TakerCoefficient.Cmp(rat(t, tt.wantCoeff)) != 0 {
				t.Errorf("coefficient = %s, want %s", got.TakerCoefficient, tt.wantCoeff)
			}
			if got.RoundUpToCent != tt.wantRound {
				t.Errorf("RoundUpToCent = %v, want %v", got.RoundUpToCent, tt.wantRound)
			}
		})
	}
}

// A JSON number would decode through float64 and lose exactness. The parser
// takes text precisely so the coefficient never touches a float.
func TestParseFeeModelRejectsJSONNumber(t *testing.T) {
	if _, err := ParseFeeModel([]byte(`{"taker_coefficient":0.07}`)); err == nil {
		t.Fatal("numeric taker_coefficient should be rejected; it must be a string")
	}
}

func TestFee(t *testing.T) {
	tests := []struct {
		name      string
		coeff     string
		roundUp   bool
		contracts int64
		price     string
		want      string
	}{
		// 0.07 * 0.5 * 0.5 = 0.0175/contract. 100 contracts = $1.75, the
		// documented maximum taker fee per 100 contracts on Kalshi.
		{
			name: "kalshi peak fee at 50c", coeff: "0.07",
			contracts: 100, price: "0.5", want: "1.75",
		},
		// The fee curve is symmetric about 0.50.
		{
			name: "symmetric around half (0.1)", coeff: "0.07",
			contracts: 100, price: "0.1", want: "0.63",
		},
		{
			name: "symmetric around half (0.9)", coeff: "0.07",
			contracts: 100, price: "0.9", want: "0.63",
		},
		// Polymarket's 0.05 category rate: 0.05*0.25 = 0.0125 -> $1.25/100.
		{
			name: "polymarket 0.05 rate at 50c", coeff: "0.05",
			contracts: 100, price: "0.5", want: "1.25",
		},
		{
			name: "zero coefficient costs nothing", coeff: "0",
			contracts: 100, price: "0.5", want: "0",
		},
		// A near-certain outcome is nearly free to trade.
		{
			name: "fee vanishes at the extremes", coeff: "0.07",
			contracts: 1, price: "0.99", want: "0.000693",
		},
		{
			name: "single contract, no rounding", coeff: "0.07",
			contracts: 1, price: "0.5", want: "0.0175",
		},
		// Rounding is applied to the ORDER, and always upward.
		{
			name: "round up to cent", coeff: "0.07", roundUp: true,
			contracts: 1, price: "0.5", want: "0.02",
		},
		{
			name:  "already whole cents is unchanged by rounding",
			coeff: "0.07", roundUp: true,
			contracts: 100, price: "0.5", want: "1.75",
		},
		{
			name: "zero contracts costs nothing", coeff: "0.07",
			contracts: 0, price: "0.5", want: "0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := FeeModel{TakerCoefficient: rat(t, tt.coeff), RoundUpToCent: tt.roundUp}
			got := m.Fee(tt.contracts, rat(t, tt.price))
			if got.Cmp(rat(t, tt.want)) != 0 {
				t.Errorf("Fee(%d, %s) = %s, want %s",
					tt.contracts, tt.price, got.RatString(), tt.want)
			}
		})
	}
}

func TestCeilToCentAlwaysRoundsUp(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"0.001", "0.01"},
		{"0.010", "0.01"},
		{"0.0101", "0.02"},
		{"1.7500", "1.75"},
		{"1.7501", "1.76"},
		{"0", "0"},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got := ceilToCent(rat(t, tt.in))
			if got.Cmp(rat(t, tt.want)) != 0 {
				t.Errorf("ceilToCent(%s) = %s, want %s", tt.in, got.RatString(), tt.want)
			}
		})
	}
}
