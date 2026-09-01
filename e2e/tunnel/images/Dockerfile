# syntax=docker/dockerfile:1.19.0@sha256:b6afd42430b15f2d2a4c5a02b919e98a525b785b1aaff16747d2f623364e39b6

ARG SOURCE_DATE_EPOCH

FROM docker.io/library/golang:1.27.0-bookworm@sha256:ded31c68586d2e49e760acc2e65a884b23d032e9bbbed0ae0c55abd3fcaf4452 AS build
ARG SOURCE_DATE_EPOCH
WORKDIR /src

COPY go.work ./
COPY api/gen/go/go.mod api/gen/go/go.sum ./api/gen/go/
COPY api/tunnel/go.mod api/tunnel/go.sum ./api/tunnel/
COPY e2e/tunnel/go.mod e2e/tunnel/go.sum ./e2e/tunnel/
COPY platform/go.mod platform/go.sum ./platform/
COPY services/auth/go.mod services/auth/go.sum ./services/auth/
COPY services/gateway-in/go.mod services/gateway-in/go.sum ./services/gateway-in/
COPY services/gateway-out/go.mod services/gateway-out/go.sum ./services/gateway-out/
COPY services/user/go.mod ./services/user/
RUN --mount=type=cache,target=/go/pkg/mod,sharing=locked \
    --mount=type=cache,target=/root/.cache/go-build,sharing=locked \
    go mod download

COPY api/gen/go ./api/gen/go
COPY api/tunnel ./api/tunnel
COPY e2e/tunnel ./e2e/tunnel
COPY platform ./platform
COPY services/gateway-in ./services/gateway-in
COPY services/gateway-out ./services/gateway-out

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG REVISION=unknown
ENV CGO_ENABLED=0
RUN --mount=type=cache,target=/go/pkg/mod,sharing=locked \
    --mount=type=cache,target=/root/.cache/go-build,sharing=locked \
    GOOS="${TARGETOS}" GOARCH="${TARGETARCH}" \
    go build -trimpath -buildvcs=false -ldflags="-s -w" \
    -o /out/gateway-in ./services/gateway-in/cmd/gateway-in
RUN --mount=type=cache,target=/go/pkg/mod,sharing=locked \
    --mount=type=cache,target=/root/.cache/go-build,sharing=locked \
    GOOS="${TARGETOS}" GOARCH="${TARGETARCH}" \
    go build -trimpath -buildvcs=false -ldflags="-s -w" \
    -o /out/gateway-out ./services/gateway-out/cmd/gateway-out
RUN --mount=type=cache,target=/go/pkg/mod,sharing=locked \
    --mount=type=cache,target=/root/.cache/go-build,sharing=locked \
    GOOS="${TARGETOS}" GOARCH="${TARGETARCH}" \
    go build -trimpath -buildvcs=false -ldflags="-s -w" \
    -o /out/fake-internal ./e2e/tunnel/cmd/fake-internal

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab AS gateway-in
ARG SOURCE_DATE_EPOCH
ARG VERSION=dev
ARG REVISION=unknown
LABEL org.opencontainers.image.title="MarketMesh tunnel E2E gateway-in" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${REVISION}" \
      org.opencontainers.image.source="https://github.com/v0hmly/marketmesh"
COPY --from=build --chown=65532:65532 /out/gateway-in /gateway-in
USER 65532:65532
ENTRYPOINT ["/gateway-in"]

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab AS gateway-out
ARG SOURCE_DATE_EPOCH
ARG VERSION=dev
ARG REVISION=unknown
LABEL org.opencontainers.image.title="MarketMesh tunnel E2E gateway-out" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${REVISION}" \
      org.opencontainers.image.source="https://github.com/v0hmly/marketmesh"
COPY --from=build --chown=65532:65532 /out/gateway-out /gateway-out
USER 65532:65532
ENTRYPOINT ["/gateway-out"]

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab AS fake-internal
ARG SOURCE_DATE_EPOCH
ARG VERSION=dev
ARG REVISION=unknown
LABEL org.opencontainers.image.title="MarketMesh tunnel E2E fake internal" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${REVISION}" \
      org.opencontainers.image.source="https://github.com/v0hmly/marketmesh"
COPY --from=build --chown=65532:65532 /out/fake-internal /fake-internal
USER 65532:65532
ENTRYPOINT ["/fake-internal"]
