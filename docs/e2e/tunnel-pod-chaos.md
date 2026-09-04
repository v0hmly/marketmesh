# Последовательный pod chaos для tunnel

Этот документ фиксирует сценарный и safety-контракт MM-32. Он не определяет
контракты topology, workload, front door, probe или SLO: сценарий использует их
только после появления соответствующих реализаций в `dev`.

## Цель и границы

Сценарий проверяет аварийный reschedule pod-компонентов `gateway-in` и
`gateway-out` под непрерывным read/mutating потоком. В каждом шаге удаляется
ровно один pod, созданный Deployment текущего одноразового E2E-запуска. Новый
pod должен получить другой UID, пройти readiness, восстановить tunnel и вернуть
исходную доступную ёмкость.

Сценарий не изменяет Deployment, Service, topology, probe или SLO-конфигурацию,
не выполняет rolling upgrade и не останавливает cluster, container или Docker
network. Эти действия принадлежат другим E2E-задачам.

## Обязательные входы

Topology предоставляет неизменяемый inventory текущего запуска:

- уникальный непустой run ID;
- отдельный kubeconfig и точный context каждого участвующего кластера;
- namespace `marketmesh-e2e-tunnel` и точные имена Deployment
  `mm29-gateway-in`/`mm29-gateway-out` для обоих DC;
- ownership metadata, позволяющую доказать принадлежность namespace,
  Deployment и pod этому run ID;
- путь к каталогу diagnostics, ограниченному текущим запуском.

MM-32 принимает актуальный additive inventory v1 только целиком: вместе с
`target_api_version` и командами `targets_resolve`, `targets_validate` и
`targets_rebind`. Строки команд валидируются как metadata, но никогда не
исполняются runner-ом. Node-level target snapshot/rebind из MM-38/MM-41 не
используется: MM-32 удаляет только Kubernetes pod через точные kubeconfig,
context, owner chain и server-side UID precondition.

Probe предоставляет bounded операции:

- дождаться steady state и минимум одного доступного end-to-end пути;
- поставить monotonic marker начала и конца fault;
- получить bounded traffic/steady snapshot без payload, токенов и PII;
- завершить поток и вернуть итоговый ledger/report;
- доказать отсутствие lost/duplicate acknowledged mutations и незавершённых
  запросов.

SLO-контракт определяет eligible request, допустимое recovery time и условие
успеха. MM-32 не подменяет эти значения локальными константами.

## Fail-closed preflight

До любого удаления сценарий обязан:

1. Проверить deadline родительского context и положительные operation/recovery
   timeouts.
2. Открыть только переданный kubeconfig и убедиться, что точный context совпадает
   с inventory. Текущий пользовательский context не используется.
3. Прочитать namespace, owner ConfigMap `mm29-run-owner`, Deployment,
   ReplicaSet и pod по точным именам/UID без wildcard. Namespace обязан иметь
   фиксированные managed-by/task labels, ConfigMap — точный `run_id`, а
   Deployment/ReplicaSet/pod — `marketmesh.io/run-id`, `marketmesh.io/dc` и
   `marketmesh.io/zone`, соответствующие inventory; ownerReference UID образуют
   цепочку pod → ReplicaSet → Deployment.
4. Убедиться, что Deployment принадлежит текущему одноразовому стенду, имеет
   ожидаемое число ready/available replicas и не выполняет rollout.
5. Получить steady probe snapshot, а из текущего routing snapshot независимо
   доказать, что после удаления выбранного pod останется минимум один доступный
   end-to-end путь. Probe не подменяет routing capacity.

Неполное или чужое routing-сопоставление, отсутствующая ownership metadata,
чужой ресурс, неожиданный context или недостаточная ёмкость завершают
сценарий до destructive action.

## Матрица и порядок

Матрица симметрично покрывает оба DC и оба компонента. Минимум два прогона
используют противоположные порядки DC и ролей:

1. `dc-a → dc-b`, сначала pod на текущем active path, затем standby;
2. `dc-b → dc-a`, сначала standby, затем active.

Внутри каждой группы отдельно проверяются `gateway-in` и `gateway-out`. Роль
pod определяется заново непосредственно перед шагом по фактическому
health/routing snapshot: после reschedule или failover прежняя роль не
кэшируется. В smooth-WRR нет постоянного leader: active-кандидатом становится
eligible pod с максимальной суммой `ActiveRequests`, standby-кандидатом — с
минимальной. Равные значения, включая обычный для быстрых запросов нуль,
разрешаются детерминированным порядком точных pod references, поэтому active и
standby всегда различаются при двух полностью eligible replicas. Opaque
tunnel/instance IDs сопоставляются с pod только внутри bounded E2E ledger и
не попадают в logs/metrics/traces. Неоднозначный или пустой набор кандидатов
останавливает destructive action. Между fault-шагами обязательно полное
восстановление steady state и исходной capacity; параллельные удаления
запрещены.

