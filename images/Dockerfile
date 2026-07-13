FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETOS TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} \
    go build -trimpath -ldflags="-s -w" -o /out/ ./cmd/...

# One image, two entrypoints: the DaemonSet runs /ergoz-agent, the collector
# Deployment overrides command to /ergoz-collector.
#
# base, NOT static: the agent imports purego, whose //go:cgo_import_dynamic
# dlopen directive forces a dynamically linked binary (interpreter
# /lib64/ld-linux-x86-64.so.2) even under CGO_ENABLED=0. distroless/static
# has no ld.so, so the agent fails with "exec: no such file or directory".
# base-debian12 ships glibc + the loader and stays non-root and minimal.
FROM gcr.io/distroless/base-debian12:nonroot
COPY --from=build /out/ergoz-agent /ergoz-agent
COPY --from=build /out/ergoz-collector /ergoz-collector
USER 65532:65532
ENTRYPOINT ["/ergoz-agent"]
