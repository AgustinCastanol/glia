package main

import (
	"os"

	"github.com/agustincastanol/wrapper-mems/cmd/wrapper-mems/cmd"
)

func main() {
	if err := cmd.Execute(); err != nil {
		os.Exit(1)
	}
}
