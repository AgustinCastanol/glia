package config

import (
	"testing"
)

// TestCompareVersion covers REQ-CFG-04 semver-lite comparison scenarios.
func TestCompareVersion(t *testing.T) {
	cases := []struct {
		name string
		a, b string
		want int
	}{
		// equal
		{"equal plain", "1.0.0", "1.0.0", 0},
		{"equal with v prefix", "v1.2.3", "v1.2.3", 0},
		{"equal mixed prefix", "v1.2.3", "1.2.3", 0},
		// less
		{"a < b major", "v0.1.0", "v1.0.0", -1},
		{"a < b minor", "v1.0.0", "v1.1.0", -1},
		{"a < b patch", "v1.0.0", "v1.0.1", -1},
		// greater
		{"a > b major", "v2.0.0", "v1.9.9", 1},
		{"a > b minor", "v1.2.0", "v1.1.9", 1},
		{"a > b patch", "v1.0.2", "v1.0.1", 1},
		// dev
		{"dev > anything", "dev", "v9.9.9", 1},
		{"anything < dev", "v9.9.9", "dev", -1},
		{"dev == dev", "dev", "dev", 0},
		// missing parts default to 0
		{"missing patch same", "v1.0", "v1.0.0", 0},
		{"missing minor and patch same", "v1", "v1.0.0", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := CompareVersion(tc.a, tc.b)
			if got != tc.want {
				t.Errorf("CompareVersion(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

// TestRefuse covers REQ-CFG-04 version-refusal scenarios.
func TestRefuse(t *testing.T) {
	t.Run("REQ-CFG-04: min_version exceeded returns error", func(t *testing.T) {
		err := Refuse("v0.1.0", "v9.0.0")
		if err == nil {
			t.Fatal("expected error when storeMin > binaryVersion, got nil")
		}
	})

	t.Run("REQ-CFG-04: empty storeMin is permissive", func(t *testing.T) {
		if err := Refuse("v0.1.0", ""); err != nil {
			t.Fatalf("expected nil for empty storeMin, got: %v", err)
		}
	})

	t.Run("REQ-CFG-04: dev binary always passes", func(t *testing.T) {
		if err := Refuse("dev", "v9.0.0"); err != nil {
			t.Fatalf("expected nil for dev binary, got: %v", err)
		}
	})

	t.Run("same version passes", func(t *testing.T) {
		if err := Refuse("v1.0.0", "v1.0.0"); err != nil {
			t.Fatalf("expected nil when versions equal, got: %v", err)
		}
	})

	t.Run("error message contains 'upgrade glia'", func(t *testing.T) {
		err := Refuse("v0.1.0", "v9.0.0")
		if err == nil {
			t.Fatal("expected error")
		}
		msg := err.Error()
		for _, substr := range []string{"upgrade glia"} {
			if !containsCI(msg, substr) {
				t.Errorf("error message %q does not contain %q", msg, substr)
			}
		}
	})
}

func containsCI(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr ||
		len(s) > 0 && containsRaw(lower(s), lower(substr)))
}

func lower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}

func containsRaw(s, sub string) bool {
	if sub == "" {
		return true
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
