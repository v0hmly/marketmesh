# Network chaos testkit

Пакет задаёт общий безопасный lifecycle сетевых fault-сценариев MM-36. Он
намеренно не знает устройство будущего multi-DC стенда, формат availability
report и команды конкретного container runtime. Эти части должны подключаться
через небольшие adapters после слияния MM-27–MM-35 в `dev`, а не копироваться из
их task-веток.

## Контракт ресурсов

Каждый Docker container и network перед mutation задаётся одновременно точным
именем и полным 64-символьным immutable ID. `Driver.Inspect` непосредственно
перед `Apply` обязан вернуть фактический snapshot. Runner разрешит mutation
только если:

- имя начинается с уникального `mm36-<run-id>-`;
- labels равны `com.marketmesh.e2e.task=MM-36`,
  `com.marketmesh.e2e.run=<run-id>` и
  `com.marketmesh.e2e.disposable=true`;
- ID и имя точно совпадают с запрошенными;
- interface имеет явное имя `ethN`;
- все primary и peer networks имеют только private test subnets.

Adapter не должен читать или менять текущий пользовательский kube context,
host network, существующий cluster или ресурс без этих labels. Partition
задаётся peer network references, а не произвольным CIDR или shell fragment.
Docker adapter MM-36 должен строить `tc`/filter команды только из проверенного
snapshot и применять их к указанному interface внутри disposable container
network namespace.

## Lifecycle

Plan содержит `Seed` и упорядоченные Steps. Количество Steps/faults, параметры
degradation и все durations ограничены сверху. В одном Step faults действуют
одновременно; Steps выполняются последовательно. Одноразовый Runner не допускает
два конкурентных запуска в одном network scope. Перед первым `Apply` Runner
проверяет declared `CapacityLoss` всех faults и отклоняет Step, который может
снизить capacity ниже `MinimumCapacity`.

Каждый inspect, apply, diagnostics, restore и recovery имеет отдельный deadline.
При частичном apply adapter возвращает идемпотентный `RestoreFunc` вместе с
ошибкой. Runner сначала вызывает `Diagnostics.Capture`, затем восстанавливает
faults в обратном порядке и ждёт steady state. Cleanup использует
`context.WithoutCancel`, но остаётся bounded собственными deadlines.

Diagnostics получает только seed, порядковый номер/имя Step и фазу. Payload,
token, certificate, request ID, PII и высококардинальные labels в этот контракт
не входят. Adapter сохраняет Kubernetes events, безопасные logs и resource
snapshots в каталог конкретного run до удаления ресурсов.

## Отложенная интеграция

- topology/runtime adapter — после MM-28 и MM-29;
- health-aware capacity и continuous probe — после MM-30 и MM-31;
- pod/service/rolling/DC churn — композиция adapters после MM-32–MM-35;
- SLO availability/recovery/error-budget gate и JSON/JUnit — после MM-27;
- permanent PR/scheduled GitHub Actions gate — только после отдельного
  подтверждения и слияния MM-6.

До появления этих зависимостей пакет можно проверить независимо:

```bash
cd platform
GOWORK=off go test -race ./testkit/networkchaos
```
