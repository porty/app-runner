package main

import (
	"fmt"
	"os"

	"github.com/roflware/app-runner/internal/server"
)

func main() {
	if err := server.Run(os.Args[1:], productionFrontend()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
