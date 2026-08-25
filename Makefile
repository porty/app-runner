.PHONY: build dev generate test

build:
	npm --prefix frontend ci
	npm --prefix frontend run build
	rm -rf cmd/app-runner/frontend/dist
	mkdir -p cmd/app-runner/frontend
	cp -R frontend/dist cmd/app-runner/frontend/dist
	mkdir -p bin
	CGO_ENABLED=0 go build -tags production -o bin/app-runner ./cmd/app-runner
	CGO_ENABLED=0 go build -o bin/arcli ./cmd/arcli

dev:
	npm --prefix frontend install
	npm --prefix frontend run dev:all

sudodev:
	npm --prefix frontend run build
	rm -rf cmd/app-runner/frontend/dist
	mkdir -p cmd/app-runner/frontend
	cp -R frontend/dist cmd/app-runner/frontend/dist
	mkdir -p bin
	CGO_ENABLED=0 go build -tags production -o bin/app-runner ./cmd/app-runner
	CGO_ENABLED=0 go build -o bin/arcli ./cmd/arcli
	sudo setcap cap_net_admin,cap_net_bind_service,cap_net_raw=+ep bin/app-runner
	npm --prefix frontend run sudodev:all

generate:
	buf lint
	buf generate

test:
	go test ./...
	npm --prefix frontend test
	npm --prefix frontend run typecheck
