package store

import (
	"fmt"
	"regexp"

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
