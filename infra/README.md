# Инфраструктура

- [`compose`](compose/README.md) — локальное окружение Docker Compose с
  PostgreSQL streaming replication, раздельными Redis и SeaweedFS, зональными
  Alloy OTLP endpoints, Tempo, Loki, Grafana и изолированным loopback-gateway;
- `kubernetes` — манифесты и конфигурация Kubernetes для OrbStack и последующих окружений.

DMZ и внутренняя зона должны моделироваться отдельными сетями или namespace с запретом соединений из DMZ во внутреннюю зону.
