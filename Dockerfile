FROM golang:1.26.6-alpine@sha256:3889b425f035be855a72fb4755265311293b6d414521f0a519d819df32222d83 AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -trimpath -buildvcs=false -o /tmp/release-validator ./cmd/release-tool
ARG BUILD_VERSION=dev
ARG BUILD_REVISION=
ARG BUILD_BUILT_AT=
ARG BUILD_MODIFIED=unknown
RUN /tmp/release-validator --mode validate-stamp
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -buildvcs=false \
    -ldflags="-s -w -X ai-access-gateway/internal/buildinfo.Version=$BUILD_VERSION -X ai-access-gateway/internal/buildinfo.Revision=$BUILD_REVISION -X ai-access-gateway/internal/buildinfo.BuiltAt=$BUILD_BUILT_AT -X ai-access-gateway/internal/buildinfo.Modified=$BUILD_MODIFIED" \
    -o /out/ ./cmd/gateway ./cmd/inference-worker ./cmd/release-tool

FROM build AS release-binaries
RUN CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -buildvcs=false \
    -ldflags="-s -w -X ai-access-gateway/internal/buildinfo.Version=$BUILD_VERSION -X ai-access-gateway/internal/buildinfo.Revision=$BUILD_REVISION -X ai-access-gateway/internal/buildinfo.BuiltAt=$BUILD_BUILT_AT -X ai-access-gateway/internal/buildinfo.Modified=$BUILD_MODIFIED" \
    -o /out/windows-amd64/ ./cmd/gateway ./cmd/inference-worker ./cmd/update-agent ./cmd/lora-training-agent ./cmd/mining-agent ./cmd/system-monitor ./cmd/release-tool

FROM scratch AS release-artifacts
COPY --from=release-binaries /out/gateway /linux-amd64/gateway
COPY --from=release-binaries /out/inference-worker /linux-amd64/inference-worker
COPY --from=release-binaries /out/release-tool /linux-amd64/release-tool
COPY --from=release-binaries /out/windows-amd64/ /windows-amd64/

FROM alpine:3.23@sha256:fd791d74b68913cbb027c6546007b3f0d3bc45125f797758156952bc2d6daf40 AS runtime
RUN adduser -D -H -u 10001 appuser \
    && mkdir -p /var/lib/gateway-spool /var/lib/inference-outcomes \
    && chown appuser:appuser /var/lib/gateway-spool /var/lib/inference-outcomes
WORKDIR /app
USER appuser
STOPSIGNAL SIGTERM

FROM runtime AS inference-worker
COPY --from=build /out/inference-worker /app/inference-worker
EXPOSE 8096
ENTRYPOINT ["/app/inference-worker"]

FROM runtime AS gateway
USER root
RUN apk add --no-cache ffmpeg
USER appuser
COPY --from=build /out/gateway /app/gateway
EXPOSE 8090 8091
ENTRYPOINT ["/app/gateway"]
