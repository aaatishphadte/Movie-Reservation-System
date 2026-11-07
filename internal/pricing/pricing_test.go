package pricing

import "testing"

func TestSeatPrice(t *testing.T) {
	cases := []struct {
		base int
		typ  string
		want int
	}{
		{1200, "standard", 1200},
		{1200, "premium", 1800},
		{999, "premium", 1498}, // integer cents, truncated
		{0, "premium", 0},
	}
	for _, c := range cases {
		if got := SeatPrice(c.base, c.typ); got != c.want {
			t.Errorf("SeatPrice(%d,%q)=%d want %d", c.base, c.typ, got, c.want)
		}
	}
}
