package cmd

import (
	"errors"
	"fmt"
	"io/fs"

	"github.com/spf13/cobra"

	"github.com/agustincastanol/wrapper-mems/internal/config"
	"github.com/agustincastanol/wrapper-mems/internal/store"
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

// enforceMinVersion reads schema.json from storeDir and refuses to proceed if
// its wrapper_mems_min_version exceeds the binary Version (REQ-CFG-04). A
// missing schema.json or empty min_version is permissive (returns nil).
func enforceMinVersion(storeDir string) error {
	info, err := store.ReadSchema(storeDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read schema: %w", err)
	}
	return config.Refuse(Version, info.WrapperMemsMinVersion)
}
