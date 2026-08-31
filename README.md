# MarketMesh

MarketMesh — образовательный проект жизнеспособного онлайн-магазина: Go-микросервисы, модульный Vue frontend и инфраструктура с разделением на DMZ и внутреннюю зону.

## Структура

```text
api/         protobuf-схемы и сгенерированные контракты
frontend/    Vue-приложение и frontend-пакеты
infra/       Docker Compose и Kubernetes
platform/    общие технические Go-библиотеки
services/    независимо развёртываемые Go-сервисы
tools/       автоматизация монорепозитория
docs/        архитектурная документация и ADR
```

Go-часть организована как multimodule workspace. Каждый сервис имеет собственный `go.mod`; общие библиотеки и сгенерированные контракты также отделены модульными границами. Детали зафиксированы в [ADR-0012](docs/adr/0012-monorepository-and-go-workspace.md).

Внутри доменных сервисов используется прагматичная гексагональная архитектура из [ADR-0011](docs/adr/0011-go-service-hexagonal-architecture.md).

## Требования

- Go 1.27.0;
- Node.js 24.19 или новее;
- pnpm 11.19.0;
- Task 3.53 или новее;
- GNU Make — для альтернативного интерфейса команд.

## Команды из корня

```bash
task fmt        # отформатировать Go-код
task fmt-check  # проверить форматирование
task vet        # выполнить go vet во всех модулях
task test       # выполнить модульные тесты
task test-race  # выполнить тесты с race detector
task build      # собрать все Go-пакеты
task arch       # проверить модульные пути и запрещённые зависимости
task verify     # выполнить обязательный локальный набор проверок
```

Те же команды доступны через Makefile, например `make test` и `make verify`. Оба интерфейса вызывают единый скрипт `tools/go-workspace.sh`.

Frontend-команды запускаются через корневой pnpm workspace:

```bash
pnpm build
pnpm test
pnpm lint
pnpm typecheck
```

Эквивалентные команды Task: `task frontend:build`, `task frontend:test`, `task frontend:lint`, `task frontend:typecheck` и `task frontend:verify`.

Пока frontend-пакеты не созданы, эти команды завершаются без выполнения вложенных скриптов.

## Архитектура и процесс

- [Обзор архитектуры](docs/architecture/overview.md)
- [Журнал ADR](docs/adr/README.md)
- [Правила работы с Taskboard и Git Flow](AGENTS.md)
