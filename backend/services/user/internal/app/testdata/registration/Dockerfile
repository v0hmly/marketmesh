FROM golang:1.27.0-bookworm@sha256:ded31c68586d2e49e760acc2e65a884b23d032e9bbbed0ae0c55abd3fcaf4452
WORKDIR /workspace
ENV GOWORK=/workspace/backend/go.work
COPY backend/services/user/internal/app/testdata/registration/go.work ./backend/go.work
COPY backend/api/gen/go/go.mod backend/api/gen/go/go.sum backend/api/gen/go/
COPY backend/platform/go.mod backend/platform/go.sum backend/platform/
COPY backend/services/auth/go.mod backend/services/auth/go.sum backend/services/auth/
COPY backend/services/user/go.mod backend/services/user/go.sum backend/services/user/
RUN cd backend/services/user && GOWORK=off go mod download
RUN cd backend/api/gen/go && GOWORK=off go mod download
RUN cd backend/platform && GOWORK=off go mod download
RUN cd backend/services/auth && GOWORK=off go mod download
COPY backend/api/gen/go/ backend/api/gen/go/
COPY backend/platform/ backend/platform/
COPY backend/services/auth/ backend/services/auth/
COPY backend/services/user/ backend/services/user/
RUN go build -trimpath -o /usr/local/bin/auth ./backend/services/auth/cmd/auth
RUN go build -trimpath -o /usr/local/bin/user ./backend/services/user/cmd/user
RUN go build -trimpath -o /usr/local/bin/auth-registration-backfill ./backend/services/auth/cmd/auth-registration-backfill
RUN go build -o /usr/local/bin/registration-fixture-certgen ./backend/services/user/internal/app/testdata/registration/certgen
CMD ["go", "test", "-race", "-tags=integration,registrationintegration", "-p=1", "./backend/services/auth/internal/adapter/out/postgresbackfill", "./backend/services/user/internal/adapter/out/postgresregistration", "./backend/services/user/internal/app", "-run", "TestIntegrationBackfill|TestIntegrationAtomicInbox|TestIntegrationRegistrationDelivery", "-count=1", "-v"]
