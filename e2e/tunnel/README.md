# Tunnel E2E workloads

Каталог содержит минимальный workload stack задачи MM-29. Он разворачивается
поверх четырёх одноразовых OrbStack VM с k3s задачи MM-44 и не создаёт
topology, global front door, traffic probe или fault scenarios.

## Публичный контракт ресурсов

Во всех четырёх кластерах используется namespace
`marketmesh-e2e-tunnel`. Имена workload стабильны:

| Зона | Deployment | Service | Порты |
| --- | --- | --- | --- |
| DMZ | `mm29-gateway-in` | `mm29-gateway-in` | NodePort `30080` для Connect и `30443` для reverse tunnel |
| internal | `mm29-gateway-out` | нет | только pod-local health `8080` |
| internal | `mm29-fake-internal` | `mm29-fake-internal` | cluster-local mTLS gRPC `9443` |

Каждый Deployment и его Pod template содержит метки
`marketmesh.io/run-id`, `marketmesh.io/dc`, `marketmesh.io/zone`,
`marketmesh.io/task=MM-29` и
`app.kubernetes.io/managed-by=marketmesh-e2e-tunnel`. Поэтому ReplicaSet и Pod
наследуют тот же `run-id`. Компоненты имеют две реплики, PDB с
`minAvailable: 1`, rolling strategy `maxUnavailable: 0`, `maxSurge: 1`,
`progressDeadlineSeconds: 120` и ограниченные CPU/memory.

`gateway-in` становится ready только после появления обоих статических route
ID через согласованный tunnel. Каждый Pod `gateway-out` держит ровно две
bounded session и до initial readiness требует два разных opaque instance ID
`gateway-in`, полученных после mTLS и handshake tunnel v1. Если NodePort сначала
направил обе session в одну реплику, duplicate session выполняет drain и
reconnect до покрытия обеих реплик. После первоначального покрытия один живой
путь сохраняет readiness, пока второй восстанавливается в фоне. `fake-internal`
становится ready после создания handler и bounded in-memory ledger. Перед
SIGTERM hook `preStop` сначала закрывает readiness, ждёт пять секунд, а затем
общий runtime выполняет drain с отдельным 20-секундным budget и запасом в
пределах `terminationGracePeriodSeconds: 30`.

`gateway-in` получает фиксированный `DATA_CENTER` (`dc-a` или `dc-b`) из
manifest соответствующего DMZ. Это значение локально связывается с точным
аутентифицированным URI SAN `gateway-out`: tunnel peer не объявляет свой DC и
не может влиять на route/metric labels.

## Локальный front door двух DC

MM-30 добавляет один локальный HTTP entry point для внешнего E2E traffic. Ему
явно передаются два literal private/loopback IP с NodePort `30080`; DNS,
ambient kubeconfig и service discovery не используются. Front door проверяет
`/readyz` обоих `gateway-in`, сразу исключает неготовый DC после очередной
bounded проверки и плавно возвращает восстановленный DC с весом от 10% до
100%. При двух готовых DC smooth weighted round-robin распределяет traffic
поровну. Выбранный backend не меняется до завершения request: после
неопределённой транспортной ошибки запрос, включая `Mutate`, в другой DC не
повторяется.

```sh
task tunnel-e2e:frontdoor -- \
  --listen 127.0.0.1:18080 \
  --dc-a-target http://<dc-a-dmz-vm-ip>:30080 \
  --dc-b-target http://<dc-b-dmz-vm-ip>:30080 \
  --health-interval 1s \
  --health-timeout 250ms \
  --failback-warmup 30s
```

Listen address обязан быть literal loopback IP с непривилегированным port, а
backend targets — различными literal private/loopback HTTP endpoints. Наружу
доступны только `GET /livez`, `GET /readyz` и два фиксированных Connect
procedure `Read`/`Mutate`; произвольные URL, host и методы отклоняются. Метрики
используют только конечные labels `data_center`, `route` и `status`, а логи
health transition не содержат target, opaque instance ID или request data.

## Границы и PKI

На каждый запуск и DC в памяти создаются два независимых временных CA:

- tunnel CA подписывает server certificate `gateway-in` и отдельный client
  certificate `gateway-out`;
- internal CA подписывает server certificate `fake-internal` и второй client
  certificate `gateway-out`.

Закрытые ключи передаются в Kubernetes Secret через stdin `kubectl`, не
записываются в репозиторий или diagnostics и удаляются автоматически вместе с
точными ресурсами запуска. Сертификаты действуют четыре часа, используют
TLS 1.3, отдельные server/client EKU, DNS SAN и единственный URI SAN вида
`spiffe://marketmesh.test/e2e/<run-id>/<dc>/<workload>`. Клиенты дополнительно
проверяют точный DNS и URI, серверы — CA, client EKU и ожидаемый URI identity.

