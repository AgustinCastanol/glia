package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

// Version is the binary version string. The default "dev" is used for local
// builds. Release pipelines override it at link time via:
//
//	go build -ldflags "-X github.com/agustincastanol/wrapper-mems/cmd/wrapper-mems/cmd.Version=v0.1.0"
var Version = "dev"

// SchemaVersionRange documents which canonical store schema versions this
// binary can read and write.
const SchemaVersionRange = "v1"

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the binary version and supported schema range",
	Long: `version prints the wrapper-mems binary version and the canonical store
schema version range this build can read and write.

Exit code: 0 (always).`,
	Args: cobra.NoArgs,
	Run:  runVersion,
}

func init() {
	rootCmd.AddCommand(versionCmd)
}

func runVersion(cmd *cobra.Command, _ []string) {
	fmt.Fprintf(cmd.OutOrStdout(),
		"wrapper-mems %s (schema %s)\n", Version, SchemaVersionRange)
}
