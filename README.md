# App Runner

App Runner is a web-based control plane for virtual machines and containers. The project currently contains the management shell and its Go/Twirp API foundation.

## Requirements

- Go 1.25 or newer
- Node.js and npm
- Buf, only when regenerating protobuf sources

## Development

Run the backend and Vite development server together:

```sh
make dev
```

The frontend is available at <http://localhost:5173> with hot module replacement. Vite proxies `/twirp` requests to the Go backend at <http://localhost:8080>.

The two processes can also be run independently:

```sh
go run .
npm --prefix frontend run dev
```

## Test

Run backend tests, frontend tests, and TypeScript checking:

```sh
make test
```

## Production build

Build the frontend and embed it in one statically linked Go executable:

```sh
make build
./bin/app-runner
```

The production application is then available at <http://localhost:8080>. Use `-listen` to select another address, for example `./bin/app-runner -listen 127.0.0.1:9000`.

## RPC source generation

Generated Go sources are committed. After changing a protobuf definition, regenerate them with:

```sh
make generate
```

The Twirp generator is pinned as a Go tool dependency, so Buf invokes the repository's declared version.
