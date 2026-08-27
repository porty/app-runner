// Package testutil holds helpers shared by App Runner's test packages.
package testutil

import (
	"bytes"
	"log/slog"
	"testing"
)

// CaptureLogs redirects the default slog logger to a buffer for the
// duration of the test and returns that buffer.
func CaptureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	buffer := new(bytes.Buffer)
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(buffer, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return buffer
}
