# Rolling redeploy обратного tunnel без простоя

Этот документ задаёт исполняемый контракт сценариев MM-34. Он не создаёт
topology, workloads или генератор нагрузки: эти обязанности принадлежат
MM-28/MM-29 и MM-31. Реализация MM-34 должна использовать их опубликованные
интерфейсы после слияния в `dev`, а не копировать соседние E2E-компоненты.

Независимый исполняемый контур находится в `e2e/tunnel/internal/rolling`:

- `NewPlan` строит полные варианты A/B и для каждого target ставит image-step
  перед config-step;
- `Runner` ограничивает каждый этап отдельным deadline, синхронизируется с
  continuous probe и при любом пост-мутационном сбое выполняет цепочку
  diagnostics → rollback → проверка capacity → steady-state;
- Kubernetes adapter декодирует только versioned inventory v1 MM-28, извлекает
  из него четыре явных kubeconfig/context и принимает только allowlisted
  Deployment MM-29; непосредственно перед patch и rollback он повторно
  проверяет UID кластера, runtime ownership topology и namespace/run ownership;
- image-step принимает только digest того же repository, config-step изменяет
  только безопасную Pod template annotation;
- отрицательный сценарий использует встроенную fail-closed конфигурацию каждого
  workload, а не произвольный внешний image.

`cmd/rollingctl` и тонкий adapter используют опубликованный API MM-31 без
копирования его внутренних очередей. CLI запускает read и mutating traffic до
первого precondition, останавливает его только после последнего steady-state и
атомарно публикует полный `run.json`, `report.json`, `report.junit.xml` и
`report.txt`. Временный no-op probe не допускается.

Fake internal ledger хранится в памяти конкретной Pod, поэтому во время её
замены MM-34 ведёт динамический read-only архив. До старта traffic архив требует
ровно две ready Pod в каждом internal-кластере. Новая Pod должна быть обнаружена
и опрошена до перехода в ready; для завершающейся Pod обязателен последний
успешный snapshot после закрытия readiness и до её исчезновения. Пропущенный,
частичный, неоднозначный или изменившийся snapshot делает resolver нездоровым и
весь SLO-run неуспешным.

## Граница безопасности

Сценарий разрешено выполнять только над одноразовыми ресурсами конкретного
E2E-запуска. Каждый вызов Kubernetes API обязан явно передавать отдельный
`kubeconfig`, точный context и namespace. Использовать текущий context,
контекст пользователя, wildcard по cluster/container/network или окружения
`dev`, `preprod` и `production` запрещено.

До первого изменения сценарий проверяет следующие значения:

- уникальный `run_id`, входящий в имена всех disposable-ресурсов;
- inventory `marketmesh.dev/e2e-topology/v1` с ownership `MM-28`, четырьмя
  ожидаемыми context: DMZ и internal для каждого DC; command strings из
  inventory считаются только metadata и никогда не исполняются MM-34;
- namespace и labels, однозначно связывающие workload с тем же `run_id`;
- точные имена Deployment и Container, полученные из контракта MM-29;
- отсутствие любого target за пределами allowlist этого запуска.

Для прямого чтения ledger дополнительно проверяется неизменная цепочка
Pod UID → ReplicaSet UID → Deployment UID. `kubectl port-forward` запускается
без shell и ambient `KUBECONFIG`, только на случайном порту `127.0.0.1`, а после
его ready-handshake та же ownership-цепочка проверяется повторно. TLS Secret
читается только в память, client/server SPIFFE URI и server DNS сверяются
точно, transport retry отключён. Сертификаты, ключи, адреса Pod и opaque
идентификаторы не попадают в argv, логи и итоговые отчёты.

Несовпадение хотя бы одного значения завершает сценарий до мутации. Снимки
Deployment, ReplicaSet, Pod, EndpointSlice, events и безопасные логи собираются
до cleanup. Payload, metadata, сертификаты, токены, PII и opaque request или
idempotency identifiers в diagnostics не сохраняются.

## Preconditions ёмкости

Плановый rollout начинается только при полностью готовой исходной системе:

1. continuous probe MM-31 работает и подтвердил steady-state для read и
   mutating потоков;
2. оба DC готовы принимать новые запросы через аутентифицированный tunnel v1;
3. target Deployment имеет не менее двух ready replicas;
4. Deployment использует `RollingUpdate` с абсолютными значениями
   `maxUnavailable: 0` и `maxSurge: 1`;
5. `observedGeneration` совпадает с `generation`, число unavailable replicas
   равно нулю, предыдущий rollout завершён;
