package main

import (
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
)

func main() {
	address := flag.String("listen", ":8080", "HTTP listen address")
	flag.Parse()

	server := &http.Server{
		Addr:    *address,
		Handler: newHTTPHandler(productionFrontend()),
	}

	slog.Info("App Runner listening", "address", *address)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
