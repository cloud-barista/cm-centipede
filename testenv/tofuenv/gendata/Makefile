# gendata is a standalone Go module, separate from the repository root module.
# GOWORK=off is set so every go command here builds against this module's own
# go.mod, whether or not a go.work exists above it.
export GOWORK=off
export GOFLAGS=-mod=mod

BIN := gendata

.PHONY: build vet tidy run clean

build:
	go build -o $(BIN) .

vet:
	go vet ./...

tidy:
	go mod tidy

run: build
	./$(BIN) $(ARGS)

clean:
	rm -f $(BIN)
