# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS build
ARG TARGETOS TARGETARCH
ARG VERSION=dev
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY web ./web
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -ldflags "-s -w -X main.version=${VERSION}" -o /sharetunnel ./cmd/sharetunnel

FROM cloudflare/cloudflared:latest AS cf

FROM alpine:3.20
LABEL org.opencontainers.image.title="ShareTunnel" \
      org.opencontainers.image.description="Share Nextcloud files with anyone through a temporary public link. No domain, no port forwarding." \
      org.opencontainers.image.source="https://github.com/Shariqtechie/Sharetunnel" \
      org.opencontainers.image.licenses="MIT"
RUN apk add --no-cache docker-cli ca-certificates tzdata
COPY --from=cf /usr/local/bin/cloudflared /usr/local/bin/cloudflared
COPY --from=build /sharetunnel /usr/local/bin/sharetunnel
ENV DATA_DIR=/data
VOLUME /data
HEALTHCHECK --interval=30s --timeout=3s --start-period=10s CMD wget -qO- "http://127.0.0.1:${PANEL_PORT:-9099}/healthz" >/dev/null || exit 1
ENTRYPOINT ["sharetunnel"]
CMD ["serve"]
