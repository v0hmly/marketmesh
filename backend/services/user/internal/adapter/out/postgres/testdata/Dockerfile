FROM golang:1.27.0-bookworm@sha256:ded31c68586d2e49e760acc2e65a884b23d032e9bbbed0ae0c55abd3fcaf4452

WORKDIR /workspace
ENV GOWORK=/workspace/backend/go.work

COPY backend/services/user/internal/adapter/out/postgres/testdata/go.work ./backend/go.work
COPY backend/api/gen/go/go.mod backend/api/gen/go/go.sum backend/api/gen/go/
COPY backend/platform/go.mod backend/platform/go.sum backend/platform/
COPY backend/services/user/go.mod backend/services/user/go.sum backend/services/user/

RUN cd backend/api/gen/go && GOWORK=off go mod download
RUN cd backend/platform && GOWORK=off go mod download
RUN cd backend/services/user && GOWORK=off go mod download

COPY backend/api/gen/go/ backend/api/gen/go/
COPY backend/platform/ backend/platform/
COPY backend/services/user/ backend/services/user/

RUN go test ./backend/services/user/internal/adapter/out/postgres

CMD ["go", "test", "-tags=integration", "-p=1", "./backend/services/user/...", "-run", "Integration", "-count=1"]
