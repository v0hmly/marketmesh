# Disposable двух-DC topology

Инструмент MM-44 создаёт локальный Kubernetes E2E-стенд из четырёх одноразовых
OrbStack VM с k3s и владеет только их lifecycle, доставкой образов,
diagnostics и проверкой зональной изоляции. Application workloads, PKI tunnel
и fault injection входят в соседние задачи и здесь отсутствуют. Архитектурное
решение зафиксировано в
[ADR-0014](../../docs/adr/0014-orbstack-vm-k3s-e2e-topology.md).

## Ресурсы и идентичности

Логическая topology фиксирована, а реальные имена VM получают уникальный
префикс instance. Для instance по умолчанию `mm44` создаются:

| Логический кластер | OrbStack VM | Зона |
| --- | --- | --- |
| `dc-a-dmz` | `mm44-dc-a-dmz` | DMZ dc-a |
| `dc-a-internal` | `mm44-dc-a-internal` | internal dc-a |
| `dc-b-dmz` | `mm44-dc-b-dmz` | DMZ dc-b |
| `dc-b-internal` | `mm44-dc-b-internal` | internal dc-b |

Каждая VM — `ubuntu:24.04` с 2 vCPU, 2 GiB памяти и 20 GiB диска; OrbStack
назначает VM частный IPv4 из общего L2 (например `192.168.139.0/24`), видимый
с host и между VM. Имя узла k3s совпадает с именем VM. Узлы и namespace
`marketmesh-system` получают labels `marketmesh.dev/cluster`,
`marketmesh.dev/dc`, `marketmesh.dev/zone`,
`marketmesh.dev/owner-task=MM-44` и
`marketmesh.dev/topology-instance=<instance>`. Полная workload identity
будущих задач имеет формат `<pod>/<namespace>/<logical-cluster>` и не включает
IP, credentials или payload.

В каждой VM работает k3s server с отключёнными traefik, servicelb и
metrics-server; kubeconfig записывается k3s с правами `0600`, извлекается на
host, переписывается на `https://<vm-ip>:6443` и context
`<instance>-<logical>`. NodePort-диапазон по умолчанию; tunnel port `30443` и
HTTP port `30080` сохраняются.

Зональная изоляция выполняется идемпотентными iptables-цепочками внутри VM.
Все четыре VM делят один L2-сегмент OrbStack, поэтому действует полная
mesh-политика (паритет kind-модели, где cross-DC трафик был физически
невозможен из-за разных Docker-сетей): каждая VM направляет INPUT от IPv4
каждой из трёх других VM в свою цепочку (`MM44-DMZ-IN` или `MM44-INT-IN`).
Цепочка сначала принимает ESTABLISHED,RELATED (ответы на исходящий разрешённый
tunnel), затем:

- DMZ VM дополнительно принимает TCP на tunnel port `30443` только от internal
  VM того же DC;
- всё остальное VM→VM отбрасывается: cross-DC internal → DMZ, DMZ ↔ DMZ,
  internal ↔ internal и любые DMZ-initiated соединения.

Трафик host (kubectl на 6443, front door на 30080) не ограничивается. Правила
не сохраняются между запусками: VM одноразовые.

Базовый образ OrbStack `ubuntu:24.04` не содержит ни iptables, ни nft. Поэтому
`up` после установки k3s и до применения изоляции ставит firewall toolchain в
каждую VM: проверка `iptables --version`, при отсутствии — `apt-get update` и
`apt-get install -y --no-install-recommends iptables=1.8.10-3ubuntu2`
(pinned версия из noble/main; iptables 1.8.x в noble — это iptables-nft поверх
nf_tables, совместимый с используемым синтаксисом), затем обязательная
повторная проверка версии (`iptables v1.8.10...`). При смене candidate в
репозитории Ubuntu команда падает — это осознанный fail-fast, требующий
пересмотра пина, а не молчаливого drift. Шаг требует egress VM в репозиторий
Ubuntu во время `up`.

