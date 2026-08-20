package main

import (
	"bytes"
	"log/slog"
	"testing"
)

func captureDefaultLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	buffer := new(bytes.Buffer)
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(buffer, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return buffer
}
