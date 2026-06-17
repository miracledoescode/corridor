package store

import (
	"fmt"
	"math/big"
	"regexp"
	"strconv"

	"github.com/jackc/pgx/v5/pgtype"
)

// decimalRe accepts plain decimal text: optional sign, digits, optional
// fraction. Exponents and venue garbage ("NaN", "1e-5", "") are rejected
// before they get near the database.
var decimalRe = regexp.MustCompile(`^-?(\d+(\.\d*)?|\.\d+)$`)

// NumericFromString converts exact decimal text to pgtype.Numeric.
// An empty string maps to SQL NULL (Valid=false) — venues omit fields all
// the time, and NULL must stay distinct from zero.
//
// WHY this exists at all: the never-float hard rule. Prices travel as
// strings from the JSON decoder to here, and pgtype parses the decimal
// text directly — no float64 ever holds a price.
func NumericFromString(s string) (pgtype.Numeric, error) {
	var n pgtype.Numeric
	if s == "" {
		return n, nil // Valid=false → NULL
	}
	if !decimalRe.MatchString(s) {
		return n, fmt.Errorf("not a plain decimal: %q", s)
	}
	if err := n.Scan(s); err != nil {
		return n, fmt.Errorf("parse numeric %q: %w", s, err)
	}
	return n, nil
}

// canonicalKey returns a scale-independent identity string for a Numeric:
// numerically equal values produce equal keys regardless of trailing zeros,
// so "0.5", "0.50" and "0.500" all map to the same key, and NULL maps to "".
//
// WHY the write-path dedup needs this rather than a raw string compare:
// the cache is seeded at startup from the DB (q.bid::text → "0.57000",
// scale-padded by the NUMERIC(8,5) column) but compared against freshly
// parsed venue text ("0.57"). A byte compare would see those as different
// and write a redundant row on the first post-restart cycle for every
// outcome. Normalizing to (mantissa, exponent) with trailing zeros stripped
// makes the two representations compare equal — a true value comparison,
// never float (the never-float hard rule still holds; this is big.Int math).
func canonicalKey(n pgtype.Numeric) string {
	if !n.Valid {
		return "" // NULL — distinct from any real price, including zero
	}
	if n.NaN || n.InfinityModifier != pgtype.Finite {
		return "nan" // never expected for a price; collapse to one bucket
	}
	if n.Int == nil || n.Int.Sign() == 0 {
		return "0"
	}
	i := new(big.Int).Set(n.Int)
	exp := n.Exp
	ten := big.NewInt(10)
	rem := new(big.Int)
	for {
		q, _ := new(big.Int).QuoRem(i, ten, rem)
		if rem.Sign() != 0 {
			break
		}
		i, exp = q, exp+1
	}
	return i.String() + "e" + strconv.Itoa(int(exp))
}
