# syntax=docker/dockerfile:1

FROM golang:1.24-alpine AS builder

WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal

ARG TARGETOS=linux
ARG TARGETARCH=amd64

RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/github-release-cache ./cmd/github-release-cache && \
    mkdir -p /out/data /out/config

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=builder /out/github-release-cache /github-release-cache
COPY --from=builder --chown=65532:65532 /out/data /data
COPY --from=builder --chown=65532:65532 /out/config /config

ENV LISTEN_ADDR=":8080" \
    DATA_DIR="/data"

VOLUME ["/data"]
EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD ["/github-release-cache", "-healthcheck"]

USER 65532:65532
ENTRYPOINT ["/github-release-cache"]

LABEL org.opencontainers.image.source="https://github.com/timcappalli/github-release-cache"
LABEL org.opencontainers.image.version="1.0.0"