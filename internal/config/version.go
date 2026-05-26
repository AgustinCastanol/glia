package config

import (
	"fmt"
	"strconv"
	"strings"
)

// CompareVersion compares two semver strings of the form vMAJOR.MINOR.PATCH or
// MAJOR.MINOR.PATCH. Leading "v" is stripped before parsing. Missing parts
// default to 0 (so "v1" == "v1.0.0").
//
// The special string "dev" is treated as infinitely high — a dev build always
// satisfies any minimum version requirement.
//
// Returns -1 if a < b, 0 if a == b, 1 if a > b.
func CompareVersion(a, b string) int {
	if a == "dev" && b == "dev" {
		return 0
	}
	if a == "dev" {
		return 1
	}
	if b == "dev" {
		return -1
	}
	aParts := parseSemver(a)
	bParts := parseSemver(b)
	for i := 0; i < 3; i++ {
		if aParts[i] < bParts[i] {
			return -1
		}
		if aParts[i] > bParts[i] {
			return 1
		}
	}
	return 0
}

// Refuse returns a non-nil error if storeMin > binaryVersion. An empty or
// missing storeMin is permissive (returns nil). This is the version-refusal
// guard described in REQ-CFG-04 and design §8.
func Refuse(binaryVersion, storeMin string) error {
	if storeMin == "" {
		return nil
	}
	if CompareVersion(storeMin, binaryVersion) > 0 {
		return fmt.Errorf(
			"this store requires wrapper-mems >= %s (binary is %s): please upgrade wrapper-mems",
			storeMin, binaryVersion,
		)
	}
	return nil
}

// parseSemver splits a version string into a [3]int array [major, minor, patch].
// Leading "v" is stripped. Missing or non-numeric parts default to 0.
func parseSemver(v string) [3]int {
	v = strings.TrimPrefix(v, "v")
	parts := strings.SplitN(v, ".", 3)
	var result [3]int
	for i := 0; i < len(parts) && i < 3; i++ {
		n, err := strconv.Atoi(parts[i])
		if err == nil {
			result[i] = n
		}
	}
	return result
}
