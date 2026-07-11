# Build storefront (Go, SQLite thuan Go -> khong can CGO).
FROM golang:1.22-alpine AS build
WORKDIR /app
COPY go.mod ./
RUN go mod download 2>/dev/null || true
COPY . .
RUN go mod tidy && CGO_ENABLED=0 go build -ldflags="-s -w" -o storefront .

FROM alpine:3.20
RUN apk add --no-cache ca-certificates && adduser -D -u 10001 app
WORKDIR /app
COPY --from=build /app/storefront /app/storefront
COPY --from=build /app/web /app/web
# Tao san thu muc data voi quyen cua user app (SQLite se ghi vao day).
RUN mkdir -p /app/data && chown -R app:app /app/data
USER app
VOLUME ["/app/data"]
EXPOSE 8083
ENTRYPOINT ["/app/storefront"]
