package main

import (
	"os"

	"nubilo/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:]))
}