`ready` кратковременно устанавливает статически собранный `mm44-tcpprobe` в
точные VM и запускает его transient systemd units с автоматическим
завершением максимум через 20 секунд. Probe не является Kubernetes workload и
не передаёт payload; listeners группируются по VM и поднимаются по одному
разу на порт. Проверка доказывает разрешённый `internal → DMZ:30443` того же
DC и запрещённые `internal → DMZ:30444`, `DMZ → internal:30443`, cross-DC
`internal → DMZ:30443` и `DMZ ↔ DMZ:30443` для обоих DC.

## Зафиксированный toolchain

| Компонент | Версия |
| --- | --- |
| k3s | `v1.36.4+k3s1` (Kubernetes 1.36.4) |
| kubectl | `v1.37.0` |
| OrbStack | `orbctl` установленной версии (разработано на v2.2.3) |

`bootstrap` скачивает официальные k3s (linux, архитектура host — OrbStack VM
всегда совпадает с ней) и kubectl binaries только по HTTPS, разрешает redirect
только на GitHub/Kubernetes hosts, ограничивает размер и проверяет
зафиксированный SHA-256 до атомарной установки в игнорируемый
`.cache/e2e-topology/<instance>/bin`. TCP-probe собирается из этого Go-модуля
с `CGO_ENABLED=0`, `GOOS=linux`, `GOWORK=off`; сторонних Go-зависимостей
модуль не добавляет. `bootstrap` также проверяет наличие `orbctl`.

Перед обновлением любой версии обязательны review license/changelog,
совместимость CLI, проверка известных уязвимостей, unit/race/security checks
и два полных lifecycle run.

## Требования

- macOS с OrbStack (Linux-машины OrbStack) на `amd64`/`arm64`;
- архитектура VM совпадает с архитектурой host;
- не менее 8 CPU, 12 GB RAM и 20 GB свободного диска для одновременной работы
  четырёх VM;
- доступ к GitHub и `dl.k8s.io` во время bootstrap; Docker CLI на host для
  `load-images`.

Инструмент предназначен только для уникальных disposable ресурсов. Его нельзя
запускать против dev, preprod или production. Для другого локального запуска
укажите отдельный валидный instance, например:

```bash
go run ./tools/e2e-topology --instance mm44-ci1 up
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
VM и fail-closed отклоняет машину с неверным именем, образом или архитектурой.
`down` сначала вызывает `inspect`, затем перед каждым delete повторно
подтверждает точное имя. Никакие globs, пользовательский kube current context
или несвязанные ресурсы не используются.

`verify` выполняет последовательность `up → ready → targets → down` два раза.
При ошибке `up` сначала собирает diagnostics, затем удаляет только
подтверждённые ресурсы этого instance.

## Доставка образов

Команда `load-images` закрывает доставку локально собранных образов во все
четыре кластера, не выходя за границы ownership VM:

```bash
go run ./tools/e2e-topology load-images \
  --image marketmesh/gateway-in:mm29-<sha12> \
  --image marketmesh/gateway-out:mm29-<sha12> \
  --image marketmesh/fake-internal:mm29-<sha12>
