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

Если нужны только логи и трассировки без PostgreSQL, Redis и SeaweedFS:

```bash
task observability:up
task observability:smoke
```

Grafana после запуска доступна только локально по адресу
<http://127.0.0.1:3000>. Авторизация для development-экземпляра отключена,
анонимному пользователю выдана роль Editor. Порт привязан к loopback, поэтому
такой режим нельзя переносить в общее или production-окружение.

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
- `otel-collector:4317` и `otel-collector:4318` — Alloy OTLP gRPC и HTTP для DMZ.

Internal содержит:

- `auth-state:6379` — отдельный Redis чувствительного состояния Auth;
- `postgres-rw:5432` и `postgres-ro:5432`;
- `object-internal:8333` — S3 endpoint исходных и обработанных объектов;
- `otel-collector:4317` и `otel-collector:4318` — отдельный internal Alloy.

Раздельные экземпляры Alloy принимают OTLP в каждой зоне и не подключаются к
соседней зоне. Каждый имеет исходящее подключение к отдельной внутренней сети
`marketmesh-observability`, где работают Tempo и Loki. Имена `otel-collector`
сохранены, поэтому конфигурация `platform/telemetry` не получает конкурирующую
точку приёма. Alloy принимает metrics для совместимости с `platform/telemetry`,
но после bounded processing отбрасывает их: отдельный metrics backend не входит
в MM-17.

Сеть `marketmesh-observability` остаётся Docker `internal`, поэтому Alloy,
Tempo, Loki и Grafana не получают внешний egress. Доступ с host обеспечивает
отдельный HAProxy: он подключён к этой сети и к пустой access-сети, публикует
только перечисленные ниже порты и только на `127.0.0.1`. Из DMZ или internal
через него нельзя маршрутизировать произвольный трафик.

`task infra:smoke` сначала подтверждает доступность разрешённых DMZ endpoints,
а затем проверяет недостижимость PostgreSQL, Redis Auth и внутреннего SeaweedFS
из контейнера, подключённого только к DMZ. Это проверка локальной модели, а не
доказательство физической изоляции production-сетей.

### Данные и ресурсы

PostgreSQL, оба Redis, все три SeaweedFS, Tempo, Loki и Grafana используют
именованные volumes. Redis работает с AOF, SeaweedFS запускается в single-node
режиме `weed mini`, без production HA. Команда `task infra:persistence`
записывает markers во все хранилища, отправляет связанный trace и log, выполняет
обычный `docker compose restart` и проверяет данные после восстановления.

Лимиты Compose задают верхнюю границу около 4.5 GB RAM и 5.8 CPU суммарно. Для
Docker/OrbStack рекомендуется выделить не менее 6 GiB RAM. Loki и Tempo хранят
данные 24 часа; Loki дополнительно ограничивает скорость ingestion, размер строки
и количество streams. Контейнерные stdout/stderr-логи observability вращаются
по три файла размером 10 MiB. Переносимые hard quota для именованных Docker
volumes не задаются: проверяйте их через `docker system df` и выполняйте полную
очистку, если локальный диск заканчивается.

## Observability: поток данных и версии

Все образы зафиксированы без `latest`: Alloy `v1.19.2`, Tempo `2.10.5`, Loki
`3.7.7`, Grafana `13.2.0`, HAProxy `3.2.23-alpine3.24` и Alpine `3.23.5` для
одноразовой инициализации прав volumes. Tempo намеренно остаётся на монолитном
поколении 2.x: MM-17 не включает распределённую архитектуру Tempo 3.

```text
service in DMZ      -> alloy-dmz      --traces--> Tempo
service in internal -> alloy-internal --logs----> Loki
                                      --metrics-> drop (backend отсутствует)
host 127.0.0.1      -> HAProxy ------> Alloy / Tempo / Loki / Grafana
Grafana                              -> Tempo + Loki
```

Alloy использует memory limiter 96 MiB, batch до 1024 элементов и отдельные
bounded queues по 64 batch. При недоступном Tempo или Loki повторные попытки
прекращаются через 30 секунд, поэтому очередь не растёт бесконечно. Потеря части
локальной development-телеметрии в продолжительной аварии ожидаема.

### Настройка приложений

`platform/telemetry.Config.Endpoint` принимает `host:port` без схемы. Для
локального незашифрованного OTLP обязательно укажите `Insecure: true`:

```go
pipeline, err := telemetry.New(ctx, telemetry.Config{
	ServiceName:    "user",
	ServiceVersion: "dev",
	Environment:    "local",
	InstanceID:     "user-local-1",
	Endpoint:       "127.0.0.1:4317",
	Insecure:       true,
})
```

Порты с host:

| Зона приложения | OTLP/gRPC | OTLP/HTTP |
| --- | --- | --- |
| internal | `127.0.0.1:4317` | `http://127.0.0.1:4318` |
| DMZ | `127.0.0.1:14317` | `http://127.0.0.1:14318` |

В контейнерах обеих зон используйте `otel-collector:4317` или
`http://otel-collector:4318`; DNS разрешается в соответствующий Alloy. Порты
можно переопределить переменными из `.env.example`, но они по-прежнему
публикуются только на `127.0.0.1`.

