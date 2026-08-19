# Milestone 1

Stand up the frontend and backend.

## Backend

- written in Go and built as a single binary
- exposes dummy methods over a Twirp RPC interface to verify end-to-end communication
- embeds and serves the compiled production frontend, including SPA route fallback

## Frontend

- built with Vite, TypeScript, React, and MUI
- uses client-side routing; the browser URL reflects navigation state without full page loads
- includes a persistent dark mode toggle
- includes a collapsible sidebar with icons and a two-level hierarchy; relevant or dummy entries are acceptable
- calls the dummy backend RPC methods and displays their results

## Development and production workflows

- development runs the Go backend and Vite development server separately
- Vite provides frontend hot module replacement and proxies Twirp requests to the Go backend
- production builds the frontend before compiling the Go binary so the frontend resources are embedded in that binary
- generated RPC source is committed so users can build without installing protobuf generation tools

## Completion criteria

- backend and frontend unit tests pass
- a production build produces one executable containing the frontend
- directly visiting or refreshing a client-side route works when served by the production executable
- the frontend can successfully invoke a dummy Twirp method in both development and production
