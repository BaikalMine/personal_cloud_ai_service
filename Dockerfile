FROM golang:1.26.5-alpine@sha256:0178a641fbb4858c5f1b48e34bdaabe0350a330a1b1149aabd498d0699ff5fb2 AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -buildvcs=false -ldflags="-s -w" -o /out/gateway ./cmd/gateway

FROM alpine:3.23@sha256:fd791d74b68913cbb027c6546007b3f0d3bc45125f797758156952bc2d6daf40
RUN adduser -D -H -u 10001 appuser
WORKDIR /app
COPY --from=build /out/gateway /app/gateway
USER appuser
EXPOSE 8090 8091
STOPSIGNAL SIGTERM
ENTRYPOINT ["/app/gateway"]