Traces и metrics отправляет `platform/telemetry`. Структурированные logs должны
поступать в тот же Alloy по OTLP/HTTP `/v1/logs` или OTLP/gRPC и содержать
совпадающие `trace_id`/`span_id`. Не отправляйте тела запросов, токены, Cookie,
пароли, e-mail, PII и почти уникальные значения в Loki labels. `service.name`,
`service.version`, `deployment.environment.name` и `service.instance.id`
совпадают с ресурсом `platform/telemetry`; высококардинальный
`service.instance.id` сохраняется как structured metadata, а не index label.

### Поиск и корреляция в Grafana

Provisioning создаёт папку `MarketMesh`, dashboards «состояние сервисов» и
«observability stack», а также data sources `Loki` и `Tempo`. В Explore:

- traces ищутся в Tempo по `resource.service.name`;
- logs ищутся в Loki запросом `{service_name="user"}`;
- точная корреляция выполняется фильтром `| trace_id="<trace-id>"`;
- ссылка `trace_id` в JSON log открывает соответствующий trace, а span в Tempo
  предлагает переход к logs по service name, trace ID и span ID.

Smoke-проверка отправляет один trace и один OTLP log с общими ID, проверяет
обязательные resource attributes, data sources и dashboards Grafana.
`task observability:outage` временно останавливает Tempo и Loki, отправляет
телеметрию, проверяет фиксированный размер очередей, лимит памяти и состояние
обоих Alloy, затем восстанавливает backends и повторяет сквозной smoke.

## Команды

| Task | Назначение |
| --- | --- |
| `task infra:env` | создать `.env`, если его ещё нет |
| `task infra:config` | проверить итоговую Compose-конфигурацию |
| `task infra:up` | создать или обновить окружение и дождаться готовности |
| `task infra:ready` | повторно проверить health checks |
| `task infra:smoke` | проверить PostgreSQL, DMZ и observability pipeline |
| `task infra:persistence` | проверить данные после обычного restart |
| `task infra:verify` | выполнить config, up, smoke и persistence |
| `task infra:status` | показать состояние контейнеров |
| `task infra:logs` | следить за общими логами |
| `task infra:down` | остановить контейнеры, сохранив volumes |
| `task infra:clean` | остановить окружение и удалить все его volumes |
| `task observability:config` | проверить Compose, Alloy, Tempo, Loki и HAProxy |
| `task observability:up` | запустить только observability-стек |
| `task observability:ready` | проверить health checks observability |
| `task observability:smoke` | отправить и найти связанный trace и log |
| `task observability:outage` | проверить bounded queues при недоступных backends |
| `task observability:persistence` | проверить данные после restart observability |
| `task observability:verify` | выполнить полный набор observability-проверок |
| `task observability:status` | показать состояние observability-контейнеров |
| `task observability:logs` | следить за их stdout/stderr |
| `task observability:down` | остановить observability с сохранением volumes |
| `task observability:clean` | удалить только observability-контейнеры и volumes |

## Остановка, очистка и смена секретов

Обычная остановка сохраняет данные:

```bash
task infra:down
```

Полная очистка необратимо удаляет volumes PostgreSQL, Redis, SeaweedFS, Tempo,
Loki и Grafana:

```bash
task infra:clean
```

Удалить только traces, logs и локальное состояние Grafana, не затрагивая
PostgreSQL, Redis и SeaweedFS:

```bash
task observability:clean
```

`infra:clean` сохраняет `.env`, чтобы следующий запуск использовал прежние
локальные значения. Для полной переинициализации секретов сначала выполните
`task infra:clean`, затем вручную удалите `infra/compose/.env` и снова запустите
`task infra:up`. Не удаляйте `.env` при существующих volumes: PostgreSQL уже
инициализирован старыми паролями, и проверки с новыми значениями не пройдут.

## Ограничения локальной модели

- нет Patroni, etcd, PgBouncer и автоматической смены ролей PostgreSQL;
- нет backup/PITR и production TLS;
- нет NATS и Kubernetes;
- SeaweedFS не реплицируется и не заменяет CDN;
- Tempo и Loki являются single-process локальными backends без HA и backup;
- metrics backend и alerts отсутствуют, принятые metrics не сохраняются;
- секреты являются локальными development credentials, а не моделью их
  production-ротации и доставки.

## Диагностика

- `address already in use`: переопределите конфликтующий loopback-порт в
  `infra/compose/.env` по именам из `.env.example`;
- Alloy healthy, но данных нет: проверьте `task observability:logs`, затем
  endpoint приложения, `Insecure: true` и обязательные resource attributes;
- Loki отклоняет log: не превышайте 256 KiB, уберите чувствительные и
  высококардинальные attributes, проверьте OTLP path `/v1/logs` у Alloy;
- trace находится, а переход к log пуст: log обязан нести тот же `trace_id` и
  попадать в окно времени span ±2 минуты;
- Grafana не видит data source/dashboard: выполните `task observability:config`,
  затем `task observability:down && task observability:up`; provisioning
  является read-only и восстанавливается из репозитория;
- corrupted local data или закончившийся диск: выполните
  `task observability:clean`, затем `task observability:up`; удалённые volumes
  восстановить нельзя.
