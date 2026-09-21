# Инфраструктура

- [`compose`](compose/README.md) — локальное окружение Docker Compose с
  PostgreSQL streaming replication, раздельными Redis и SeaweedFS, зональными
  Alloy OTLP endpoints, Tempo, Loki, Grafana и изолированным loopback-gateway;
- `kubernetes` — манифесты и конфигурация Kubernetes для OrbStack и последующих окружений.
- [`account-local`](account-local/README.md) — отдельный HTTPS-кабинет с настоящими
  Auth/User, событием регистрации, обратным mTLS-туннелем и браузерными E2E.

- [`mailpit`](mailpit/README.md) — локальный SMTP-приёмник и web UI для просмотра писем.

DMZ и внутренняя зона должны моделироваться отдельными сетями или namespace с запретом соединений из DMZ во внутреннюю зону.
