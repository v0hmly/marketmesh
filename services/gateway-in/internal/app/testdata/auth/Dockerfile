FROM golang:1.27.0-bookworm@sha256:ded31c68586d2e49e760acc2e65a884b23d032e9bbbed0ae0c55abd3fcaf4452
WORKDIR /workspace
COPY services/gateway-in/internal/app/testdata/auth/go.work ./go.work
COPY api/gen/go/go.mod api/gen/go/go.sum api/gen/go/
RUN cd api/gen/go && GOWORK=off go mod download
COPY api/tunnel/go.mod api/tunnel/go.sum api/tunnel/
RUN cd api/tunnel && GOWORK=off go mod download
COPY platform/go.mod platform/go.sum platform/
RUN cd platform && GOWORK=off go mod download
COPY services/auth/go.mod services/auth/go.sum services/auth/
RUN cd services/auth && GOWORK=off go mod download
COPY services/gateway-in/go.mod services/gateway-in/go.sum services/gateway-in/
RUN cd services/gateway-in && GOWORK=off go mod download
COPY services/gateway-out/go.mod services/gateway-out/go.sum services/gateway-out/
RUN cd services/gateway-out && GOWORK=off go mod download
COPY api/gen/go/ api/gen/go/
COPY api/tunnel/ api/tunnel/
COPY platform/ platform/
COPY services/auth/ services/auth/
COPY services/gateway-in/ services/gateway-in/
COPY services/gateway-out/ services/gateway-out/
RUN go build -trimpath -o /usr/local/bin/gateway-in ./services/gateway-in/cmd/gateway-in
RUN go build -trimpath -o /usr/local/bin/auth ./services/auth/cmd/auth
RUN go build -trimpath -o /usr/local/bin/gateway-out ./services/gateway-out/cmd/gateway-out
CMD ["go", "test", "-race", "-tags=integration,authbrowserintegration", "./services/gateway-in/internal/app", "-run", "TestIntegrationAuthBrowser", "-count=1", "-v"]
