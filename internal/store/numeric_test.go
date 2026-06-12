package store

import (
	"math/big"
	"testing"
)

func TestNumericFromString(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantErr bool
		valid   bool   // expected pgtype Valid flag
		wantInt string // expected unscaled integer digits, if valid
		wantExp int32
	}{
		{"typical price", "0.57", false, true, "57", -2},
		{"five decimals", "0.07143", false, true, "7143", -5},
		{"integer", "100", false, true, "1", 2},
		{"zero", "0", false, true, "0", 0},
		{"leading dot", ".5", false, true, "5", -1},
		{"negative", "-0.25", false, true, "-25", -2},
		{"empty means NULL", "", false, false, "", 0},
		{"exponent rejected", "1e-5", true, false, "", 0},
		{"NaN rejected", "NaN", true, false, "", 0},
		{"text rejected", "abc", true, false, "", 0},
		{"trailing junk rejected", "0.5x", true, false, "", 0},
		{"comma rejected", "1,5", true, false, "", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NumericFromString(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("NumericFromString(%q) err = %v, wantErr %v", tt.in, err, tt.wantErr)
			}
			if err != nil {
				return
			}
			if got.Valid != tt.valid {
				t.Fatalf("NumericFromString(%q) Valid = %v, want %v", tt.in, got.Valid, tt.valid)
			}
			if !tt.valid {
				return
			}
			wantInt, _ := new(big.Int).SetString(tt.wantInt, 10)
			if got.Int.Cmp(wantInt) != 0 || got.Exp != tt.wantExp {
				t.Errorf("NumericFromString(%q) = %v x 10^%d, want %v x 10^%d",
					tt.in, got.Int, got.Exp, wantInt, tt.wantExp)
			}
		})
	}
}
