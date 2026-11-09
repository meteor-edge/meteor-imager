VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
GOFLAGS_RELEASE = -trimpath -tags fat_noexfat -ldflags "-s -w -X main.version=$(VERSION)"
PLATFORMS = linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64

.PHONY: test vet build cross licenses clean run

test:
	go test ./...
	go test -tags fat_noexfat ./internal/bootfs ./internal/flash

vet:
	gofmt -l cmd internal | (! grep .)
	go vet ./...
	GOOS=darwin go vet ./...
	GOOS=windows go vet ./...

build:
	CGO_ENABLED=0 go build $(GOFLAGS_RELEASE) -o dist/meteor-imager ./cmd/meteor-imager

run:
	go run ./cmd/meteor-imager

licenses:
	scripts/third-party-licenses.sh > dist/THIRD_PARTY_LICENSES.txt

# dist/<os>_<arch>/meteor-imager[.exe] plus THIRD_PARTY_LICENSES.txt
cross:
	@mkdir -p dist
	scripts/third-party-licenses.sh > dist/THIRD_PARTY_LICENSES.txt
	@set -e; for p in $(PLATFORMS); do \
	  os=$${p%/*}; arch=$${p#*/}; ext=; [ $$os = windows ] && ext=.exe; \
	  echo "build $$os/$$arch"; \
	  CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build $(GOFLAGS_RELEASE) -o dist/$${os}_$${arch}/meteor-imager$$ext ./cmd/meteor-imager; \
	  cp dist/THIRD_PARTY_LICENSES.txt dist/$${os}_$${arch}/; \
	done

clean:
	rm -rf dist
