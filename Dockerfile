FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY *.go ./
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -ldflags "-s -w -X main.version=${VERSION}" -o /sharetunnel .

FROM cloudflare/cloudflared:latest AS cf

FROM alpine:3.20
RUN apk add --no-cache docker-cli ca-certificates
COPY --from=cf /usr/local/bin/cloudflared /usr/local/bin/cloudflared
COPY --from=build /sharetunnel /usr/local/bin/sharetunnel
ENTRYPOINT ["sharetunnel"]
CMD ["run"]
