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
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/ergoz-agent /ergoz-agent
COPY --from=build /out/ergoz-collector /ergoz-collector
USER 65532:65532
ENTRYPOINT ["/ergoz-agent"]
