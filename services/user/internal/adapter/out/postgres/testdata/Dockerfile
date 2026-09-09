FROM golang:1.27.0-bookworm@sha256:ded31c68586d2e49e760acc2e65a884b23d032e9bbbed0ae0c55abd3fcaf4452

WORKDIR /workspace

COPY services/user/internal/adapter/out/postgres/testdata/go.work ./go.work
COPY api/gen/go/go.mod api/gen/go/go.sum api/gen/go/
COPY platform/go.mod platform/go.sum platform/
COPY services/user/go.mod services/user/go.sum services/user/

RUN cd api/gen/go && GOWORK=off go mod download
RUN cd platform && GOWORK=off go mod download
RUN cd services/user && GOWORK=off go mod download

COPY api/gen/go/ api/gen/go/
COPY platform/ platform/
COPY services/user/ services/user/

RUN go test ./services/user/internal/adapter/out/postgres

CMD ["go", "test", "-tags=integration", "-p=1", "./services/user/...", "-run", "Integration", "-count=1"]
