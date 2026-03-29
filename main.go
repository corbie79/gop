package main

import (
	"os"

	"github.com/corbie79/gop/cmd"
)

func main() {
	if err := cmd.Execute(); err != nil {
		os.Exit(1)
	}
}
