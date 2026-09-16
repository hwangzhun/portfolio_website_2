FROM golang:1.26-alpine AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w -buildid=" -o /out/portfolio . && \
    mkdir -p /out/data/uploads /out/data/tmp

FROM scratch

ENV NODE_ENV=production \
    API_PORT=8787 \
    DATA_DIR=/app/data \
    UPLOAD_DIR=/app/data/uploads \
    TMPDIR=/app/data/tmp

WORKDIR /app
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY docker/passwd /etc/passwd
COPY docker/group /etc/group
COPY --from=builder --chown=1000:1000 /out/data /app/data
COPY --from=builder --chown=1000:1000 /out/portfolio /app/portfolio

USER 1000:1000
EXPOSE 8787
VOLUME ["/app/data"]
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 CMD ["/app/portfolio", "healthcheck"]
ENTRYPOINT ["/app/portfolio"]
