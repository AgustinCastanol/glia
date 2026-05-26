// Package identity resolves the origin.author value used in canonical records.
// It is intentionally stdlib-only with zero internal dependencies so that both
// internal/config and the adapter packages can import it without creating import
// cycles (design ADR-D3: no adapter→config import edge).
package identity

import (
	"os"
	"os/user"
)

// Default returns the fallback author string in the form "hostname:username".
// If either component cannot be determined, a safe substitute is used.
//
// Implements REQ-IDN-01 step 3: Default = <hostname>:<USER>.
func Default() string {
	host, _ := os.Hostname()
	if host == "" {
		host = "localhost"
	}

	u, err := user.Current()
	if err == nil && u.Username != "" {
		return host + ":" + u.Username
	}

	// user.Current() failed — fall back to the USER env var.
	if envUser := os.Getenv("USER"); envUser != "" {
		return host + ":" + envUser
	}

	return host + ":unknown"
}

// Resolve returns the effective author string by applying the precedence order
// defined in REQ-IDN-01:
//  1. WRAPPER_MEMS_AUTHOR env var (non-empty).
//  2. userCfgAuthor parameter (the value of IdentityConfig.Author from the user config).
//  3. Default() — hostname:username.
//
// Callers pass cfg.Identity.Author (or "") to avoid importing internal/config.
func Resolve(userCfgAuthor string) string {
	if v := os.Getenv("WRAPPER_MEMS_AUTHOR"); v != "" {
		return v
	}
	if userCfgAuthor != "" {
		return userCfgAuthor
	}
	return Default()
}
