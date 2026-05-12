package cmd

import "testing"

func TestHumanBytes(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1023, "1023 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{1048576, "1.0 MB"},
		{1572864, "1.5 MB"}, // 1.5 * 1024 * 1024
		{5 * 1024 * 1024 * 1024, "5.0 GB"},
	}
	for _, c := range cases {
		if got := humanBytes(c.in); got != c.want {
			t.Errorf("humanBytes(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestDurationHMS(t *testing.T) {
	cases := []struct {
		in   int
		want string
	}{
		{0, "0:00:00"},
		{5, "0:00:05"},
		{65, "0:01:05"},
		{3599, "0:59:59"},
		{3661, "1:01:01"},
		{-3, "0:00:00"},
	}
	for _, c := range cases {
		if got := durationHMS(c.in); got != c.want {
			t.Errorf("durationHMS(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}
