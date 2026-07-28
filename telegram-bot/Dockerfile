# Build sidecar telegram-bot - Go stdlib, khong phu thuoc ngoai.
FROM --platform=$BUILDPLATFORM golang:1.23-alpine AS builder
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod ./
COPY *.go ./
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} \
    go build -ldflags="-s -w" -o /telegram-bot .

FROM alpine:3.20
# Unlike keyadmin, this service calls api.telegram.org over HTTPS, so it needs the
# root certificates. Without them every request fails with a certificate error.
RUN apk add --no-cache ca-certificates
# Runs as a non-root user: the bot holds tokens that grant key management, so it
# should not also have root inside its container.
RUN adduser -D -u 10001 botuser
USER botuser
# No EXPOSE: the bot long-polls outbound to Telegram and never listens for
# connections, so it needs no inbound port at all.
ENTRYPOINT ["/telegram-bot"]
