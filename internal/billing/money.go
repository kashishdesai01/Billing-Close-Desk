// Package billing implements a deliberately bounded USD monthly CPM workflow.
package billing

import (
	"errors"
	"math/big"
	"regexp"
)

var ratePattern = regexp.MustCompile(`^[0-9]{1,12}(\.[0-9]{1,6})?$`)

// Price converts impressions and dollars-per-thousand to cents, rounding once,
// half away from zero. Intermediates are arbitrary precision; persisted amounts
// are capped below JavaScript's exact integer boundary.
func Price(impressions int64, rate string) (int64, error) {
	if impressions < 0 || impressions > 1_000_000_000_000 || !ratePattern.MatchString(rate) {
		return 0, errors.New("invalid quantity or rate (maximum six decimals)")
	}
	r, ok := new(big.Rat).SetString(rate)
	if !ok || r.Sign() <= 0 {
		return 0, errors.New("rate must be positive")
	}
	r.Mul(r, new(big.Rat).SetFrac(big.NewInt(impressions), big.NewInt(10)))
	q, rem := new(big.Int), new(big.Int)
	q.QuoRem(r.Num(), r.Denom(), rem)
	if new(big.Int).Mul(rem, big.NewInt(2)).Cmp(r.Denom()) >= 0 {
		q.Add(q, big.NewInt(1))
	}
	if !q.IsInt64() || q.Int64() > 1_000_000_000_000 {
		return 0, errors.New("amount exceeds supported limit")
	}
	return q.Int64(), nil
}
