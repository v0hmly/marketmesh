# Network chaos testkit

Пакет задаёт общий безопасный lifecycle сетевых fault-сценариев MM-36. Он
намеренно не знает формат availability report и устройство multi-DC front
door/probe. Эти части подключаются через небольшие adapters после слияния
соответствующих MM-27–MM-35 в `dev`, а не копируются из их task-веток.

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

`DockerDriver` запрашивает у Docker только ID, имя, три обязательных label,
network endpoints и IPAM, не читая `Config.Env`. Он повторно связывает `ethN`
с private subnet перед mutation и запускает фиксированный `docker` binary без
shell. Degradation
использует один root `netem` и отказывается перетирать любой qdisc, кроме
исходного `noqueue`. Partition использует точные IPv4/IPv6 prefixes из snapshot
и run-scoped comments firewall. Частичное применение возвращает cleanup для уже
добавленных правил.

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

`EvaluateResources` проверяет ordered ledger goroutine, heap и трёх
фиксированных queue classes: `control`, `auth`, `realtime`. Любое промежуточное
превышение остаётся failure, даже если следующий sample вернулся в норму. Это
запрещает скрывать нестабильность повторным «flaky pass».
`WriteReplayManifest` сохраняет версионированный JSON с seed и фактической
последовательностью faults, но без Docker IDs: новый запуск обязан заново
разрешить disposable resources.

`GateAttempts` сохраняет failure при любом неуспешном attempt и не позволяет
последующему retry превратить flaky scenario в pass. Если scenario вынесен из
required gate, `ValidateQuarantine` требует явные `@owner`, причину и будущий
expiry не дальше 30 дней; quarantine остаётся метаданными и не переписывает
фактический результат попытки.

## Отложенная интеграция

- workloads/PKI MM-29 уже доступны; фактический Docker topology contract и
  destructive проверка adapter ожидают MM-28;
- health-aware capacity и continuous probe — после MM-30 и MM-31;
- pod/service/rolling/DC churn — композиция adapters после MM-32–MM-35;
- SLO availability/recovery/error-budget gate и JSON/JUnit — после MM-27;
- permanent PR/scheduled GitHub Actions gate — после MM-6. До этого действует
  зафиксированное в проекте MarketMesh процессное исключение: PR/merge разрешены
  только после полного эквивалентного набора локальных проверок, независимого
  review и фиксации результатов в PR.

До появления этих зависимостей пакет можно проверить независимо:

```bash
cd platform
GOWORK=off go test -race ./testkit/networkchaos
```
