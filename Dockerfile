# syntax=docker/dockerfile:1.20@sha256:26147acbda4f14c5add9946e2fd2ed543fc402884fd75146bd342a7f6271dc1d

# Digest update and verification instructions are in docs/container-images.md.
FROM --platform=$BUILDPLATFORM golang:1.26.5-alpine3.23@sha256:622e56dbc11a8cfe87cafa2331e9a201877271cbff918af53d3be315f3da88cc AS build

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=0.0.0-dev
ARG COMMIT=unknown
ARG BUILD_DATE=1970-01-01T00:00:00Z
ARG SOURCE_DATE_EPOCH=0
ARG BUILD_CACHE_ID=default

WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,id=acr-go-mod-${BUILD_CACHE_ID},target=/go/pkg/mod,sharing=locked go mod download

COPY . ./
RUN --mount=type=cache,id=acr-go-build-${BUILD_CACHE_ID},target=/root/.cache/go-build,sharing=locked \
    --mount=type=cache,id=acr-go-mod-${BUILD_CACHE_ID},target=/go/pkg/mod,sharing=locked \
    --network=none \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} SOURCE_DATE_EPOCH=${SOURCE_DATE_EPOCH} \
    go build -buildvcs=false -trimpath \
      -ldflags="-s -w -buildid= -X github.com/full-chaos/dev-health-acr/internal/version.Version=${VERSION} -X github.com/full-chaos/dev-health-acr/internal/version.Commit=${COMMIT} -X github.com/full-chaos/dev-health-acr/internal/version.Date=${BUILD_DATE}" \
      -o /out/acr-api ./cmd/acr-api && \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} SOURCE_DATE_EPOCH=${SOURCE_DATE_EPOCH} \
    go build -buildvcs=false -trimpath \
      -ldflags="-s -w -buildid= -X github.com/full-chaos/dev-health-acr/internal/version.Version=${VERSION} -X github.com/full-chaos/dev-health-acr/internal/version.Commit=${COMMIT} -X github.com/full-chaos/dev-health-acr/internal/version.Date=${BUILD_DATE}" \
      -o /out/acr-mcp ./cmd/acr-mcp && \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} SOURCE_DATE_EPOCH=${SOURCE_DATE_EPOCH} \
    go build -buildvcs=false -trimpath \
      -ldflags="-s -w -buildid= -X github.com/full-chaos/dev-health-acr/internal/version.Version=${VERSION} -X github.com/full-chaos/dev-health-acr/internal/version.Commit=${COMMIT} -X github.com/full-chaos/dev-health-acr/internal/version.Date=${BUILD_DATE}" \
      -o /out/acr-migrate ./cmd/acr-migrate && \
    mkdir -p /out-api-root/etc/ssl/certs /out-api-root/usr/local/bin \
      /out-mcp-root/etc/ssl/certs /out-mcp-root/usr/local/bin && \
    cp /etc/ssl/certs/ca-certificates.crt /out-api-root/etc/ssl/certs/ca-certificates.crt && \
    cp /etc/ssl/certs/ca-certificates.crt /out-mcp-root/etc/ssl/certs/ca-certificates.crt && \
    cp /out/acr-api /out/acr-migrate /out-api-root/usr/local/bin/ && \
    cp /out/acr-mcp /out-mcp-root/usr/local/bin/ && \
    find /out-api-root /out-mcp-root -exec touch -d "@${SOURCE_DATE_EPOCH}" {} +

FROM gcr.io/distroless/static-debian12:nonroot@sha256:f5b485ea962d9bd1186b2f6b3a061191539b905b82ec395de78cbfae51f20e35 AS acr-api

ARG VERSION=0.0.0-dev
ARG COMMIT=unknown
ARG BUILD_DATE=1970-01-01T00:00:00Z

LABEL org.opencontainers.image.title="ACR API" \
      org.opencontainers.image.description="Dev Health Agent Context Runtime hosted API" \
      org.opencontainers.image.source="https://github.com/full-chaos/dev-health-acr" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.created="${BUILD_DATE}"

COPY --from=build /out-api-root/ /

USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/acr-api"]
CMD ["serve"]

FROM cgr.dev/chainguard/git:latest@sha256:62135ba579e1ac309441b3de21983230a4979603ab879bafa6d2773e8d5fd626 AS acr-mcp-base

FROM build AS acr-mcp-root
COPY --from=acr-mcp-base / /mcp-root
RUN rm -f /mcp-root/usr/bin/sh /mcp-root/usr/bin/dash && \
    printf '[safe]\n\tdirectory = /workspace\n' > /mcp-root/etc/gitconfig

FROM scratch AS acr-mcp

ARG VERSION=0.0.0-dev
ARG COMMIT=unknown
ARG BUILD_DATE=1970-01-01T00:00:00Z

LABEL org.opencontainers.image.title="ACR MCP" \
      org.opencontainers.image.description="Dev Health Agent Context Runtime local STDIO sidecar" \
      org.opencontainers.image.source="https://github.com/full-chaos/dev-health-acr" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.created="${BUILD_DATE}"

COPY --from=acr-mcp-root /mcp-root/ /
COPY --from=build /out-mcp-root/ /

USER 65532:65532
ENTRYPOINT ["/usr/local/bin/acr-mcp"]
