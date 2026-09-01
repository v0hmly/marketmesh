FROM golang:1.27.0-bookworm@sha256:ded31c68586d2e49e760acc2e65a884b23d032e9bbbed0ae0c55abd3fcaf4452

WORKDIR /workspace

COPY services/auth/internal/adapter/out/postgres/testdata/go.work ./go.work
COPY api/gen/go/go.mod api/gen/go/go.sum api/gen/go/
COPY platform/go.mod platform/go.sum platform/
COPY services/auth/go.mod services/auth/go.sum services/auth/

RUN cd api/gen/go && GOWORK=off go mod download all
RUN cd platform && GOWORK=off go mod download all
RUN cd services/auth && GOWORK=off go mod download all

COPY api/gen/go/ api/gen/go/
COPY platform/ platform/
COPY services/auth/ services/auth/

RUN go test ./services/auth/internal/adapter/out/postgres

CMD ["go", "test", "-tags=integration", "./services/auth/internal/adapter/out/postgres", "-run", "Integration", "-count=1"]
