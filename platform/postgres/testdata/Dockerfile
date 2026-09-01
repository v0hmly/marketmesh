FROM golang:1.27.0-bookworm@sha256:ded31c68586d2e49e760acc2e65a884b23d032e9bbbed0ae0c55abd3fcaf4452

WORKDIR /workspace/platform

COPY platform/go.mod platform/go.sum ./
RUN go mod download

COPY platform/ ./

CMD ["go", "test", "-tags=integration", "./postgres", "-run", "Integration", "-count=1"]