```

Для каждого образа выполняется `docker save` на host в staging-каталог
state, tar передаётся в каждую VM, импортируется в containerd k3s
(`k3s ctr -n k8s.io images import`) и удаляется из VM; staging tar на host
также удаляется. После импорта `load-images` fail-closed проверяет, что
точный тег (с учётом нормализации containerd до `docker.io/`) виден в
`k3s ctr -n k8s.io images ls` каждой VM. Манифесты E2E workloads используют
`imagePullPolicy: Never`, поэтому пропущенная загрузка обнаруживается сразу.
Обертка — `task tunnel-e2e:load`, версия образа вычисляется из HEAD.

## Versioned inventory

После успешного `up` команда `task e2e:topology:inventory` проверяет
ownership работающих VM и выводит JSON с
`api_version=marketmesh.dev/e2e-topology/v1`. До `up` она завершается ошибкой,
не подставляя вычисленные адреса. Тот же документ атомарно записывается с
правами `0600` в `.cache/e2e-topology/<instance>/inventory.json`. Для каждого
кластера он содержит:

- логическое и реальное resource name;
- DC/zone и проверенный control-plane IPv4 (адрес VM);
- абсолютный путь к отдельному kubeconfig;
- точное имя context `<instance>-<logical>`;
- namespace, workload identity format и gateway-in tunnel port `30443`;
- runtime `orbstack-vm+k3s`, ownership metadata и bounded команды `ready`,
  `inspect`, `down`.

Соседние E2E-задачи должны читать inventory v1, вызывать kubectl только с
парой `--kubeconfig`/`--context` из него и проверять ownership до любой
destructive операции. После полностью успешного `down` inventory удаляется,
чтобы потребитель не мог использовать устаревшие адреса; при частичном cleanup
он сохраняется для диагностики. Копировать lifecycle или вычислять пути
самостоятельно запрещено.

Inventory v1 сохраняет `api_version`; MM-44 заменяет поле `docker_context` на
`runtime` и убирает docker network из записей кластеров. Внешних читателей
формата нет: потребители получают значения флагами. Additive command metadata
включает `targets_resolve`, `targets_validate` и `targets_rebind`.

## Immutable fault targets v1

Fault orchestration не входит в topology tool. Команды MM-38 только читают
OrbStack/Kubernetes runtime и публикуют target при полном однозначном
совпадении. Snapshot привязывается к конкретной задаче и уникальному run ID;
run ID должен начинаться с нормализованного ключа задачи, например `mm35-` или
`mm36-`.

Разрешить оба target одного DC:

```bash
go run ./tools/e2e-topology \
  --instance mm44-check \
  targets resolve \
  --consumer-task MM-35 \
  --consumer-run-id mm35-check-001 \
  --dc dc-a
```

Без `--dc` resolver требует и возвращает ровно четыре target. `--zone`
допустим только вместе с exact `--dc`. Версия документа —
`marketmesh.dev/e2e-topology/targets/v1`. Для каждого target snapshot
содержит:

- exact logical/resource cluster, DC, zone, kubeconfig/context и namespace;
- immutable machine ID (ULID) OrbStack, exact machine name, текущий private
  IPv4, MAC и имя первого non-loopback интерфейса, несущего этот IPv4;
- boot ID (`/proc/sys/kernel/random/boot_id`) как поколение запуска VM: любой
  reboot делает старый running snapshot stale;
- Kubernetes node name, UID и allowlist ownership labels instance/DC/zone;
- canonical `sha256:<64 hex>` token. Token обнаруживает изменение snapshot и
  связывает validation receipt с исходным документом, но не является
  authentication secret.

Snapshot не содержит kubeconfig bytes, environment dump, credentials, workload
payload или logs. Resolver не использует shell, glob, kube current context или
вычисленные потребителем имена. Все subprocesses получают explicit argv,
sanitized environment, deadline и ограниченный output.

Повторная проверка читает только один bounded snapshot со stdin; path input
намеренно запрещён, чтобы исключить symlink/path replacement между проверкой и
чтением:

```bash
go run ./tools/e2e-topology \
  --instance mm44-check \
  targets validate \
  --snapshot - \
  --expected-state running \
  --target dc-a-internal <target-snapshot.json
```

`--target` можно повторить. Без него проверяются все target исходного
snapshot; subset позволяет безопасно валидировать target непосредственно перед
его операцией, не завися от несвязанного кластера. Validator никогда не ищет
замену по имени: машина повторно inspect-ится по имени из snapshot с
обязательной сверкой immutable machine ID, после чего сверяются state, IPv4,
MAC/interface, boot ID и Kubernetes UID/labels. Unknown/duplicate JSON fields,
trailing document, неправильная cardinality/selector/token, stale ID,
неоднозначный interface и любое частичное inspect приводят к non-zero exit и
нулевому stdout. Успех возвращает bounded receipt версии
`marketmesh.dev/e2e-topology/target-validation/v1` с тем же token, exact
logical targets, observed state и `validated=true`; IDs для mutation всё равно
берутся только из исходного snapshot.

Для network fault используется `--expected-state running`: машина обязана
работать с неизменными IPv4, MAC, boot ID и Kubernetes identity. Network chaos
(tc netem/iptables) выполняется consumer-задачами через `orbctl run` внутри
конкретной VM; topology tool даёт только resolve/validate identity. Для
stop/start сценария `--expected-state stopped` требует exact state `stopped`
и тот же immutable machine ID; stopped машина не имеет live execution handle,
поэтому stopped-валидация не выполняет ни одного `orbctl run` внутрь VM.

OrbStack при `stop → start` сохраняет machine ID, MAC и IPv4, но назначает
новый boot ID и сбрасывает netfilter state: iptables-цепочки зон не переживают
restart VM. Поэтому старый running snapshot после start обязан стать
stale. Принимать новое поколение обычным resolve запрещено. Для этого есть
единственный explicit state-transition API:

```bash
go run ./tools/e2e-topology \
  --instance mm44-check \
  targets rebind \
  --transition - \
  --target dc-a-internal <rebind-input.json
