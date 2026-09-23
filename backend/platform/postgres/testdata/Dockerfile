FROM golang:1.27.0-bookworm@sha256:ded31c68586d2e49e760acc2e65a884b23d032e9bbbed0ae0c55abd3fcaf4452

WORKDIR /workspace/platform

COPY backend/platform/go.mod backend/platform/go.sum ./
RUN go mod download

COPY backend/platform/ ./

CMD ["go", "test", "-tags=integration", "./postgres", "-run", "Integration", "-count=1"]
