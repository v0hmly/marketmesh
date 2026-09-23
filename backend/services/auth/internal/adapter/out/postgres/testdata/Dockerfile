FROM golang:1.27.0-bookworm@sha256:ded31c68586d2e49e760acc2e65a884b23d032e9bbbed0ae0c55abd3fcaf4452

WORKDIR /workspace
ENV GOWORK=/workspace/backend/go.work

COPY backend/services/auth/internal/adapter/out/postgres/testdata/go.work ./backend/go.work
COPY backend/api/gen/go/go.mod backend/api/gen/go/go.sum backend/api/gen/go/
COPY backend/platform/go.mod backend/platform/go.sum backend/platform/
COPY backend/services/auth/go.mod backend/services/auth/go.sum backend/services/auth/

RUN cd backend/api/gen/go && GOWORK=off go mod download all
RUN cd backend/platform && GOWORK=off go mod download all
RUN cd backend/services/auth && GOWORK=off go mod download all

COPY backend/api/gen/go/ backend/api/gen/go/
COPY backend/platform/ backend/platform/
COPY backend/services/auth/ backend/services/auth/

RUN go test ./backend/services/auth/internal/adapter/out/postgres

CMD ["go", "test", "-tags=integration", "./backend/services/auth/internal/adapter/out/postgres", "-run", "Integration", "-count=1"]
