# Build sidecar keycheck - Go stdlib, khong phu thuoc ngoai.
FROM --platform=$BUILDPLATFORM golang:1.23-alpine AS builder
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod ./
COPY main.go ./
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} \
    go build -ldflags="-s -w" -o /keycheck .

# Runtime toi gian.
# LUU Y: chay bang root la CO Y. kiro-go ghi config.json voi quyen 0600 (chi root
# doc duoc), nen sidecar phai la root moi doc duoc de xac thuc key + lay usage.
# Tradeoff chap nhan duoc vi: mount config READ-ONLY, chi bind 127.0.0.1 (sau Nginx),
# server chi dung thu vien chuan, khong phu thuoc ngoai -> be mat tan cong toi thieu.
FROM alpine:3.20
COPY --from=builder /keycheck /usr/local/bin/keycheck
EXPOSE 8081
ENTRYPOINT ["/usr/local/bin/keycheck"]
