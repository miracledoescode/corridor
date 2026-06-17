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

// TestCanonicalKey is the dedup correctness guard: values that are equal must
// key equal regardless of trailing-zero scale (the cache is seeded from
// scale-padded DB text but compared against raw venue text), and distinct
// values — including NULL vs zero — must key apart.
func TestCanonicalKey(t *testing.T) {
	key := func(s string) string {
		n, err := NumericFromString(s)
		if err != nil {
			t.Fatalf("NumericFromString(%q): %v", s, err)
		}
		return canonicalKey(n)
	}

	// Scale must not matter: these are all the same price.
	for _, eq := range [][]string{
		{"0.57", "0.57000", "0.570"},
		{"100", "100.00", "100.000"},
		{"0", "0.0", "0.00", "0.000"},
	} {
		want := key(eq[0])
		for _, s := range eq[1:] {
			if got := key(s); got != want {
				t.Errorf("canonicalKey(%q)=%q, want %q (== canonicalKey(%q))", s, got, want, eq[0])
			}
		}
	}

	// Distinct values, and NULL, must all key apart.
	distinct := map[string]string{
		"NULL": key(""), // empty string → NULL Numeric
		"zero": key("0"),
		"0.57": key("0.57"),
		"0.61": key("0.61"),
		"0.07": key("0.07143"),
	}
	seen := map[string]string{}
	for label, k := range distinct {
		if prev, dup := seen[k]; dup {
			t.Errorf("canonicalKey collision: %s and %s both keyed %q", prev, label, k)
		}
		seen[k] = label
	}
	if distinct["NULL"] != "" {
		t.Errorf("NULL must key to empty string, got %q", distinct["NULL"])
	}
}
