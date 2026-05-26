package config

// envBinding pairs an environment variable name with a setter that applies its
// value to a *Config.
type envBinding struct {
	name string
	set  func(c *Config, v string)
}

// envBindings is the canonical table of env var overrides (REQ-CFG-03).
//
// NOTE: WRAPPER_MEMS_AUTHOR is NOT bound here — identity.Resolve() reads the
// env var directly so that adapters can call identity.Resolve without holding a
// *Config (design ADR-D3: no adapter→config import edge).
//
// NOTE: WRAPPER_MEMS_CONFIG is resolved by the caller (cmd layer) BEFORE
// invoking Load() to choose userConfigPath; it is not a Config field.
//
// NOTE: NO_COLOR is handled in the cmd/output layer, not in config.
var envBindings = []envBinding{
	{
		name: "WRAPPER_MEMS_PROJECT",
		set:  func(c *Config, v string) { c.Project = v },
	},
	{
		name: "WRAPPER_MEMS_ENGRAM_BIN",
		set:  func(c *Config, v string) { c.Providers.Engram.CLIPath = v },
	},
	{
		name: "WRAPPER_MEMS_CM_BASE_URL",
		set:  func(c *Config, v string) { c.Providers.ClaudeMem.HTTPBaseURL = v },
	},
}

// envOverlay applies each binding from envBindings to cfg when the corresponding
// environment variable is non-empty. env is the lookup function; in production
// pass os.Getenv; in tests pass a closure over t.Setenv'd values.
func envOverlay(c *Config, env func(string) string) {
	for _, b := range envBindings {
		if v := env(b.name); v != "" {
			b.set(c, v)
		}
	}
}
