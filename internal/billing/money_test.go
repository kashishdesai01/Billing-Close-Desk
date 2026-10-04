package billing

import (
	"fmt"
	"testing"
)

func TestCPMExactRounding(t *testing.T) {
	for _, tt := range []struct {
		q    int64
		r    string
		want int64
	}{{1, "5", 1}, {1, "4.999999", 0}, {1, "15", 2}, {1000, "12.3456", 1235}, {1450000, "12.3456", 1790112}, {1000000000000, "0.000001", 100000}} {
		got, e := Price(tt.q, tt.r)
		if e != nil || got != tt.want {
			t.Fatalf("%+v: got %d %v", tt, got, e)
		}
	}
}
func TestInvalidMoneyInputs(t *testing.T) {
	for _, r := range []string{"-1", "NaN", "Infinity", "1e4", "0", "0.0000001", "1.2345678", "999999999999999"} {
		if _, e := Price(1000, r); e == nil {
			t.Errorf("accepted %q", r)
		}
	}
	if _, e := Price(-1, "1"); e == nil {
		t.Fatal("negative quantity")
	}
	if _, e := Price(1000000000000, "999999999999"); e == nil {
		t.Fatal("overflow not rejected")
	}
}
func FuzzCPMDeterministic(f *testing.F) {
	f.Add(uint64(1450000), uint32(12345600))
	f.Fuzz(func(t *testing.T, q uint64, r uint32) {
		q %= 1000000000001
		if r == 0 {
			return
		}
		rate := newRate(r)
		a, e := Price(int64(q), rate)
		b, e2 := Price(int64(q), rate)
		if (e == nil) != (e2 == nil) || a != b {
			t.Fatal("nondeterministic")
		}
	})
}
func newRate(n uint32) string { return fmt.Sprintf("%d.%06d", n/1000000, n%1000000) }
