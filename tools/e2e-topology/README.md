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
task e2e:topology:targets -- resolve --consumer-task MM-36 --consumer-run-id mm36-example
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

Inventory v1 остаётся обратно совместимым: `api_version`, существующие поля и
семантика не изменены. MM-38 добавляет только `target_api_version` и две команды
`targets_resolve`/`targets_validate`. Старый потребитель, игнорирующий неизвестные
JSON-поля, продолжает работать без изменений. Additive command metadata включает
`targets_resolve`, `targets_validate` и `targets_rebind`.

## Immutable fault targets v1

Fault orchestration не входит в topology tool. Команды MM-38 только читают
Docker/Kubernetes runtime и публикуют target при полном однозначном совпадении.
Snapshot привязывается к конкретной задаче и уникальному run ID; run ID должен
начинаться с нормализованного ключа задачи, например `mm35-` или `mm36-`.

Разрешить оба target одного DC:

```bash
go run ./tools/e2e-topology \
  --instance mm38-check \
  --docker-context default \
  targets resolve \
  --consumer-task MM-35 \
  --consumer-run-id mm35-check-001 \
  --dc dc-a
```

Без `--dc` resolver требует и возвращает ровно четыре target. `--zone` допустим
только вместе с exact `--dc`. Версия документа —
`marketmesh.dev/e2e-topology/targets/v1`. Для каждого target snapshot содержит:

- exact logical/resource cluster, DC, zone, kubeconfig/context и namespace;
- полный immutable container ID, exact container name, pinned image reference,
  immutable image ID и exact kind cluster label;
- Kubernetes node name, UID и allowlist ownership labels instance/DC/zone;
- все ожидаемые network attachments: у DMZ node одна primary DMZ network, у
  internal node — primary internal и DMZ network того же DC;
- полный network ID, name, bridge/local mode, subnet и MM-28 ownership labels;
- endpoint ID/network ID, private address/prefix, gateway и MAC, одинаковые в
  `container inspect` и `network inspect`;
- единственный `ethN` с exact ifindex/MAC/address и bounded netns identity вида
  `net:[digits]`, полученную фиксированным `readlink /proc/self/ns/net` внутри
  exact container ID;
- canonical `sha256:<64 hex>` token. Token обнаруживает изменение snapshot и
  связывает validation receipt с исходным документом, но не является
  authentication secret.

Snapshot не содержит kubeconfig bytes, environment dump, host netns path,
credentials, workload payload или logs. Resolver не использует shell, glob,
Docker current context, kube current context или вычисленные потребителем имена.
Все subprocesses получают explicit argv, sanitized environment, deadline и
ограниченный output.

Повторная проверка читает только один bounded snapshot со stdin; path input
намеренно запрещён, чтобы исключить symlink/path replacement между проверкой и
чтением:

```bash
go run ./tools/e2e-topology \
  --instance mm38-check \
  --docker-context default \
  targets validate \
  --snapshot - \
  --expected-state running \
  --target dc-a-internal <target-snapshot.json
```

`--target` можно повторить. Без него проверяются все target исходного snapshot;
subset позволяет безопасно валидировать target непосредственно перед его
операцией, не завися от несвязанного кластера. Validator никогда не ищет замену
по имени: container и network повторно inspect-ятся только по ID snapshot, после
чего сверяются name, image, labels, endpoint, membership с обеих сторон,
interface, netns и Kubernetes UID/labels. Unknown/duplicate JSON fields,
trailing document, неправильная cardinality/selector/token, stale ID,
неоднозначный interface и любое частичное inspect приводят к non-zero exit и
нулевому stdout. Успех возвращает bounded receipt версии
`marketmesh.dev/e2e-topology/target-validation/v1` с тем же token, exact logical
targets, observed state и `validated=true`; IDs для mutation всё равно берутся
только из исходного snapshot.

Для network fault используется `--expected-state running`: live endpoint,
sandbox, interface, netns, container generation (`StartedAt`) и Kubernetes
identity обязательны. Для stop/start сценария `--expected-state stopped`
требует exact status `exited`, тот же `StartedAt`, валидный `FinishedAt`, пустые
live endpoint/sandbox fields и отсутствие container в live membership каждого
network. Immutable container/image/network/ownership продолжают проверяться.

Docker 29.4 при `stop → start` сохраняет container ID, network ID и IP, но
легитимно создаёт новые EndpointID, SandboxID, MAC и interface/netns binding.
Поэтому старый running snapshot после start обязан стать stale. Принимать новую
binding обычным resolve запрещено. Для этого есть единственный explicit
state-transition API:

```bash
go run ./tools/e2e-topology \
  --instance mm38-check \
  --docker-context default \
  targets rebind \
  --transition - \
  --target dc-a-internal <rebind-input.json
```

`rebind-input.json` содержит ровно `snapshot` и полученный непосредственно перед
start `stopped_receipt`. Rebind повторно inspect-ит только original container ID
и original network IDs и требует:

- exact container name/image/cluster label и exact network names/subnets/labels;
- отсутствие дополнительных attachments, тот же private IP/gateway и тот же
  Kubernetes UID/context/ownership;
- `StartedAt` новой running generation строго после подтверждённого `FinishedAt`;
- running container `FinishedAt` равен stopped receipt, поэтому receipt нельзя
  replay после второго stop/start;
- stopped receipt имеет valid canonical digest, exact old snapshot token,
  target/container/network IDs и был выдан после `FinishedAt`.

Разрешённый diff ограничен live EndpointID/SandboxID/MAC/interface/netns.
Результат содержит полный новый snapshot/token и transition receipt с
`from_token`, `to_token`, target, new `StartedAt` и stopped receipt digest. Одна
и та же фактическая binding с тем же old snapshot/receipt даёт тот же token и
transition digest, поэтому повтор после потерянного stdout безопасен. Вызов с
уже обновлённым snapshot и старым receipt, второй restart или любое static
изменение завершаются fail-closed без нового snapshot.

Типовой consumer flow:

1. Один раз получить snapshot после `ready` и сохранить его token в bounded run
   ledger.
2. Перед первой mutation проверить все участвующие target.
3. Непосредственно перед каждой mutation проверить exact target/subset по тому же
   исходному snapshot и сверить token receipt.
4. Выполнить mutation структурированным argv только по immutable ID snapshot.
5. После stop получить stopped receipt. После start вызвать rebind с old snapshot
   и этим receipt, сохранить token chain и немедленно `validate running` уже
   нового snapshot. MM-36, не останавливающая container, rebind не использует.
6. Непосредственно перед cleanup снова проверить актуальный snapshot/token;
   обычный resolve во время fault/cleanup запрещён.

Snapshot намеренно не имеет TTL: истечение времени не должно блокировать
restorative cleanup. Staleness определяется заменой immutable runtime identity и
повторным inspect. Любое несовпадение означает fail-closed: consumer не выполняет
destructive команду и сохраняет diagnostics.

Риски MM-38 ограничены расширением публичного JSON и дополнительными read-only
inspect/exec вызовами. Rollback — удалить additive target fields/commands и
вернуться к inventory v1; созданные MM-28 resources и их данные не мигрируются.
Если rollback происходит во время fault-сценария, сначала consumer обязан
восстановить fault по уже сохранённому и повторно проверенному snapshot, затем
вызвать public `down`. Нельзя заменять этот порядок вычислением имён или чтением
`.cache` internals.

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
