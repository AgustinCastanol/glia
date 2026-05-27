// debug-store is an INTERNAL smoke-testing tool for the glia store
// package. It is NOT the user-facing CLI described in PRD-5.
// Usage:
//
//	debug-store <rootDir> append
//	debug-store <rootDir> read   <canonical_id>
//	debug-store <rootDir> delete <canonical_id>
//	debug-store <rootDir> rebuild
//	debug-store <rootDir> dump
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/agustincastanol/glia/internal/store"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: debug-store <rootDir> <subcommand> [args...]")
		os.Exit(1)
	}

	rootDir := os.Args[1]
	cmd := os.Args[2]

	s, err := store.Open(rootDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open: %v\n", err)
		os.Exit(1)
	}
	defer s.Close()

	switch cmd {
	case "append":
		now := time.Now().UTC().Format(time.RFC3339Nano)
		r, err := s.Append(store.CanonicalRecord{
			Kind:          "observation",
			Title:         "debug-store test record",
			Content:       "appended via debug-store at " + now,
			ContentFormat: "text",
			CreatedAt:     now,
			UpdatedAt:     now,
			Tags:          []string{"debug"},
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "append: %v\n", err)
			os.Exit(1)
		}
		printJSON(r)

	case "read":
		if len(os.Args) < 4 {
			fmt.Fprintln(os.Stderr, "read requires <canonical_id>")
			os.Exit(1)
		}
		r, err := s.ReadLive(os.Args[3])
		if err != nil {
			fmt.Fprintf(os.Stderr, "read: %v\n", err)
			os.Exit(1)
		}
		printJSON(r)

	case "delete":
		if len(os.Args) < 4 {
			fmt.Fprintln(os.Stderr, "delete requires <canonical_id>")
			os.Exit(1)
		}
		id := os.Args[3]
		now := time.Now().UTC().Format(time.RFC3339Nano)
		tomb, err := s.Append(store.CanonicalRecord{
			CanonicalID: id,
			Kind:        "observation",
			Deleted:     true,
			UpdatedAt:   now,
			CreatedAt:   now,
			Tags:        []string{},
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "delete: %v\n", err)
			os.Exit(1)
		}
		printJSON(tomb)

	case "rebuild":
		if err := s.Rebuild(); err != nil {
			fmt.Fprintf(os.Stderr, "rebuild: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("rebuild complete")

	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n", cmd)
		os.Exit(1)
	}
}

func printJSON(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		fmt.Fprintf(os.Stderr, "json: %v\n", err)
	}
}
