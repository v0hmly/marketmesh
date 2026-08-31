# Инфраструктура

- [`compose`](compose/README.md) — локальное окружение Docker Compose с
  PostgreSQL streaming replication, раздельными Redis и SeaweedFS, а также
  OpenTelemetry Collector;
- `kubernetes` — манифесты и конфигурация Kubernetes для OrbStack и последующих окружений.

DMZ и внутренняя зона должны моделироваться отдельными сетями или namespace с запретом соединений из DMZ во внутреннюю зону.
