package config

// ResolveProject returns the effective project name following the strict
// priority order defined in PRD-6:
//
//  1. --project CLI flag (cliFlag) — highest priority, overrides all
//  2. providers.<x>.project (providerOverride) — per-provider override
//  3. Config.Project (global) — global fallback
//
// An absent or empty value at any level is treated identically — it is skipped
// and the next level is tried. If all three levels are empty, "" is returned.
//
// This function is pure: no side effects, no I/O, no imports beyond the
// standard library. Callers in the wiring layer invoke it once per provider
// at construction time and store the resolved value on the adapter Config.
func ResolveProject(cliFlag, providerOverride, global string) string {
	if cliFlag != "" {
		return cliFlag
	}
	if providerOverride != "" {
		return providerOverride
	}
	return global
}
