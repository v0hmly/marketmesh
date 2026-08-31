# Локальная инфраструктура Docker Compose

Окружение предназначено для разработки MarketMesh в OrbStack. Оно воспроизводит
границы доверия и ключевые свойства хранения, но не является production-схемой
высокой доступности.

## Быстрый запуск

Требуются Docker с Compose v2, Task и `openssl`. В OrbStack должен быть включён
Docker engine.

```bash
task infra:up
task infra:smoke
```

Первая команда создаёт игнорируемый Git файл `infra/compose/.env` с отдельными
случайными локальными секретами, загружает образы, запускает контейнеры и ждёт
их health checks. Повторный вызов идемпотентен. Имена переменных без значений
перечислены в [.env.example](.env.example); значения не выводятся в терминал и
не фиксируются в репозитории.

## Состав окружения

### PostgreSQL

Топология статична и не выполняет автоматический failover:

| Назначение | Compose service | DNS внутри `internal` | Роль |
| --- | --- | --- | --- |
| чтение и запись | `postgres-primary` | `postgres-rw:5432` | `app_rw` |
| согласованное чтение | `postgres-sync` | `postgres-ro:5432` | `app_ro` |
| асинхронная резервная копия | `postgres-async` | `postgres-async:5432` | только recovery |

Primary использует physical streaming replication. В
`synchronous_standby_names` явно указан `postgres_sync`, а
`synchronous_commit=remote_apply`, поэтому успешный commit через RW виден через
RO сразу после возврата клиенту. `postgres-async` не участвует в подтверждении
commit. Для обеих replicas заранее создаются отдельные physical replication
slots.

Роли имеют разные локальные пароли:

- `app_rw` не является суперпользователем и владеет прикладными таблицами;
- `app_ro` не является суперпользователем, получает только `SELECT` и имеет
  `default_transaction_read_only=on`;
- `replicator` имеет только `LOGIN` и `REPLICATION`;
- `postgres` используется только инициализацией и инфраструктурными проверками.

Проверка `task infra:smoke` ожидает в `pg_stat_replication` состояния `sync` и
`async`, записывает marker через `app_rw`, немедленно читает его через
`app_ro` и убеждается, что запись через RO отклонена.

Для интерактивной диагностики без публикации портов на host:

```bash
task infra:psql-rw
task infra:psql-ro
```

### Зоны доверия

Сети `marketmesh-dmz` и `marketmesh-internal` объявлены как `internal` и не
имеют общего контейнера. Порты хранилищ не публикуются на host: сервисы проекта
должны запускаться в нужной сети Compose и обращаться к стабильным DNS-именам.

DMZ содержит:

- `edge-state:6379` — отдельный Redis пограничного состояния;
- `object-quarantine:8333` — S3 endpoint карантина SeaweedFS;
- `object-public:8333` — S3 endpoint опубликованных объектов;
- `otel-collector:4317` и `otel-collector:4318` — OTLP gRPC и HTTP для DMZ.

Internal содержит:

- `auth-state:6379` — отдельный Redis чувствительного состояния Auth;
- `postgres-rw:5432` и `postgres-ro:5432`;
- `object-internal:8333` — S3 endpoint исходных и обработанных объектов;
- `otel-collector:4317` и `otel-collector:4318` — отдельный internal Collector.

Раздельные OpenTelemetry Collectors принимают traces и metrics и выводят их
через debug exporter. Такое локальное решение не создаёт контейнер-мост между
зонами. В production вместо debug exporter нужен защищённый backend и отдельная
политика экспорта.

`task infra:smoke` сначала подтверждает доступность разрешённых DMZ endpoints,
а затем проверяет недостижимость PostgreSQL, Redis Auth и внутреннего SeaweedFS
из контейнера, подключённого только к DMZ. Это проверка локальной модели, а не
доказательство физической изоляции production-сетей.

### Данные и ресурсы

PostgreSQL, оба Redis и все три SeaweedFS используют именованные volumes. Redis
работает с AOF, SeaweedFS запускается в single-node режиме `weed mini`, без
production HA. Команда `task infra:persistence` записывает markers в каждый тип
хранилища, выполняет обычный `docker compose restart` и проверяет markers после
восстановления готовности.

Лимиты Compose задают верхнюю границу около 3.3 GiB RAM и 4 CPU суммарно. Для
самого Docker/OrbStack рекомендуется выделить не менее 4 GiB RAM. Начальное
потребление диска состоит главным образом из образов; затем его определяют
именованные volumes. SeaweedFS ограничивает локальный размер одного volume до
256 MiB, но общий рост данных автоматически не ограничивается.

## Команды

| Task | Назначение |
| --- | --- |
| `task infra:env` | создать `.env`, если его ещё нет |
| `task infra:config` | проверить итоговую Compose-конфигурацию |
| `task infra:up` | создать или обновить окружение и дождаться готовности |
| `task infra:ready` | повторно проверить health checks |
| `task infra:smoke` | проверить PostgreSQL и изоляцию DMZ |
| `task infra:persistence` | проверить данные после обычного restart |
| `task infra:verify` | выполнить config, up, smoke и persistence |
| `task infra:status` | показать состояние контейнеров |
| `task infra:logs` | следить за общими логами |
| `task infra:down` | остановить контейнеры, сохранив volumes |
| `task infra:clean` | остановить окружение и удалить все его volumes |

## Остановка, очистка и смена секретов

Обычная остановка сохраняет данные:

```bash
task infra:down
```

Полная очистка необратимо удаляет volumes PostgreSQL, Redis и SeaweedFS:

```bash
task infra:clean
```

`infra:clean` сохраняет `.env`, чтобы следующий запуск использовал прежние
локальные значения. Для полной переинициализации секретов сначала выполните
`task infra:clean`, затем вручную удалите `infra/compose/.env` и снова запустите
`task infra:up`. Не удаляйте `.env` при существующих volumes: PostgreSQL уже
инициализирован старыми паролями, и проверки с новыми значениями не пройдут.

## Ограничения локальной модели

- нет Patroni, etcd, HAProxy, PgBouncer и автоматической смены ролей;
- нет backup/PITR и production TLS;
- нет NATS и Kubernetes;
- SeaweedFS не реплицируется и не заменяет CDN;
- Collector не хранит telemetry после завершения процесса;
- секреты являются локальными development credentials, а не моделью их
  production-ротации и доставки.
