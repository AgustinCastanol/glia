package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

var showFlags struct {
	kind   string
	typ    string
	asJSON bool
}

var showCmd = &cobra.Command{
	Use:   "show",
	Short: "List canonical records in the store",
	Long: `show renders all live (non-deleted) canonical records. By default it prints a
human-readable table. Use --json to get JSONL output for piping (REQ-SE-12..14).

Filters --kind and --type are OR-ed independently (i.e. all records matching
either the kind or the type are shown when both flags are given — within a single
flag value the filter is exact-match).`,
	Args: cobra.NoArgs,
	Run:  runShow,
}

func init() {
	showCmd.Flags().StringVar(&showFlags.kind, "kind", "",
		"filter by kind (observation, session_summary, relation)")
	showCmd.Flags().StringVar(&showFlags.typ, "type", "",
		"filter by type field")
	showCmd.Flags().BoolVar(&showFlags.asJSON, "json", false,
		"emit JSONL (one full canonical record per line)")
	rootCmd.AddCommand(showCmd)
}

func runShow(cmd *cobra.Command, _ []string) {
	dir, err := projectDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "show: resolve dir:", err)
		os.Exit(1)
	}

	s, err := requireStore(dir)
	if err != nil {
		os.Exit(1)
	}
	defer s.Close()

	records, err := s.ListLive()
	if err != nil {
		fmt.Fprintln(os.Stderr, "show:", err)
		os.Exit(1)
	}

	// Apply filters (REQ-SE-12).
	filtered := records[:0]
	for _, r := range records {
		if showFlags.kind != "" && r.Kind != showFlags.kind {
			continue
		}
		if showFlags.typ != "" && r.Type != showFlags.typ {
			continue
		}
		filtered = append(filtered, r)
	}

	w := cmd.OutOrStdout()

	if showFlags.asJSON {
		// JSONL output — one full record per line (REQ-SE-13).
		enc := json.NewEncoder(w)
		for _, r := range filtered {
			if err := enc.Encode(r); err != nil {
				fmt.Fprintln(os.Stderr, "show: encode:", err)
				os.Exit(1)
			}
		}
		return
	}

	// Table output (REQ-SE-14).
	// Columns: id(12), kind, type, topic_key, title(60), updated_at, revision
	tw := newTabWriter(w)
	fmt.Fprintln(tw, "ID\tKIND\tTYPE\tTOPIC_KEY\tTITLE\tUPDATED_AT\tREV")
	for _, r := range filtered {
		id := r.CanonicalID
		if len(id) > 12 {
			id = id[:12]
		}
		title := r.Title
		if len(title) > 60 {
			title = title[:57] + "..."
		}
		// Replace tabs inside fields to avoid breaking the table.
		title = strings.ReplaceAll(title, "\t", " ")
		topicKey := strings.ReplaceAll(r.TopicKey, "\t", " ")

		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%d\n",
			id, r.Kind, r.Type, topicKey, title, r.UpdatedAt, r.Revision)
	}
	tw.Flush()
}
