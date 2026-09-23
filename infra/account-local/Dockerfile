# API generation stage: keep identical across Dockerfiles for BuildKit cache reuse.
FROM node:24.19.0-bookworm@sha256:4196d66a565c6f195728d9952f161f4adfe2ad753052a08b7ec7f1c5a6bda42b AS api-node
FROM golang:1.27.0-bookworm@sha256:ded31c68586d2e49e760acc2e65a884b23d032e9bbbed0ae0c55abd3fcaf4452 AS api-generated
COPY --from=api-node /usr/local/ /usr/local/
RUN apt-get update && apt-get install -y --no-install-recommends unzip && rm -rf /var/lib/apt/lists/*
WORKDIR /workspace
COPY api/ api/
COPY backend/api/gen/go/go.mod backend/api/gen/go/go.sum backend/api/gen/go/
COPY frontend/package.json frontend/pnpm-lock.yaml frontend/pnpm-workspace.yaml frontend/
COPY frontend/gen/package.json frontend/gen/package.json
COPY frontend/apps/storefront/package.json frontend/apps/storefront/package.json
RUN printf 'go 1.27.0\nuse ./api/gen/go\n' > backend/go.work && corepack enable pnpm && \
    corepack prepare "$(node -p 'require("./frontend/package.json").packageManager')" --activate
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/workspace/.cache/protobuf \
    bash api/tools/protobuf.sh generate

FROM golang:1.27.0-bookworm@sha256:ded31c68586d2e49e760acc2e65a884b23d032e9bbbed0ae0c55abd3fcaf4452 AS go-build
WORKDIR /workspace
ENV GOWORK=/workspace/backend/go.work
COPY backend/api/gen/go/ backend/api/gen/go/
COPY --from=api-generated /workspace/backend/api/gen/go/ backend/api/gen/go/
COPY backend/api/tunnel/ backend/api/tunnel/
COPY backend/platform/ backend/platform/
COPY backend/services/auth/ backend/services/auth/
COPY backend/services/user/ backend/services/user/
COPY backend/services/gateway-in/ backend/services/gateway-in/
COPY backend/services/gateway-out/ backend/services/gateway-out/
COPY backend/tools/account-local/ backend/tools/account-local/
COPY backend/fixtures/account-local/go.work ./backend/go.work
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build CGO_ENABLED=0 go build -trimpath -o /out/auth ./backend/services/auth/cmd/auth && \
 CGO_ENABLED=0 go build -trimpath -o /out/user ./backend/services/user/cmd/user && \
 CGO_ENABLED=0 go build -trimpath -o /out/gateway-in ./backend/services/gateway-in/cmd/gateway-in && \
 CGO_ENABLED=0 go build -trimpath -o /out/gateway-out ./backend/services/gateway-out/cmd/gateway-out && \
 CGO_ENABLED=0 go build -trimpath -o /out/account-local ./backend/tools/account-local/cmd/account-local

FROM node:24.19.0-bookworm@sha256:4196d66a565c6f195728d9952f161f4adfe2ad753052a08b7ec7f1c5a6bda42b AS frontend-build
WORKDIR /workspace
COPY frontend/package.json frontend/pnpm-lock.yaml frontend/pnpm-workspace.yaml ./frontend/
COPY frontend/ frontend/
COPY --from=api-generated /workspace/frontend/gen/ frontend/gen/
ARG VITE_RYBBIT_ENABLED=false
ENV VITE_RYBBIT_ENABLED=$VITE_RYBBIT_ENABLED
ENV VITE_ACCOUNT_ADDRESSES_ENABLED=true
ENV VITE_ACCOUNT_SETTINGS_ENABLED=true
ENV VITE_ACCOUNT_ID_ENABLED=true
ARG VITE_ACCOUNT_AVATAR_ENABLED=false
ARG VITE_FILES_UPLOAD_ORIGIN
ARG VITE_FILES_DOWNLOAD_ORIGINS
ENV VITE_ACCOUNT_AVATAR_ENABLED=$VITE_ACCOUNT_AVATAR_ENABLED
ENV VITE_FILES_UPLOAD_ORIGIN=$VITE_FILES_UPLOAD_ORIGIN
ENV VITE_FILES_DOWNLOAD_ORIGINS=$VITE_FILES_DOWNLOAD_ORIGINS
WORKDIR /workspace/frontend
RUN corepack enable && pnpm install --frozen-lockfile && pnpm --filter @marketmesh/storefront build

FROM alpine:3.23.5@sha256:fd791d74b68913cbb027c6546007b3f0d3bc45125f797758156952bc2d6daf40
RUN apk add --no-cache --upgrade 'libcrypto3=3.5.8-r0' 'libssl3=3.5.8-r0'
COPY --from=go-build /out/ /usr/local/bin/
COPY --from=frontend-build /workspace/frontend/apps/storefront/dist/ /site/
COPY backend/services/auth/migrations/*.up.sql /migrations/auth/
COPY backend/services/user/migrations/*.up.sql /migrations/user/
COPY infra/account-local/launch.sh /usr/local/bin/launch
ENTRYPOINT ["/usr/local/bin/launch"]
