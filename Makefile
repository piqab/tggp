BINARY  = mtproxy
CMD     = ./cmd/mtproxy
LDFLAGS = -s -w

.PHONY: build run linux clean secret

## build: compile for the current platform
build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) $(CMD)

## linux: cross-compile a static Linux amd64 binary
linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
		go build -ldflags "$(LDFLAGS)" -o $(BINARY)-linux-amd64 $(CMD)

## run: run with .env file (requires `source .env` or use direnv)
run: build
	./$(BINARY)

## secret: generate a random dd-secret ready to use
secret:
	@printf "dd%s\n" $$(openssl rand -hex 16)

## clean:
clean:
	rm -f $(BINARY) $(BINARY)-linux-amd64
