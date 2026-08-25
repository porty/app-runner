package main

import (
	"fmt"
	"os"

	"github.com/roflware/app-runner"
)

func main() {
	if err := apprunner.RunServer(os.Args[1:], productionFrontend()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
