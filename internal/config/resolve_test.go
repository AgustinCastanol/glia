package config

// TestResolveProject covers all precedence cases for ResolveProject.
// Priority: CLI flag > provider override > global fallback > empty.
import "testing"

func TestResolveProject(t *testing.T) {
	tests := []struct {
		name             string
		cliFlag          string
		providerOverride string
		global           string
		want             string
	}{
		{
			name:             "CLI flag wins over everything",
			cliFlag:          "cli-value",
			providerOverride: "eng-specific",
			global:           "global",
			want:             "cli-value",
		},
		{
			name:             "provider override wins when no CLI flag",
			cliFlag:          "",
			providerOverride: "eng-specific",
			global:           "global",
			want:             "eng-specific",
		},
		{
			name:             "global fallback when no CLI flag and no provider override",
			cliFlag:          "",
			providerOverride: "",
			global:           "global",
			want:             "global",
		},
		{
			name:             "all empty returns empty string",
			cliFlag:          "",
			providerOverride: "",
			global:           "",
			want:             "",
		},
		{
			name:             "CLI flag wins even when empty provider and global set",
			cliFlag:          "cli-only",
			providerOverride: "",
			global:           "global",
			want:             "cli-only",
		},
		{
			name:             "provider override wins when CLI flag empty and global set",
			cliFlag:          "",
			providerOverride: "per-provider",
			global:           "",
			want:             "per-provider",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ResolveProject(tc.cliFlag, tc.providerOverride, tc.global)
			if got != tc.want {
				t.Errorf("ResolveProject(%q, %q, %q) = %q, want %q",
					tc.cliFlag, tc.providerOverride, tc.global, got, tc.want)
			}
		})
	}
}
