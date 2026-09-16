FROM golang:1.26-alpine AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    mkdir -p /out/rootfs/app/data/uploads /out/rootfs/app/data/tmp /out/rootfs/usr/local/bin && \
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w -buildid=" -o /out/rootfs/app/portfolio . && \
    ln -s /app/portfolio /out/rootfs/usr/local/bin/docker-entrypoint.sh

FROM scratch

ENV NODE_ENV=production \
    API_PORT=8787 \
    DATA_DIR=/app/data \
    UPLOAD_DIR=/app/data/uploads \
    TMPDIR=/app/data/tmp \
    PATH=/usr/local/bin:/app

WORKDIR /app
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY docker/passwd /etc/passwd
COPY docker/group /etc/group
# 1Panel may preserve the previous Node image entrypoint during an in-place
# upgrade. This is the same static Go executable, not a shell script; legacy
# arguments such as "node server/index.js" are intentionally ignored.
COPY --from=builder --chown=1000:1000 /out/rootfs /

USER 1000:1000
EXPOSE 8787
VOLUME ["/app/data"]
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 CMD ["/app/portfolio", "healthcheck"]
ENTRYPOINT ["/app/portfolio"]
