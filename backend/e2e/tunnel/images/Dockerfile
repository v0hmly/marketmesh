# syntax=docker/dockerfile:1.19.0@sha256:b6afd42430b15f2d2a4c5a02b919e98a525b785b1aaff16747d2f623364e39b6

ARG SOURCE_DATE_EPOCH

FROM docker.io/library/golang:1.27.0-bookworm@sha256:ded31c68586d2e49e760acc2e65a884b23d032e9bbbed0ae0c55abd3fcaf4452 AS build
ARG SOURCE_DATE_EPOCH
WORKDIR /src
ENV GOWORK=/src/backend/go.work

COPY backend/go.work ./backend/
COPY backend/api/gen/go/go.mod backend/api/gen/go/go.sum ./backend/api/gen/go/
COPY backend/api/tunnel/go.mod backend/api/tunnel/go.sum ./backend/api/tunnel/
COPY backend/e2e/tunnel/go.mod backend/e2e/tunnel/go.sum ./backend/e2e/tunnel/
COPY backend/platform/go.mod backend/platform/go.sum ./backend/platform/
COPY backend/services/auth/go.mod backend/services/auth/go.sum ./backend/services/auth/
COPY backend/services/gateway-in/go.mod backend/services/gateway-in/go.sum ./backend/services/gateway-in/
COPY backend/services/gateway-out/go.mod backend/services/gateway-out/go.sum ./backend/services/gateway-out/
COPY backend/services/user/go.mod ./backend/services/user/
COPY backend/services/files/go.mod backend/services/files/go.sum ./backend/services/files/
COPY backend/tools/account-local/go.mod backend/tools/account-local/go.sum ./backend/tools/account-local/
COPY backend/tools/e2e-topology/go.mod ./backend/tools/e2e-topology/
RUN --mount=type=cache,target=/go/pkg/mod,sharing=locked \
    --mount=type=cache,target=/root/.cache/go-build,sharing=locked \
    go mod download

COPY backend/api/gen/go ./backend/api/gen/go
COPY backend/api/tunnel ./backend/api/tunnel
COPY backend/e2e/tunnel ./backend/e2e/tunnel
COPY backend/platform ./backend/platform
COPY backend/services/gateway-in ./backend/services/gateway-in
COPY backend/services/gateway-out ./backend/services/gateway-out

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG REVISION=unknown
ENV CGO_ENABLED=0
RUN --mount=type=cache,target=/go/pkg/mod,sharing=locked \
    --mount=type=cache,target=/root/.cache/go-build,sharing=locked \
    GOOS="${TARGETOS}" GOARCH="${TARGETARCH}" \
    go build -trimpath -buildvcs=false -ldflags="-s -w" \
    -o /out/gateway-in ./backend/services/gateway-in/cmd/gateway-in
RUN --mount=type=cache,target=/go/pkg/mod,sharing=locked \
    --mount=type=cache,target=/root/.cache/go-build,sharing=locked \
    GOOS="${TARGETOS}" GOARCH="${TARGETARCH}" \
    go build -trimpath -buildvcs=false -ldflags="-s -w" \
    -o /out/gateway-out ./backend/services/gateway-out/cmd/gateway-out
RUN --mount=type=cache,target=/go/pkg/mod,sharing=locked \
    --mount=type=cache,target=/root/.cache/go-build,sharing=locked \
    GOOS="${TARGETOS}" GOARCH="${TARGETARCH}" \
    go build -trimpath -buildvcs=false -ldflags="-s -w" \
    -o /out/fake-internal ./backend/e2e/tunnel/cmd/fake-internal

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