6. Pod имеет readiness gate, lifecycle `preStop` и положительный
   `terminationGracePeriodSeconds`;
7. budget termination строго больше суммы budget `preStop`, application drain
   и bounded shutdown с запасом на завершение процесса;
8. предыдущая и новая revisions используют tunnel protocol v1 и различаются
   только проверяемым image digest или config revision.

Если доступной capacity недостаточно, интервал нельзя исключать из метрики и
нельзя маскировать повтором: сценарий завершается до rollout как invalid setup.

## Lifecycle workloads

Readiness означает способность принять **новый** eligible request, а не только
живой процесс.

- `gateway-in` готов только при готовности внешнего listener и наличии хотя бы
  одного аутентифицированного, не-draining tunnel для проверяемого route;
- `gateway-out` готов только после успешного mTLS и tunnel v1 handshake, а при
  начале termination немедленно становится not-ready; один из двух путей
  периодически перераспределяется, пока второй сохраняет доступность;
- fake internal service готов только после готовности read/mutating handlers и
  request ledger, а при termination перестаёт принимать новые RPC до drain.

`preStop` сначала закрывает readiness для нового трафика, затем инициирует
bounded application drain. Для `gateway-out` штатный shutdown обязан отправить
`Drain`; для `gateway-in` registry обязан перестать выбирать завершающийся
tunnel до остановки gRPC transport. После drain процесс завершает оставшиеся
операции в пределах общего deadline. Обычный `sleep` без проверки состояния не
считается доказательством корректного drain.

## Матрица и порядок

Один шаг изменяет ровно один Deployment в одном DC. Следующий шаг начинается
только после восстановления полной исходной capacity и steady-state probe.

| Вариант | Порядок DC | Порядок зон и компонентов |
| --- | --- | --- |
| A | `dc-a`, затем `dc-b` | DMZ `gateway-in`, затем internal `gateway-out`, затем internal service |
| B | `dc-b`, затем `dc-a` | internal service, затем internal `gateway-out`, затем DMZ `gateway-in` |

Оба варианта выполняются целиком. Так проверяются оба порядка DC и оба
направления перехода между DMZ/internal без одновременного уменьшения capacity
нескольких компонентов.

Исполняемые MM-27 scenario v1 находятся рядом с rolling package:
`testdata/scenarios/rolling-update-mm34-a.json` и
`testdata/scenarios/rolling-update-mm34-b.json`. Каждый файл содержит ровно 12
уникальных fault expectations: image/config для трёх workload в обоих DC.
`ValidateScenarioForPlan` требует точного совпадения порядка и target каждого
fault с выбранным планом; общий пример MM-27 с тремя fault ID для полного
прогона MM-34 не используется.

Отрицательная матрица хранится в
`testdata/scenarios/rolling-rollback-mm34.json`: шесть built-in readiness
faults покрывают каждый workload в каждом DC и обязаны завершиться автоматическим
rollback и marker `recovered/success` без `failure` для ожидаемого отказа
readiness. Неожиданный сбой mutation, rollback или steady-state остаётся
fail-closed.

Для каждого target выполняются два перехода соседних revisions:

1. предыдущая → новая с изменением зафиксированного image digest;
2. новая → следующая config revision без изменения tunnel v1 wire contract.

Тег `latest` и повторное использование изменяемого image запрещены. Новая Pod
сначала проходит startup/readiness и tunnel handshake; только после этого
Kubernetes может завершить старую Pod. На каждом наблюдаемом состоянии должны
сохраняться инварианты `unavailable == 0`, `ready >= desired` и
`total <= desired + 1`.

## Rollback при провале readiness

Перед изменением сохраняются предыдущие Deployment revision, generation,
image digests и безопасный config revision. Для отдельного отрицательного шага
применяется заведомо неготовая, но безопасная revision только к одному target.
Она должна запускаться и fail-closed не проходить readiness или tunnel
handshake; использование чужого registry или произвольного image запрещено.

Ожидание readiness ограничено `progressDeadlineSeconds` и более коротким
scenario deadline. При истечении deadline сценарий:

1. ставит fault marker в continuous probe;
2. сохраняет diagnostics неготовой revision;
3. откатывает **тот же** Deployment на точно сохранённую предыдущую revision;
4. bounded ожидает прежние ready replicas, endpoints и tunnel handshake;
5. требует восстановления полной capacity и steady-state probe без ручного
   restart соседних workloads.

Успешный rollback не превращает отрицательный rollout в успешный: отчёт должен
показать ожидаемый отказ readiness и отдельно успешное восстановление. Ошибка
rollback или неполная capacity немедленно завершает сценарий.

