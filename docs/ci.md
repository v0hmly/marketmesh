# CI и проверки безопасности

MM-6 вводит один workflow [CI](../.github/workflows/ci.yml) с одним заданием
`verify` на стандартном `ubuntu-24.04`. Он запускается для PR в `dev`, `main`,
`release/*`, `epic/*`, после push в эти ветки и вручную. Новый запуск отменяет
устаревший запуск того же PR/ветки. Лимит задания — 30 минут.

Проверяется весь workspace: изменение `platform` или API всегда проверяет
зависимые сервисы. На первом этапе нет сложных path-фильтров, матрицы ОС/версий,
порога покрытия, платных сервисов ревью и автоматического deployment.

## Состав проверок

| Проверка | Локальная команда |
| --- | --- |
| Синтаксис GitHub Actions, отрицательные проверки самого CI | `./tools/ci.sh workflow-lint`, `./tools/ci-test.sh` |
| Go: форматирование, архитектурные границы, vet, unit-тесты с race detector, сборка | `task ci:go` |
| Каждый Go-модуль без workspace: тесты, сборка, целостность скачанных модулей | входит в `task ci:go` |
| Frontend: frozen lockfile, сборка, тесты, Prettier/ESLint, типы | `task ci:frontend` |
| Protobuf: lint, совместимость, воспроизводимость генерации и self-test | `task ci:api` |
| Gitleaks для новых коммитов, govulncheck всех Go-модулей, pnpm audit для high/critical | `task ci:security` |
| Отсутствие изменений tracked-файлов и новых неигнорируемых файлов | `./tools/ci.sh check-tree` |

Go lint на этом этапе — `gofmt` и `go vet`; дополнительные стилистические
линтеры не вводятся. Race-прогон уже выполняет unit-тесты, отдельный повтор
без race в workspace не нужен. Изолированный прогон дополнительно проверяет
собственные зависимости каждого модуля.

### Изоляция Go-модулей

`backend/tools/go-workspace.sh isolated` использует `GOWORK=off` и `-mod=readonly`.
Каждый модуль собирается и тестируется по собственным `go.mod`/`go.sum`, без
временных `replace`, `go get` или `tidy` в CI. Внутренние зависимости закреплены
на опубликованных commit монорепозитория. Если новый код использует новый API,
нужно обновить соответствующую версию в `require` и `go.sum` до запуска CI.
Workspace отдельно проверяет совместимость всех текущих исходников.

`go mod verify` проверяет целостность скачанных зависимостей; Go checksum
database и проверка integrity пакетов pnpm остаются включены.

### Baseline и безопасность PR

`CI_BASE_REF` — base SHA PR, предыдущий SHA для push или `origin/dev` для
ручного запуска/создания ветки. Отсутствующая ссылка — ошибка, а не пропуск
проверки. Protobuf сравнивает схемы с этим commit. Gitleaks сканирует весь
диапазон новых коммитов, включая секрет, добавленный и затем удалённый;
значения находок скрыты через `--redact`. Исторический аудит секретов до
baseline выполняется отдельно; локальный незакоммиченный diff Gitleaks не сканирует.

Workflow использует `pull_request`, а не `pull_request_target`, только
`contents: read`, без environments и секретов. Checkout не сохраняет credentials.
Нет публикации контейнеров, пакетов или workflow artifacts. Actions закреплены
по полному SHA, версии Go-инструментов — в
[`tools/ci-versions.env`](../tools/ci-versions.env), protobuf — в существующем
[`api/tools/protobuf-versions.env`](../api/tools/protobuf-versions.env). Версии Go, pnpm
и Node соответствуют требованиям репозитория. Кэшируются только зависимости
и Go build cache; кэши не должны содержать секреты.

## Локальный запуск

Нужны Go, Node, pnpm из [README](../README.md), Git, curl, unzip и OpenSSL.

```bash
task ci:bootstrap
pnpm --dir frontend install --frozen-lockfile
CI_BASE_REF=origin/dev task ci:verify
# После commit; чистый checkout обязателен только для этого шага:
./tools/ci.sh check-tree
```

`ci:bootstrap` устанавливает инструменты в игнорируемый `bin`, не в системный
PATH. Базовые команды `task verify` и `task frontend:verify` продолжают работать;
CI использует те же сценарии. При находках безопасности исправляется причина,
а исключения оформляются с причиной, владельцем и сроком.

## GitHub Free и required check

Репозиторий публичный. Стандартные GitHub-hosted runners для него
[бесплатны](https://docs.github.com/en/billing/concepts/product-billing/github-actions).
Платные larger runners не используются. Если репозиторий станет private,
GitHub Free предоставляет 2000 минут в месяц и 500 MB artifacts; бесплатный
cache limit — 10 GB на репозиторий. Не включайте платное превышение лимитов
без отдельного решения. Workflow не требует GitHub Advanced Security.

После первого успешного запуска в rulesets интеграционных веток нужно выбрать
один required status check **`verify`**, источник **GitHub Actions**. Workflow
называется `CI`; в интерфейсе запуск отображается как `CI / verify`.
`strict_required_status_checks_policy` требует актуальной целевой ветки.
Не добавляйте ещё не существующий check независимого ревью.

Остальные настройки соответствуют [AGENTS.md](../AGENTS.md): только PR,
запрет force-push/удаления, разрешённые обсуждения, сброс устаревших approvals.
Для единственного разработчика `required_approving_review_count=0`,
`require_code_owner_review=false`, `require_last_push_approval=false`.
Независимый агент оставляет отчёт с head/base SHA; перед merge его актуальность
проверяется вручную. Сам workflow не выдаёт такого одобрения.

Настройки Actions: read-only token по умолчанию, без разрешения workflow
создавать/одобрять PR; запуски от внешних участников требуют штатного одобрения
GitHub. Для этого CI не нужны secrets или environments. Rulesets доступны
для публичных репозиториев на GitHub Free; при смене visibility нужно
[перепроверить возможности тарифа](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/managing-rulesets/about-rulesets).

## Ограниченный первый этап

По запросу владельца от 22.09.2026 CI вводится без строгого и дорогостоящего
набора проверок. Владелец исключения — `v0hmly`, задача — MM-6, пересмотр
не позднее **22.10.2026**. До этой даты в обязательный hosted workflow не входят:

- Docker builds и сканирование контейнеров: известные находки базовых образов
  уже учитываются в MM-67; образы не изменяются и не публикуются в MM-6.
- Полные Docker/браузерные интеграции, миграции и последовательное развёртывание,
  четырёхкластерные OrbStack E2E/chaos/soak. При изменении соответствующих границ
  необходимые сценарии из Taskfile выполняются локально с результатом в PR.
- Автоматический обход всех ссылок документации: изменённые локальные ссылки
  и структура проверяются при ревью.

Это явное временное ограничение автоматизации, а не утверждение о прохождении
этих проверок. Production deployment и его требования не меняются.

Откат CI — отдельный PR с отменой MM-6 и согласованным изменением required
check; иначе отсутствующий `verify` заблокирует следующие PR. Данные приложений
и deployment workflow не затрагиваются.
