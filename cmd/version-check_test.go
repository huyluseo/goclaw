package cmd

import "testing"

func TestNormalizeVersion(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"v2.4.7", "2.4.7"},
		{"v2.4.7-dirty", "2.4.7"},
		{"v1.0.0-g1a2b3c4", "1.0.0"},
		{"2.4.7", "2.4.7"},
		{"dev", "dev"},
	}
	for _, tt := range tests {
		if got := normalizeVersion(tt.input); got != tt.want {
			t.Errorf("normalizeVersion(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"2.4.7", "2.4.7", 0},
		{"2.4.7", "2.5.0", -1},
		{"2.5.0", "2.4.7", 1},
		{"1.0.0", "2.0.0", -1},
		{"2.4.7", "2.4.8", -1},
		{"10.0.0", "9.9.9", 1},
		{"2.4", "2.4.0", 0},
	}
	for _, tt := range tests {
		if got := compareVersions(tt.a, tt.b); got != tt.want {
			t.Errorf("compareVersions(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}