## Контракт с continuous probe

MM-34 не управляет внутренними очередями probe и не повторяет mutating calls.
Он только передаёт bounded lifecycle и fault markers через интерфейс MM-31:

- точный `run_id` и одноразовый bounded lifecycle probe;
- фаза `before`, `steady`, `rollout`, `rollback` или `recovered`;
- конечные значения `dc`, `zone`, `component` и `result`;
- monotonic offset относительно начала запуска;
- старый и новый безопасные revision identifiers без image registry credentials.

Probe стартует до первой precondition и останавливается после финального
steady-state. Во время всех положительных rollout-шагов 100% eligible read и
mutating requests должны завершиться успешно. Mutating request после
неопределённого результата автоматически не повторяется; client/internal
ledgers MM-31 обязаны доказать отсутствие lost или duplicate acknowledged
mutations.

Нулевое число eligible requests, пропущенный интервал, неизвестный результат,
потерянный marker, незавершённый cleanup или превышение любого deadline означают
fail. Метрики и labels используют только конечные категории из этого документа.

## Запуск CLI и артефакты

Каждый вариант A/B и rollback-матрица запускаются с отдельной свежей baseline
и новым абсолютным каталогом артефактов. Front door MM-30 должен уже быть готов,
а inventory MM-28 сохранён в обычный файл. Для положительного варианта нужны
три соседних image reference вида `repository@sha256:<64 hex>`; repository
обязан точно совпадать с текущим image соответствующего Deployment.

Для disposable kind допускается локальный режим без registry: три образа
загружаются через `kind load docker-image` под уникальными тегами
`marketmesh/<component>:mm34-<12 hex commit>`. Другие mutable tags, включая
`latest`, отклоняются, а workload используют `imagePullPolicy: Never`.

```sh
task tunnel-e2e:rolling -- \
  --run-id mm34-a-1 \
  --mode a \
  --inventory /absolute/path/mm34-a-1-inventory.json \
  --frontdoor http://127.0.0.1:18080 \
  --artifacts /absolute/path/mm34-a-1-artifacts \
  --gateway-in-image marketmesh/gateway-in@sha256:<digest> \
  --gateway-out-image marketmesh/gateway-out@sha256:<digest> \
  --fake-internal-image marketmesh/fake-internal@sha256:<digest>
```

Вариант B отличается только `--mode b` и новым baseline/run ID. Отрицательная
матрица использует встроенные безопасные readiness faults и не принимает новый
image:

```sh
task tunnel-e2e:rolling -- \
  --run-id mm34-rollback-1 \
  --mode rollback \
  --inventory /absolute/path/mm34-rollback-1-inventory.json \
  --frontdoor http://127.0.0.1:18080 \
  --artifacts /absolute/path/mm34-rollback-1-artifacts
```

Каталог артефактов должен отсутствовать до запуска: публикация использует
no-replace rename и не перезаписывает доказательства предыдущего прогона.
Даже при валидном провале SLO CLI пытается сохранить полный fail-report;
неполный архив ledger, незавершённый traffic runner или cleanup запрещают
создание искусственного capacity interval и потому не могут дать pass.

## Bounded execution и cleanup

Отдельные deadlines задаются для precondition, каждого rollout, drain,
rollback, восстановления probe, сбора diagnostics и cleanup. Общий deadline
жёстко ограничивает сумму этапов. Retry допускается только для идемпотентного
чтения состояния Kubernetes с ограниченным числом попыток и backoff.

Cleanup идемпотентен и удаляет только namespace/ресурсы с проверенными
`run_id`, context и owner labels. Перед каждым delete повторно валидируются
точные target-значения. Cleanup запускается после diagnostics и не скрывает
основную ошибку; его собственная ошибка также делает сценарий неуспешным.

## Обязательные проверки после интеграции prerequisites

- unit-тесты матрицы, preconditions, deadline и rollback state machine;
- отрицательные тесты context/namespace/run ownership и недостаточной capacity;
- integration-тесты Kubernetes adapter с отдельным disposable cluster;
- положительный rolling E2E для обоих вариантов не менее двух последовательных
  запусков;
- отрицательный readiness rollout с автоматическим rollback для каждого типа
  workload;
- проверка JSON/JUnit отчётов MM-31, отсутствия duplicate mutations и
  сохранения 100% eligible success;
- `gofmt`, lint, `go vet`, `go test -race`, build, `GOWORK=off`,
  `govulncheck`, `go mod verify`, генерация и архитектурные/root verify.
