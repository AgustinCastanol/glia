package main

import (
	"os"

	"github.com/agustincastanol/glia/cmd/glia/cmd"
)

func main() {
	if err := cmd.Execute(); err != nil {
		os.Exit(cmd.ExitCode(err))
	}
}
