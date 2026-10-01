##############################################################
## Stage 1 - Go Build
##############################################################
#
# The build context is the repository root, which is also where this file lives:
#   docker build -t centipedetest/cm-centipede:0.0.0 .
#
# The server imports the nested modules github.com/cloud-barista/cm-centipede/dmdl
# and .../transx-ex, which resolve through the local-path replace directives in
# go.mod. Both directories must therefore be present in the build context.

# Keep this in sync with the go directive in go.mod.
FROM golang:1.26.3-alpine AS builder

WORKDIR /src

# The module graph goes in first, on its own layer, so that `go mod download`
# is re-run only when a dependency changes rather than on every source edit.
# The replaced modules need their go.mod present for the graph to load; dmdl
# has no external requires and so has no go.sum.
COPY go.mod go.sum ./
COPY dmdl/go.mod ./dmdl/
COPY transx-ex/go.mod transx-ex/go.sum ./transx-ex/

RUN --mount=type=cache,target=/go/pkg/mod \
	go mod download

# Only what ./cmd/cm-centipede actually pulls in. conf/ is not needed here —
# stage 2 copies it straight from the build context.
COPY cmd/ ./cmd/
COPY pkg/ ./pkg/
COPY dmdl/ ./dmdl/
COPY transx-ex/ ./transx-ex/

# CGO_ENABLED=0: the SQLite driver (glebarez/sqlite) is pure Go, so the binary
# needs no C toolchain and stays static.
RUN --mount=type=cache,target=/go/pkg/mod \
	--mount=type=cache,target=/root/.cache/go-build \
	CGO_ENABLED=0 go build -ldflags '-s -w' -o /out/cm-centipede ./cmd/cm-centipede

##############################################################
## Stage 2 - Application Setup
##############################################################

FROM alpine:3.21 AS prod

# rsync + openssh-client: transx-ex shells out to `rsync -e ssh` for filesystem
#   migrations that run through this container.
# curl: used by the container healthcheck against /centipede/readyz.
RUN apk add --no-cache ca-certificates tzdata rsync openssh-client curl

WORKDIR /app

COPY --from=builder /out/cm-centipede .
COPY conf/ ./conf/

# The server reads ./conf/cm-centipede.yaml relative to this workdir; every
# value can be overridden with the matching CENTIPEDE_* environment variable.
# The listen port comes from centipede.self.endpoint (CENTIPEDE_SELF_ENDPOINT).
EXPOSE 8085

ENTRYPOINT ["./cm-centipede"]
