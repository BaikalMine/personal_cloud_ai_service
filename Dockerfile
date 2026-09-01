FROM golang:1.26.6-alpine@sha256:3889b425f035be855a72fb4755265311293b6d414521f0a519d819df32222d83 AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -buildvcs=false -ldflags="-s -w" -o /out/gateway ./cmd/gateway

FROM alpine:3.23@sha256:fd791d74b68913cbb027c6546007b3f0d3bc45125f797758156952bc2d6daf40
RUN adduser -D -H -u 10001 appuser \
    && mkdir -p /var/lib/gateway-spool \
    && chown appuser:appuser /var/lib/gateway-spool
WORKDIR /app
COPY --from=build /out/gateway /app/gateway
USER appuser
EXPOSE 8090 8091
STOPSIGNAL SIGTERM
ENTRYPOINT ["/app/gateway"]
