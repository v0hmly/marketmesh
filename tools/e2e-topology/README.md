# Disposable двух-DC topology

Инструмент MM-28 создаёт локальный Kubernetes E2E-стенд из четырёх kind-кластеров
и владеет только их сетями, lifecycle, diagnostics и проверкой зональной
изоляции. Application workloads, PKI tunnel и fault injection входят в соседние
задачи и здесь отсутствуют.

## Ресурсы и идентичности

Логическая topology фиксирована, а реальные имена получают уникальный префикс
instance. Для instance по умолчанию `mm28` создаются:

| Логический кластер | kind cluster и Docker network | Docker subnet | Pod subnet | Service subnet |
| --- | --- | --- | --- | --- |
| `dc-a-dmz` | `mm28-dc-a-dmz` | `172.28.10.0/24` | `10.28.0.0/16` | `10.128.0.0/16` |
| `dc-a-internal` | `mm28-dc-a-internal` | `172.28.11.0/24` | `10.29.0.0/16` | `10.129.0.0/16` |
| `dc-b-dmz` | `mm28-dc-b-dmz` | `172.28.20.0/24` | `10.30.0.0/16` | `10.130.0.0/16` |
| `dc-b-internal` | `mm28-dc-b-internal` | `172.28.21.0/24` | `10.31.0.0/16` | `10.131.0.0/16` |

Docker networks имеют обязательные labels
`com.marketmesh.task=MM-28` и `com.marketmesh.topology=<instance>`. Узлы и
namespace `marketmesh-system` получают labels `marketmesh.dev/cluster`,
`marketmesh.dev/dc`, `marketmesh.dev/zone`,
`marketmesh.dev/owner-task=MM-28` и
`marketmesh.dev/topology-instance=<instance>`. Полная workload identity будущих
задач имеет формат `<pod>/<namespace>/<logical-cluster>` и не включает IP,
credentials или payload.

Internal control-plane каждого DC дополнительно подключается к DMZ network того
же DC. Поскольку `docker network connect` может назначить новый default route,
`up` явно возвращает его на internal interface, а `ready` fail-closed проверяет
единственный internal gateway. Идемпотентные `iptables` chains внутри этого
disposable node разрешают
только исходящее TCP-соединение к gateway-in NodePort `30443`, разрешают ответы на
него и отклоняют:

- новые соединения DMZ → internal;
- internal → DMZ на любом другом порту;
- транзит, не относящийся к разрешённому tunnel port.

`ready` кратковременно копирует статически собранный `mm28-tcpprobe` только в
точные kind-node containers. Probe не является Kubernetes workload, не передаёт
payload и автоматически завершается максимум через 20 секунд. Проверка
доказывает разрешённый `internal → DMZ:30443`, запрещённый
`internal → DMZ:30444` и запрещённый `DMZ → internal:30443` симметрично для
обоих DC.

## Зафиксированный toolchain

| Компонент | Версия |
| --- | --- |
| kind | `v0.33.0` |
| Kubernetes/kubectl | `v1.37.0` |
| node image | `kindest/node:v1.37.0@sha256:a1ed56cfb0e7b93589bdf97c8cd566405a265939e3620fc4f5de89adff580ae5` |

`bootstrap` скачивает официальные kind и kubectl binaries только по HTTPS,
разрешает redirect только на GitHub/Kubernetes hosts, ограничивает размер и
проверяет зафиксированный SHA-256 до атомарной установки в игнорируемый
`.cache/mm28-topology/<instance>/bin`. TCP-probe собирается из этого Go-модуля с
`CGO_ENABLED=0`, `GOOS=linux`, `GOWORK=off`; сторонних Go-зависимостей модуль не
добавляет.

Оценка внешних компонентов:

- kind поддерживается Kubernetes SIG Testing, лицензирован Apache-2.0;
  `v0.33.0` — стабильный release с исправлениями зависимостей и bug fixes;
- kubectl и Kubernetes node image поддерживаются upstream Kubernetes и
  лицензированы Apache-2.0; client и server зафиксированы на одном patch release;
- node image взят из release notes соответствующего kind и закреплён digest;
- custom network выбирается через зафиксированный kind API
  `KIND_EXPERIMENTAL_DOCKER_NETWORK`. Переменная передаётся только дочернему
  kind-process, не экспортируется process-wide. Поскольку upstream всё ещё
  помечает этот интерфейс experimental, обновление kind требует отдельного
  повторного E2E и проверки исходного provider contract.

