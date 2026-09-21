FROM golang:1.27.0-bookworm@sha256:ded31c68586d2e49e760acc2e65a884b23d032e9bbbed0ae0c55abd3fcaf4452 AS go-build
WORKDIR /workspace
COPY api/gen/go/ api/gen/go/
COPY api/tunnel/ api/tunnel/
COPY platform/ platform/
COPY services/auth/ services/auth/
COPY services/user/ services/user/
COPY services/gateway-in/ services/gateway-in/
COPY services/gateway-out/ services/gateway-out/
COPY tools/account-local/ tools/account-local/
COPY infra/account-local/go.work ./go.work
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build CGO_ENABLED=0 go build -trimpath -o /out/auth ./services/auth/cmd/auth && \
 CGO_ENABLED=0 go build -trimpath -o /out/user ./services/user/cmd/user && \
 CGO_ENABLED=0 go build -trimpath -o /out/gateway-in ./services/gateway-in/cmd/gateway-in && \
 CGO_ENABLED=0 go build -trimpath -o /out/gateway-out ./services/gateway-out/cmd/gateway-out && \
 CGO_ENABLED=0 go build -trimpath -o /out/account-local ./tools/account-local/cmd/account-local

FROM node:24.19.0-bookworm@sha256:4196d66a565c6f195728d9952f161f4adfe2ad753052a08b7ec7f1c5a6bda42b AS frontend-build
WORKDIR /workspace
COPY package.json pnpm-lock.yaml pnpm-workspace.yaml ./
COPY frontend/ frontend/
COPY api/gen/ts/ api/gen/ts/
ARG VITE_RYBBIT_ENABLED=false
ENV VITE_RYBBIT_ENABLED=$VITE_RYBBIT_ENABLED
ENV VITE_ACCOUNT_ADDRESSES_ENABLED=true
ENV VITE_ACCOUNT_SETTINGS_ENABLED=true
RUN corepack enable && pnpm install --frozen-lockfile && pnpm --filter @marketmesh/storefront build

FROM alpine:3.23.5@sha256:fd791d74b68913cbb027c6546007b3f0d3bc45125f797758156952bc2d6daf40
RUN apk add --no-cache --upgrade 'libcrypto3=3.5.8-r0' 'libssl3=3.5.8-r0'
COPY --from=go-build /out/ /usr/local/bin/
COPY --from=frontend-build /workspace/frontend/apps/storefront/dist/ /site/
COPY services/auth/migrations/*.up.sql /migrations/auth/
COPY services/user/migrations/*.up.sql /migrations/user/
COPY infra/account-local/launch.sh /usr/local/bin/launch
ENTRYPOINT ["/usr/local/bin/launch"]
