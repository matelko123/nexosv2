package version

import "testing"

func TestNewer(t *testing.T) {
	cases := []struct {
		cand, cur string
		want      bool
	}{
		{"v1.2.0", "v1.1.9", true},
		{"v1.10.0", "v1.9.0", true},
		{"v1.2.0", "v1.2.0", false},
		{"v1.1.0", "v1.2.0", false},
		{"v1.2.0", "dev", true},
		{"v1.2.0", "v1.2.0-rc.1", true},
		{"v1.2.0-rc.2", "v1.2.0-rc.1", true},
		{"v1.2.0-rc.1", "v1.2.0", false},
		{"garbage", "v1.0.0", false},
		{"2.0.0", "v1.0.0", true},
	}
	for _, c := range cases {
		if got := Newer(c.cand, c.cur); got != c.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", c.cand, c.cur, got, c.want)
		}
	}
}