Security scan от 2026-09-01 выполнен `govulncheck v1.7.0` и `Trivy v0.74.0`.
Новый Go-модуль и standard library Go 1.27.0 не имеют известных достижимых
уязвимостей. В официальных node images kind v0.33.0 Trivy обнаруживает upstream
HIGH/CRITICAL, включая исправимые: для Kubernetes 1.35.8 — 77 срабатываний, для
выбранного Kubernetes 1.37.0 — 45. Более свежего совместимого официального
image без этих срабатываний на дату проверки нет; сборка собственного
непроверенного node image увеличила бы supply-chain риск.

Временное исключение действует только для локального disposable E2E без
production secrets и application workloads. Стенд bounded и полностью удаляется
после проверки. Владелец пересмотра — задача MM-37, срок — 2026-09-15. До этой
даты image нужно повторно просканировать и заменить после upstream security
rebuild; молчаливое продление исключения запрещено.

Перед обновлением любой версии обязательны review license/changelog,
совместимость CLI, новые транзитивные компоненты node image, проверка известных
уязвимостей, unit/race/security checks и два полных lifecycle run.

## Требования

- macOS или Linux на `amd64`/`arm64`;
- Linux Docker engine в явно выбранном context; локально по умолчанию
  используется `orbstack`;
- архитектура Docker engine совпадает с архитектурой host, как требует kind;
- не менее 8 CPU, 12 GB RAM и 20 GB свободного диска для одновременной работы
  четырёх control-plane nodes;
- доступ к GitHub, `dl.k8s.io` и Docker Hub во время bootstrap/первого pull.

Инструмент предназначен только для уникальных disposable ресурсов. Его нельзя
запускать против dev, preprod или production. Для другого локального запуска
укажите отдельный валидный instance, например:

```bash
go run ./tools/e2e-topology --instance mm28-ci1 --docker-context default up
```

## Команды

```bash
task e2e:topology:bootstrap
task e2e:topology:versions
task e2e:topology:inventory
task e2e:topology:up
task e2e:topology:ready
task e2e:topology:inspect
task e2e:topology:down
task e2e:topology:verify
```

Каждый subprocess имеет deadline. `up` идемпотентно проверяет уже существующие
ресурсы и fail-closed отклоняет cluster/container/network с неверным именем,
label, subnet или image. `down` сначала вызывает `inspect`, затем перед каждым
delete повторно подтверждает точное имя и ownership. Никакие globs, пользовательский
kube current context или несвязанные Docker resources не используются.

`verify` выполняет последовательность `up → ready → down` два раза. При ошибке
`up` сначала собирает diagnostics, затем удаляет только подтверждённые ресурсы
этого instance.

## Versioned inventory

После успешного `up` команда `task e2e:topology:inventory` проверяет ownership
работающих node containers и выводит JSON с
`api_version=marketmesh.dev/e2e-topology/v1`. До `up` она завершается ошибкой,
не подставляя вычисленные адреса. Тот же документ атомарно
записывается с правами `0600` в
`.cache/mm28-topology/<instance>/inventory.json`. Для каждого кластера он содержит:

- логическое и реальное resource name;
- DC/zone, Docker network и проверенный control-plane IPv4;
- абсолютный путь к отдельному kubeconfig;
- точное имя context `kind-<resource-name>`;
- namespace, workload identity format и gateway-in tunnel port `30443`;
- ownership metadata и bounded команды `ready`, `inspect`, `down`.

Соседние E2E-задачи должны читать inventory v1, вызывать kubectl только с парой
`--kubeconfig`/`--context` из него и проверять ownership до любой destructive
операции. После полностью успешного `down` inventory удаляется, чтобы потребитель
не мог использовать устаревшие адреса; при частичном cleanup он сохраняется для
диагностики. Копировать lifecycle или вычислять пути самостоятельно запрещено.

## Diagnostics и cleanup

`inspect` создаёт каталог
`.cache/mm28-topology/<instance>/diagnostics/<UTC timestamp>` с правами `0750`.
Каждый файл имеет права `0600`. Сбор ограничен четырьмя MiB на subprocess и
bounded timeout; сохраняются только:

- allowlist безопасных полей Docker info/container state, network inspect и
  one-shot stats;
- последние 500 строк node container logs не старше 30 минут;
- Kubernetes nodes, topology namespace и максимум одна bounded выборка events;
- summary без payload, secrets, PII и высококардинальных metric labels.

Secrets, ConfigMaps и application workload logs не собираются. Diagnostics
сохраняются после `down`; clusters, containers, networks и kubeconfig удаляются.
