FROM golang:1.27.0-bookworm@sha256:ded31c68586d2e49e760acc2e65a884b23d032e9bbbed0ae0c55abd3fcaf4452
WORKDIR /workspace
COPY services/user/internal/app/testdata/registration/go.work ./go.work
COPY api/gen/go/go.mod api/gen/go/go.sum api/gen/go/
COPY platform/go.mod platform/go.sum platform/
COPY services/auth/go.mod services/auth/go.sum services/auth/
COPY services/user/go.mod services/user/go.sum services/user/
RUN cd services/user && GOWORK=off go mod download
RUN cd api/gen/go && GOWORK=off go mod download
RUN cd platform && GOWORK=off go mod download
RUN cd services/auth && GOWORK=off go mod download
COPY api/gen/go/ api/gen/go/
COPY platform/ platform/
COPY services/auth/ services/auth/
COPY services/user/ services/user/
RUN go build -trimpath -o /usr/local/bin/auth ./services/auth/cmd/auth
RUN go build -trimpath -o /usr/local/bin/user ./services/user/cmd/user
RUN go build -trimpath -o /usr/local/bin/auth-registration-backfill ./services/auth/cmd/auth-registration-backfill
RUN go build -o /usr/local/bin/registration-fixture-certgen ./services/user/internal/app/testdata/registration/certgen
CMD ["go", "test", "-race", "-tags=integration,registrationintegration", "-p=1", "./services/auth/internal/adapter/out/postgresbackfill", "./services/user/internal/adapter/out/postgresregistration", "./services/user/internal/app", "-run", "TestIntegrationBackfill|TestIntegrationAtomicInbox|TestIntegrationRegistrationDelivery", "-count=1", "-v"]
