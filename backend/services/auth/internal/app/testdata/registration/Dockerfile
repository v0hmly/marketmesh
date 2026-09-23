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

FROM golang:1.27.0-bookworm@sha256:ded31c68586d2e49e760acc2e65a884b23d032e9bbbed0ae0c55abd3fcaf4452
WORKDIR /workspace
ENV GOWORK=/workspace/backend/go.work
COPY backend/services/auth/internal/app/testdata/registration/go.work ./backend/go.work
COPY backend/api/gen/go/go.mod backend/api/gen/go/go.sum backend/api/gen/go/
COPY backend/platform/go.mod backend/platform/go.sum backend/platform/
COPY backend/services/auth/go.mod backend/services/auth/go.sum backend/services/auth/
RUN cd backend/api/gen/go && GOWORK=off go mod download
RUN cd backend/platform && GOWORK=off go mod download
RUN cd backend/services/auth && GOWORK=off go mod download
COPY backend/api/gen/go/ backend/api/gen/go/
COPY --from=api-generated /workspace/backend/api/gen/go/ backend/api/gen/go/
COPY backend/platform/ backend/platform/
COPY backend/services/auth/ backend/services/auth/
RUN go build -trimpath -o /usr/local/bin/auth ./backend/services/auth/cmd/auth
RUN go build -o /usr/local/bin/registration-fixture-certgen ./backend/services/auth/internal/app/testdata/registration/certgen
CMD ["go", "test", "-race", "-tags=integration", "-p=1", "./backend/services/auth/internal/adapter/out/postgres", "./backend/services/auth/internal/adapter/out/postgresoutbox", "./backend/services/auth/internal/adapter/out/jetstream", "./backend/services/auth/internal/app", "-run", "Integration", "-count=1", "-v"]
