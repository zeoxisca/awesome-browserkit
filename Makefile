.PHONY: install test race smoke build demo pack cli server
install:
	go mod download
	npm ci --prefix react
	npm run build --prefix react
	npm ci --prefix examples/demo/web

test:
	go test ./...
	npm test --prefix react

race:
	go test -race ./...

smoke:
	go test -tags=browser_smoke . ./cmd/browserkit -run 'TestRealBrowserManagerWorkflow|TestRealCLIModes' -count=1 -v

build:
	go build ./...
	npm run build --prefix react
	npm run build --prefix examples/demo/web

cli:
	go build -o browserkit ./cmd/browserkit

server:
	go build -o browserkit-server ./cmd/browserkit-server

demo:
	go run ./examples/demo

pack:
	cd react && npm pack
