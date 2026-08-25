# Repository guidance

- Define application control APIs in `proto/apprunner/v1/app_runner.proto` and expose them through the existing Twirp service pattern. Use a direct HTTP endpoint only when the payload is unsuitable for a unary RPC, such as a large streaming file upload.
- Never clear an operation error from a background refresh; errors must remain visible until the user dismisses them or begins another operation.
