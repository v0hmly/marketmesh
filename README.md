# MarketMesh

MarketMesh — онлайн-магазин и платформа, где мастера могут продавать изделия собственного производства. Мастер изготавливает определённое количество товара, размещает предложение на платформе и продаёт готовые изделия покупателям. Это могут быть как изделия ручной работы, так и товары, созданные с помощью оборудования.

Например, мастер покупает вышивальную машину, выпускает небольшую партию футболок с вышитыми рисунками и выставляет их на MarketMesh. В предложении указаны фотографии, описание, цена и доступное количество; покупатели выбирают изделия из этой партии.

[Описание продукта](docs/product/overview.md) раскрывает целевую модель и основной пользовательский сценарий.

Проект разрабатывается как образовательный, но рассчитанный на жизнеспособную платформу: Go-микросервисы, модульный Vue frontend и инфраструктура с разделением на DMZ и внутреннюю зону.

## Структура

```text
backend/     Go workspace: сервисы, библиотеки, генерация, инструменты и тесты
frontend/    pnpm workspace: приложения, пакеты, генерация и TS/JS-инструменты
api/         protobuf-схемы, EasyP и скрипты генерации
infra/       Docker Compose, Kubernetes и подготовка окружений
tools/       общепроектная автоматизация CI и Taskboard
docs/        продукт, архитектура, дизайн и ADR
```

Go-часть организована как multimodule workspace. Каждый сервис имеет собственный `go.mod`; общие библиотеки и сгенерированные контракты также отделены модульными границами. Детали зафиксированы в [ADR-0012](docs/adr/0012-monorepository-and-go-workspace.md).

Внутри доменных сервисов используется прагматичная гексагональная архитектура из [ADR-0011](docs/adr/0011-go-service-hexagonal-architecture.md).

## Требования

- Go 1.27.0;
- Node.js 24.19 или новее;
- pnpm 11.19.0;
- Task 3.53 или новее;
- Docker 29 с Docker Compose v2 либо совместимая версия OrbStack;
- OpenSSL для генерации локальных development credentials;
- Git, curl, tar и unzip — для bootstrap и самопроверки protobuf toolchain.

EasyP, `protoc` и плагины protobuf устанавливаются в игнорируемый корневой каталог `bin` командой `task api:bootstrap`; архивы загрузок и зависимости остаются в `.cache`. Системная установка и версии `latest` не используются.

## Полный локальный запуск

Из корня репозитория при запущенном OrbStack:

```bash
task dev:up
```

Команда собирает storefront, Auth, User и gateway, поднимает Files с AV/CDR,
PostgreSQL, Redis, NATS, Mailpit, Grafana Stack и локальную аналитику Rybbit.
Она создаёт конфигурацию, применяет миграции, ждёт готовности и при необходимости
обновляет сертификаты. Ручные переменные окружения не нужны. Данные сохраняются
между запусками. Короткий синоним — `task up`.

- Кабинет: https://localhost:18443
- Письма и коды подтверждения: http://localhost:18025
- Grafana: http://localhost:3000
- Rybbit: http://localhost:8310

`task dev:status` показывает состояние, `task dev:down` останавливает стенд без
удаления данных. Первый запуск собирает образы и скачивает антивирусные базы.
Браузеру требуется доверие локальным CA; пути к сертификатам выводятся после
запуска. Подробности и границы стенда — [infra/dev](infra/dev/README.md).

## Команды из корня

```bash
task fmt        # отформатировать Go-код
task fmt-check  # проверить форматирование
task vet        # выполнить go vet во всех модулях
task test       # выполнить модульные тесты
task test-race  # выполнить тесты с race detector
task build      # собрать все Go-пакеты
task arch       # проверить модульные пути и запрещённые зависимости
task api:verify # проверить protobuf workflow и контракты
task verify     # выполнить обязательный локальный набор проверок workspace
task infra:up   # запустить локальную инфраструктуру
task infra:smoke # проверить PostgreSQL, DMZ и observability pipeline
task account:up # запустить кабинет с настоящими Auth и User
task account:test # проверить полный сценарий аккаунта в Chromium
task observability:up # запустить только observability-стек
task observability:smoke # проверить связанный trace и log
task observability:outage # проверить bounded queues при недоступных backends
```

Taskfile вызывает `backend/tools/go-workspace.sh` для Go-команд и `api/tools/protobuf.sh` для protobuf-команд.

Frontend workspace находится в `frontend/`. Из корня:

```bash
pnpm --dir frontend build
pnpm --dir frontend test
pnpm --dir frontend lint
pnpm --dir frontend typecheck
```

Эквивалентные команды Task: `task frontend:build`, `task frontend:test`, `task frontend:lint`, `task frontend:typecheck` и `task frontend:verify`.

Vue-приложение кабинета и его проверки описаны в
[storefront](frontend/apps/storefront/README.md). Полный HTTPS-запуск с Auth,
User и браузерными E2E — в [локальном окружении кабинета](infra/account-local/README.md).

Локальная инфраструктура PostgreSQL, Redis, SeaweedFS, Alloy, Tempo, Loki и
Grafana описана в
[руководстве Docker Compose](infra/compose/README.md). Команда
`task infra:verify` запускает окружение и выполняет полный smoke test, включая
проверку persistent volumes после restart.

## Архитектура и процесс

- [Обзор архитектуры](docs/architecture/overview.md)
- [Журнал ADR](docs/adr/README.md)
- [Правила работы с Taskboard и Git Flow](AGENTS.md)
- [CI, проверки безопасности и GitHub Free](docs/ci.md)
