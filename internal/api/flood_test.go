package api

import "testing"

func TestPublicConcurrencyLimit(t *testing.T) {
	cases := []struct {
		max, reserved, want int64
	}{
		{4096, 1024, 3072},
		{4, 3, 1},
		{4, 0, 4},
		{4, 4, 1},
		{4, 100, 1},
		{1, 1, 1},
	}
	for _, tc := range cases {
		got := publicConcurrencyLimit(tc.max, tc.reserved)
		if got != tc.want {
			t.Fatalf("max=%d reserved=%d want %d got %d", tc.max, tc.reserved, tc.want, got)
		}
	}
}
