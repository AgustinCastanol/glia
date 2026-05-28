package adapter_test

import (
	"github.com/agustincastanol/glia/internal/adapter"
	"github.com/agustincastanol/glia/internal/adapter/claudemem"
	"github.com/agustincastanol/glia/internal/adapter/engram"
)

// ---------------------------------------------------------------------------
// Phase 2 — WriteCapability compile-time interface assertions (REQ-CMW-03)
// ---------------------------------------------------------------------------

// The Adapter interface must include WriteCapability() string.
// These variables enforce the contract at compile time — if the method is
// missing the build fails immediately (no runtime test needed for interface shape).

var _ interface{ WriteCapability() string } = (*adapter.WriteCapabilityAdapter)(nil)

var _ adapter.Adapter = (*engram.EngramAdapter)(nil)
var _ adapter.Adapter = (*claudemem.ClaudeMemAdapter)(nil)

// Ensure both adapters expose WriteCapability via the interface.
var _ interface{ WriteCapability() string } = (*engram.EngramAdapter)(nil)
var _ interface{ WriteCapability() string } = (*claudemem.ClaudeMemAdapter)(nil)
