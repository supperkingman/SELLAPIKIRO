# Build sidecar keyadmin - Go stdlib, khong phu thuoc ngoai.
FROM --platform=$BUILDPLATFORM golang:1.23-alpine AS builder
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod ./
COPY main.go ./
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} \
    go build -ldflags="-s -w" -o /keyadmin .

FROM alpine:3.20
COPY --from=builder /keyadmin /usr/local/bin/keyadmin
EXPOSE 8082
ENTRYPOINT ["/usr/local/bin/keyadmin"]
