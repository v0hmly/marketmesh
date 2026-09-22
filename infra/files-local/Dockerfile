FROM golang:1.27.0-bookworm@sha256:ded31c68586d2e49e760acc2e65a884b23d032e9bbbed0ae0c55abd3fcaf4452 AS build
WORKDIR /workspace
COPY api/gen/go/ api/gen/go/
COPY platform/ platform/
COPY services/files/ services/files/
RUN printf 'go 1.27.0\nuse (\n ./api/gen/go\n ./platform\n ./services/files\n)\n' > go.work
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -o /out/files ./services/files/cmd/files && \
    CGO_ENABLED=0 go build -trimpath -o /out/files-sandbox ./services/files/cmd/files-sandbox

FROM debian:13.7-slim@sha256:a99cfc517144bc59b1978475ec53b46ecabec7e43635402ee5b77cc54cd1b20a AS parsers
RUN apt-get update && apt-get install -y --no-install-recommends \
    libreoffice-writer libreoffice-calc libreoffice-impress poppler-utils fonts-dejavu-core \
    ca-certificates && \
    dpkg-query -W > /usr/share/files-parser-packages.txt && rm -rf /var/lib/apt/lists/*

FROM parsers AS sandbox
COPY --from=build /out/files-sandbox /usr/local/bin/files-sandbox
USER 10001:10001
ENTRYPOINT ["/usr/local/bin/files-sandbox"]

FROM scratch AS av-package-arm64
ADD --checksum=sha256:4bdab4118e8accb02108316b996d7c14da1cccc4eea2dff7bea82109f9239d95 https://github.com/Cisco-Talos/clamav/releases/download/clamav-1.5.4/clamav-1.5.4.linux.aarch64.deb /clamav.deb
FROM scratch AS av-package-amd64
ADD --checksum=sha256:28d6efc5b4423e7830c3559339552eb53870a9eac51ac4efb37d60530d329886 https://github.com/Cisco-Talos/clamav/releases/download/clamav-1.5.4/clamav-1.5.4.linux.x86_64.deb /clamav.deb
FROM av-package-${TARGETARCH} AS av-package

FROM debian:13.7-slim@sha256:a99cfc517144bc59b1978475ec53b46ecabec7e43635402ee5b77cc54cd1b20a AS antivirus
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates libstdc++6 && rm -rf /var/lib/apt/lists/*
RUN --mount=type=bind,from=av-package,source=/clamav.deb,target=/tmp/clamav.deb \
    dpkg -i /tmp/clamav.deb && ldconfig && /usr/local/bin/clamscan --version
COPY infra/files-local/clamd.conf /etc/clamav/clamd.conf
USER 10001:10001
ENV TZ=UTC
ENTRYPOINT ["/usr/local/sbin/clamd", "--foreground=true", "--config-file=/etc/clamav/clamd.conf"]

FROM scratch AS runtime
COPY --from=build /out/files /usr/local/bin/files
USER 10001:10001
ENTRYPOINT ["/usr/local/bin/files"]

FROM build AS integration-build
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    go test -race -tags=integration -c -o /out/files-integration ./services/files/internal/app && \
    go test -race -tags=integration -c -o /out/files-s3-integration ./services/files/internal/adapter/out/s3store
FROM debian:bookworm-slim@sha256:3783cc01769c7b2b1b83a5c5ad96c815348e28ed7da68e2e3687004faa906251 AS integration
COPY --from=integration-build /out/files-integration /usr/local/bin/files-integration
COPY --from=integration-build /out/files-s3-integration /usr/local/bin/files-s3-integration
USER 10001:10001
ENTRYPOINT ["/usr/local/bin/files-integration", "-test.v", "-test.run=TestLive", "-test.timeout=15m"]