NetworkPolicy запрещает произвольный ingress в internal. `fake-internal:9443`
доступен только Pod с метками текущего `gateway-out`; `gateway-out` может
исходяще обратиться только к DNS, `fake-internal:9443` и фиксированному tunnel
port `30443`. Топологический запрет DMZ → internal обеспечивает MM-44.

Внешние Connect methods статически связаны с двумя существующими route ID:

- `FakeInternalService.Read` → `ROUTE_ID_USER_GET_ME`;
- `FakeInternalService.Mutate` → `ROUTE_ID_USER_UPDATE_ME`.

URL, host, port и полное имя внутреннего gRPC method не принимаются от
вызывающей стороны. Mutate требует единственный printable ASCII header
`Idempotency-Key` длиной не более protocol limit; gateway-out пересылает только
его бинарное значение под фиксированным internal metadata name. Raw key и
payload не сохраняются и не логируются; ledger хранит request ID, operation,
SHA-256 idempotency key и bounded attempt count.

Opaque instance ID `gateway-in` равен первым 16 байтам SHA-256 Pod name. Два
instance ID `gateway-out` на Pod равны первым 16 байтам SHA-256 от Pod name с
добавленным байтом slot `0` или `1`. Значения используются только для проверки
принадлежности и разнообразия путей, не дают полномочий и не становятся
metric labels.

## Сборка и загрузка образов

```sh
task tunnel-e2e:images
task tunnel-e2e:load
```

Первая команда собирает три локальных образа и ничего не публикует. Builder и
runtime base images закреплены multi-arch digest, сборка использует
`-trimpath`, `buildvcs=false`, commit timestamp как `SOURCE_DATE_EPOCH`,
отключённую локальную provenance attestation, non-root distroless runtime и
source revision в OCI labels. Полученные имена печатаются в stdout.

Вторая команда передаёт каждый образ в containerd всех четырёх VM topology
MM-44 через `e2e-topology load-images` (`docker save` → tar в VM →
`k3s ctr -n k8s.io images import` → проверка точного тега в каждой VM).
Манифесты используют `imagePullPolicy: Never`, поэтому пропущенная загрузка
обнаруживается сразу при deploy.

## Deploy и cleanup

Lifecycle не читает ambient `KUBECONFIG`: все четыре kubeconfig и context
передаются явно. Internal target каждого DC обязан использовать фиксированный
NodePort `30443`.

```sh
task tunnel-e2e:deploy -- \
  --run-id run-29 \
  --version <commit> \
  --gateway-in-image <image> \
  --gateway-out-image <image> \
  --fake-internal-image <image> \
  --dc-a-dmz-kubeconfig <path> \
  --dc-a-dmz-context mm44-dc-a-dmz \
  --dc-a-internal-kubeconfig <path> \
  --dc-a-internal-context mm44-dc-a-internal \
  --dc-a-gateway-in-target passthrough:///<dc-a-dmz-vm-ip>:30443 \
  --dc-b-dmz-kubeconfig <path> \
  --dc-b-dmz-context mm44-dc-b-dmz \
  --dc-b-internal-kubeconfig <path> \
  --dc-b-internal-context mm44-dc-b-internal \
  --dc-b-gateway-in-target passthrough:///<dc-b-dmz-vm-ip>:30443 \
  --timeout 3m
```

Повторный deploy с тем же `run-id` идемпотентен. Если owner ConfigMap содержит
другой `run-id` или любое точное имя ресурса уже занято чужим объектом, команда
завершается до первой мутации. При ошибке
сначала сохраняются bounded metadata, events и последние безопасные логи, затем
автоматически удаляются только известные Deployment, Service, PDB,
NetworkPolicy, Secret и owner ConfigMap с совпадающим `run-id`. Namespace не
удаляется, потому что его жизненным циклом владеет одноразовая topology.

Отдельные команды диагностики и штатного удаления используют тот же набор
явных topology flags:

```sh
task tunnel-e2e:inspect -- --run-id run-29 <topology-flags>
task tunnel-e2e:undeploy -- --run-id run-29 <topology-flags>
```

Полный поток: `task e2e:topology:up` → `ready` → `tunnel-e2e:images` →
`tunnel-e2e:load` → deploy → frontdoor → внешний проход read+mutate через оба
DC → undeploy → `e2e:topology:down`. Rolling redeploy описан в ветке MM-34.
Полный deploy/undeploy и внешний проход через оба DC выполняются только после
объединения MM-44. Локальные unit/integration проверки PKI, mTLS, route
allowlist, idempotency, manifests и точного cleanup от topology не зависят.
