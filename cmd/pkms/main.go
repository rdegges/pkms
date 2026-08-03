package main

import (
	"os"

	"github.com/rdegges/pkms/internal/cli"
)

func main() {
	os.Exit(cli.Execute())
}