```

`rebind-input.json` содержит ровно `snapshot` и полученный непосредственно
перед start `stopped_receipt`. Rebind повторно inspect-ит только original
machine и требует:

- тот же immutable machine ID, точное имя, тот же private IPv4 и тот же
  MAC/interface;
- новый boot ID, отличный от snapshot (доказанный переход
  stopped → running);
- тот же Kubernetes UID/context/ownership;
- stopped receipt имеет valid canonical digest, exact old snapshot token,
  target/machine ID и exact state `stopped`.

После доказательства identity rebind восстанавливает работоспособность
rebound VM, прежде чем выдать новый snapshot: при необходимости доустанавливает
pinned firewall toolchain, пересоздаёт iptables-цепочку и все три jump-правила
rebound VM (peer-правила соседей трогать не нужно — IPv4 машины не изменился)
и ждёт `Ready` узла k3s через bounded `kubectl wait`. Ошибка восстановления
firewall или readiness завершает rebind fail-closed, без нового snapshot.

Результат содержит полный новый snapshot/token и transition receipt с
`from_token`, `to_token`, target, machine ID, новым boot ID и stopped receipt
digest. Одна и та же фактическая привязка с тем же old snapshot/receipt даёт
тот же token и transition digest, поэтому повтор после потерянного stdout
безопасен. Вызов с уже обновлённым snapshot и старым receipt или любое static
изменение завершаются fail-closed без нового snapshot.

Типовой consumer flow:

1. Один раз получить snapshot после `ready` и сохранить его token в bounded
   run ledger.
2. Перед первой mutation проверить все участвующие target.
3. Непосредственно перед каждой mutation проверить exact target/subset по тому
   же исходному snapshot и сверить token receipt.
4. Выполнить mutation структурированным argv только по immutable identity
   snapshot.
5. После stop получить stopped receipt. После start вызвать rebind с old
   snapshot и этим receipt, сохранить token chain и немедленно
   `validate running` уже нового snapshot. MM-36, не останавливающая VM,
   rebind не использует.
6. Непосредственно перед cleanup снова проверить актуальный snapshot/token;
   обычный resolve во время fault/cleanup запрещён.

Snapshot намеренно не имеет TTL: истечение времени не должно блокировать
restorative cleanup. Staleness определяется заменой immutable runtime identity
и повторным inspect. Любое несовпадение означает fail-closed: consumer не
выполняет destructive команду и сохраняет diagnostics.

Если rollback происходит во время fault-сценария, сначала consumer обязан
восстановить fault по уже сохранённому и повторно проверенному snapshot, затем
вызвать public `down`. Нельзя заменять этот порядок вычислением имён или
чтением `.cache` internals.

## Diagnostics и cleanup

`inspect` создаёт каталог
`.cache/e2e-topology/<instance>/diagnostics/<UTC timestamp>` с правами `0750`.
Каждый файл имеет права `0600`. Сбор ограничен четырьмя MiB на subprocess и
bounded timeout; сохраняются только:

- список машин OrbStack и `orbctl info` каждой VM instance;
- последние 200 строк журнала k3s и вывод `iptables -L -n -v` каждой VM;
- Kubernetes nodes, topology namespace и максимум одна bounded выборка events;
- summary без payload, secrets, PII и высококардинальных metric labels.

Secrets, ConfigMaps и application workload logs не собираются. Сбор
best-effort на уровне артефакта: ошибка одного захвата (например, journalctl
на VM без установленного k3s) записывается в соседний файл `<имя>.err` и не
отменяет остальные артефакты; `inspect` возвращает ошибку, только если
диагностический прогон нельзя создать или записать summary. Diagnostics
сохраняются после `down`; VM и kubeconfig удаляются.
