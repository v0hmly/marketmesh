FROM golang:1.27.0-bookworm@sha256:ded31c68586d2e49e760acc2e65a884b23d032e9bbbed0ae0c55abd3fcaf4452
WORKDIR /workspace
ENV GOWORK=/workspace/backend/go.work
COPY backend/services/gateway-in/internal/app/testdata/auth/go.work ./backend/go.work
COPY backend/api/gen/go/go.mod backend/api/gen/go/go.sum backend/api/gen/go/
RUN cd backend/api/gen/go && GOWORK=off go mod download
COPY backend/api/tunnel/go.mod backend/api/tunnel/go.sum backend/api/tunnel/
RUN cd backend/api/tunnel && GOWORK=off go mod download
COPY backend/platform/go.mod backend/platform/go.sum backend/platform/
RUN cd backend/platform && GOWORK=off go mod download
COPY backend/services/auth/go.mod backend/services/auth/go.sum backend/services/auth/
RUN cd backend/services/auth && GOWORK=off go mod download
COPY backend/services/gateway-in/go.mod backend/services/gateway-in/go.sum backend/services/gateway-in/
RUN cd backend/services/gateway-in && GOWORK=off go mod download
COPY backend/services/gateway-out/go.mod backend/services/gateway-out/go.sum backend/services/gateway-out/
RUN cd backend/services/gateway-out && GOWORK=off go mod download
COPY backend/services/user/go.mod backend/services/user/go.sum backend/services/user/
RUN cd backend/services/user && GOWORK=off go mod download
COPY backend/api/gen/go/ backend/api/gen/go/
COPY backend/api/tunnel/ backend/api/tunnel/
COPY backend/platform/ backend/platform/
COPY backend/services/auth/ backend/services/auth/
COPY backend/services/user/ backend/services/user/
COPY backend/services/gateway-in/ backend/services/gateway-in/
COPY backend/services/gateway-out/ backend/services/gateway-out/
COPY frontend/tests/auth-browser/ frontend/tests/auth-browser/
RUN go build -trimpath -o /usr/local/bin/gateway-in ./backend/services/gateway-in/cmd/gateway-in
RUN go build -trimpath -o /usr/local/bin/auth ./backend/services/auth/cmd/auth
RUN go build -trimpath -o /usr/local/bin/gateway-out ./backend/services/gateway-out/cmd/gateway-out
RUN go build -trimpath -o /usr/local/bin/user ./backend/services/user/cmd/user
CMD ["go", "test", "-race", "-tags=integration,authbrowserintegration", "./backend/services/gateway-in/internal/app", "-run", "TestIntegrationAuthBrowser", "-count=1", "-v"]
