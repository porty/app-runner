# Repository guidance

- Define application control APIs in `proto/apprunner/v1/app_runner.proto` and expose them through the existing Twirp service pattern. Use a direct HTTP endpoint only when the payload is unsuitable for a unary RPC, such as a large streaming file upload.
- Never clear an operation error from a background refresh; errors must remain visible until the user dismisses them or begins another operation.
- `cmd/argui` is the Qt 6 desktop client (Go bindings from `github.com/mappu/miqt`). It needs cgo, the Qt 6 development packages, and QTermWidget 6 (`libqtermwidget6-2-dev` on Debian/Ubuntu), so it is built with `make argui` rather than `make build`; `go test ./...` also compiles it. `bin/argui -screenshot FILE` (optionally with `-view PLACE` and `ARGUI_OPEN=wizard|settings:NAME|console:NAME|bridge:NAME|upload`) captures a window headlessly with `QT_QPA_PLATFORM=offscreen` for checking layouts.
