module github.com/roflware/app-runner

go 1.25.0

tool github.com/twitchtv/twirp/protoc-gen-twirp

require (
	github.com/coder/websocket v1.8.15
	github.com/twitchtv/twirp v8.1.3+incompatible
	github.com/vishvananda/netlink v1.3.1
	golang.org/x/sys v0.10.0
	google.golang.org/protobuf v1.36.12
)

require (
	github.com/pkg/errors v0.9.1 // indirect
	github.com/stretchr/testify v1.12.0 // indirect
	github.com/vishvananda/netns v0.0.5 // indirect
)