Для этого MM-32 включает в `gateway-in` закрытый read-only endpoint
`GET /_e2e/tunnel-routing-snapshot`. Он регистрируется только при
`E2E_ROUTING_SNAPSHOT_ENABLED=true` и принимает соединения только с корректно
разобранного loopback IP; runner обращается к точному принадлежащему запуску pod
через bounded `kubectl port-forward`, минуя Service/NodePort. Версионированный
JSON `marketmesh.gateway-in.e2e.routing-snapshot/v1` содержит только имя текущего
E2E `gateway-in` pod и детерминированно отсортированные конечные route/state/DC,
32-символьный lower-hex InstanceID и active request count. TunnelID, payload и
metadata не экспортируются; ответ помечен `no-store`. Disabled endpoint,
не-loopback клиент, невалидная схема, неизвестное значение или неоднозначное
сопоставление дают fail-closed результат.

Mapping повторяет контракт MM-29, но не выдаёт authority: `gateway-in` server
instance ID — первые 16 байт SHA-256 имени pod; два client instance ID каждого
`gateway-out` pod — первые 16 байт SHA-256 от имени pod и одного raw slot byte
`0`/`1`. Оба slot ID сопоставляются с одним точным owned pod. ID другого pod,
один ID у нескольких pod или snapshot без полного покрытия отвергаются.

## Один fault-шаг

Каждый шаг имеет собственный bounded context и выполняется последовательно:

1. Снять pre-fault resource/probe snapshot и повторно проверить доступный путь.
2. Записать fault-start marker с bounded полями: DC, component и роль. Имя pod,
   UID, request ID и другие высококардинальные значения в metrics не попадают.
3. Непосредственно перед удалением повторно проверить ownership,
   eligibility точного выбранного pod и retained capacity. Мгновенная роль
   вторично не классифицируется: её перемещение между preflight и DELETE не
   меняет baseline target, пока тот самый namespace/name/UID остаётся owned,
   Ready и eligible, а после его удаления остаётся минимум один путь. Затем
   удалить ровно один pod по точным namespace/name с server-side UID precondition.
   grace period задаётся явно; принудительное удаление по умолчанию запрещено.
4. Дождаться исчезновения старого UID, появления replacement pod с новым UID,
   его Ready condition, восстановления available replicas и tunnel reconnect.
5. Дождаться steady probe state и записать fault-end marker. Промежуточный live
   snapshot не считается доказательством успеха: он по определению неполный.

После последнего fault continuous runner bounded-образом останавливается и
возвращает единственный immutable final snapshot. Только после этого MM-31 один
раз читает полный internal ledger, выполняет reconciliation и применяет
MM-27 SLO-контракт ко всем marker/request intervals. Незакрытый marker, stop
timeout, неполный ledger или неизвестный request дают fail-closed результат.

Любая ошибка прекращает последующие faults. При этом diagnostics собираются до
cleanup и результат остаётся fail, даже если восстановление/cleanup успешны.

## Diagnostics и cleanup

Для каждого шага сохраняются bounded и очищенные от чувствительных данных:

- Kubernetes events участвующего namespace;
- состояния Deployment, ReplicaSet и pod до fault и после recovery;
- логи старого pod, если они доступны, и replacement pod;
- probe markers, ledger и SLO report;
- seed, порядок шагов, версии инструментов и точные context names.

Diagnostics пишутся только внутрь каталога текущего run ID с ограничением
размера. Payload, Secret data, certificate/key material, Authorization/Cookie и
PII в artifacts запрещены.

Cleanup идемпотентен и не удаляет topology или пользовательские ресурсы. Он
останавливает только принадлежащие сценарию процессы/markers и ждёт завершения
всех запущенных операций. Удаление disposable topology выполняет её владелец
после того, как MM-32 сохранила diagnostics.

## Критерий результата

Сценарий успешен только если выполнены все элементы матрицы, каждый replacement
полностью восстановил capacity, probe/SLO-контракт успешен, acknowledged
mutations не потеряны и не продублированы, незавершённые запросы/горутины
отсутствуют, diagnostics завершены, а cleanup подтвердил отсутствие ресурсов,
которыми владеет только MM-32. Пропущенный шаг, неполный ledger, неизвестный
интервал, timeout или ошибка cleanup дают ненулевой результат.

## Запуск runner

После создания уникальной topology и deploy принадлежащих ей MM-29 workloads
runner запускается только с абсолютными non-symlink путями inventory/scenario и
новым абсолютным каталогом artifacts:

```sh
task tunnel-e2e:pod-chaos -- \
  --run-id mm32-example \
  --inventory /absolute/path/inventory.json \
  --scenario /absolute/path/e2e/tunnel/podchaos/testdata/scenario.json \
  --artifacts /absolute/path/mm32-artifacts \
  --kubectl /absolute/path/mm28-topology/mm32-example/bin/kubectl \
  --revision <commit> \
  --timeout 15m
```

Continuous probe использует два потока по 25 RPS и заранее проверенный bounded
journal: максимум 50 000 requests и 150 000 timeline events. Финальный ledger
каждого internal replica читается один раз с limit 50 000 и внутренним deadline.
Успех публикует atomic probe bundle; oversize, incomplete ledger, cleanup error
или неполная матрица не могут дать passing report.

Путь `--kubectl` обязателен, должен указывать на absolute executable без
symlink, установленный bootstrap соответствующей MM-28 topology. Ambient
`PATH` runner не использует, поэтому несовместимый локальный kubectl не может
неявно обслуживать destructive preflight, port-forward или diagnostics.
