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

Это правило относится к обычному `DockerDriver`. `TopologyDriver` не ослабляет
его через публичные параметры: пакетный непубличный scope-validator принимает
только исходный `targets/v1` snapshot MM-38, точное совпадение topology labels и
успешный свежий `targets validate --expected-state running`. Внешний Driver не
может подменить этот validator. Adapter не должен читать или менять текущий
пользовательский kube context, host network, существующий cluster или ресурс
вне этих двух закрытых ownership-контрактов. Partition задаётся peer network
references, а не произвольным CIDR или shell fragment.

`DockerDriver` запрашивает у Docker только ID, имя, три обязательных label,
network endpoints и IPAM, не читая `Config.Env`. Он повторно связывает `ethN`
с private subnet перед mutation и запускает фиксированный `docker` binary без
shell. Degradation
использует один root `netem` и отказывается перетирать любой qdisc, кроме
исходного `noqueue`. Partition использует точные IPv4/IPv6 prefixes из snapshot
и run-scoped comments firewall. Частичное применение возвращает cleanup для уже
добавленных правил.

`TopologyTargetClient` один раз разрешает все четыре targets для конкретного
`mm36-*` run и сохраняет original token. Fresh resolve и rebind внутри network
run запрещены. Перед resolve/validate и сразу после них клиент сверяет имя
Docker context с заранее закреплённым локальным `unix://` endpoint; сами
destructive Docker-команды используют `--host` с этим endpoint, а не изменяемое
ambient context. Любое расхождение token, container/network/interface binding,
ownership, schema/digest или context endpoint останавливает mutation и cleanup.

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

Опциональный `Observer` получает bounded `before`, `active` и `recovered`
события каждого fault, включая индекс и число faults в Step. Это позволяет
test-only E2E harness записать markers MM-31 для всех одновременных mutations,
но ждать steady sample только после последнего active/recovered marker. Ошибка
observer не отменяет обязательную попытку restore.

Diagnostics получает только seed, порядковый номер/имя Step и фазу. Payload,
token, certificate, request ID, PII и высококардинальные labels в этот контракт
не входят. Adapter сохраняет Kubernetes events, безопасные logs и resource
snapshots в каталог конкретного run до удаления ресурсов.

`ResourceSampler` строит ordered ledger параллельно soak traffic через явный
`ResourceSource`. Baseline читается немедленно, каждое последующее чтение имеет
собственный timeout, а общий Run context обязан иметь deadline не дальше 24
часов. Только отдельный закрытый stop-канал считается штатным завершением;
deadline, source error, менее двух samples и достижение `MaxSamples` до stop
дают failure с сохранением уже собранного ledger.

`EvaluateResources` проверяет ordered ledger goroutine, heap и трёх
фиксированных queue classes: `control`, `auth`, `realtime`. Любое промежуточное
превышение остаётся failure, даже если следующий sample вернулся в норму. Это
запрещает скрывать нестабильность повторным «flaky pass».
`WriteReplayManifest` сохраняет версионированный JSON с seed и фактической
последовательностью faults, но без Docker IDs: новый запуск обязан заново
разрешить disposable resources.

Запрет объявлять flaky run успешным не дублируется в этом пакете: merged
исполняемый контракт MM-27 `e2e/tunnel/spec.Evaluate` считает любой retry,
missing или unknown неуспешным SLO sample. Если scenario вынесен из required
gate, `ValidateQuarantine` требует явные `@owner`, причину и будущий expiry не
дальше 30 дней; quarantine остаётся метаданными и не переписывает итоговый
SLO report.

`HTTPCapacitySource` подключает публичный readiness-контракт MM-30 без импорта
его internal package. Adapter принимает ровно два разных literal
private/loopback HTTP URL с явным непривилегированным port и точным `/readyz`,
проверяет оба DC одновременно и считает только ответ `200 OK`. Redirect,
ambient proxy, compression и DNS запрещены; timeout и тело ответа ограничены,
а transport/read error останавливает gate вместо молчаливого уменьшения
capacity.

## Интеграция и оставшиеся зависимости

- workloads/PKI MM-29, topology MM-28 и independently reviewed исправления
  публичного MM-38 `targets/v1` уже объединены; thin immutable target consumer
  реализован без копирования topology lifecycle;
- health-aware capacity MM-30 подключена; continuous probe MM-31 с
  исправлениями integrity/bounded artifacts MM-39/MM-40 доступен для test-only
  marker/steady adapter;
- service churn MM-33 доступен; pod/rolling/DC churn ожидает MM-32/MM-34/MM-35;
- SLO availability/recovery/error-budget gate и JSON/JUnit берутся напрямую из
  merged MM-27 без локальной копии;
- permanent PR/scheduled GitHub Actions gate — после MM-6. До этого действует
  зафиксированное в проекте MarketMesh процессное исключение: PR/merge разрешены
  только после полного эквивалентного набора локальных проверок, независимого
  review и фиксации результатов в PR.

До объединения остальных scenario adapters destructive suite не запускается.
Сам пакет проверяется независимо:

```bash
cd platform
GOWORK=off go test -race ./testkit/networkchaos
```
