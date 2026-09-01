# Полный отказ DC, failover и controlled failback

Этот документ фиксирует исполняемый контракт сценария MM-35. Конкретные
адаптеры стенда, front door, непрерывного probe и rolling redeploy поставляются
задачами MM-28, MM-30, MM-31 и MM-34. Сценарий MM-35 использует их публичные
границы и не воспроизводит их внутреннюю реализацию.

## Границы безопасности

Сценарий разрешено запускать только на одноразовом локальном стенде текущего
run. До остановки, удаления или восстановления ресурсов orchestration обязан
проверить одновременно:

- kubeconfig содержит ровно четыре ожидаемых context текущего run;
- имена кластеров равны ожидаемым именам `dc-a-dmz`, `dc-a-internal`,
  `dc-b-dmz` и `dc-b-internal` с уникальным префиксом текущего run;
- все контейнеры и Docker-сети принадлежат тому же run и имеют его label;
- выбранный DC содержит ровно DMZ- и internal-кластер;
- второй DC полностью ready до начала fault phase;
- context или resource без уникального префикса и label текущего run приводит к
  немедленному отказу без destructive-действий.

Запрещено использовать текущий пользовательский Kubernetes context, широкие
glob-выражения, выбор ресурсов только по частичному совпадению имени и любые
dev, preprod или production окружения. Все ожидания, повторные попытки,
очереди, drain и cleanup имеют явные пределы времени и количества операций.

## Адаптерные границы

Сценарий зависит только от следующих узких контрактов:

- **Topology** возвращает неизменяемый снимок точных cluster, container,
  network и kube context identifiers текущего run; выполняет preflight,
  остановку и восстановление только переданного набора идентификаторов;
- **Drainer** выполняет управляемый drain выбранного DC до остановки кластеров;
- **Front door** использует публичный MM-30 метод `Check(context.Context)` для
  немедленной проверки двух фиксированных backend; исключение unhealthy DC и
  постепенный failback выполняются самой реализацией MM-30;
- **Probe** запускает bounded read и mutating потоки, ставит fault markers и
  завершает ledger reconciliation без повторов mutating-запроса после
  неопределённого результата;
- **Readiness** подтверждает PKI, workloads, tunnel и front door eligibility
  восстановленного DC до разрешения failback;
- **Timeline** сохраняет только тип события, monotonic offset, DC, zone,
  resource kind, bounded status и безопасную причину без payload, secrets, PII,
  request ID и иных высококардинальных значений.

Каждая операция получает `context.Context`, возвращает ошибку с контекстом
операции и не создаёт fire-and-forget goroutine. Владельцем всех фоновых
процессов является runner: он обязан отменить их и дождаться завершения.

Consumer-side границы находятся в пакете
`e2e/tunnel/internal/dcfailover`: `Topology`, `Drainer`, `FrontDoor`,
`Readiness` и `Probe`. Интерфейс `FrontDoor` структурно совпадает с публичным
контрактом MM-30 и проверяется compile-time и network contract-тестом; локальная
реализация routing или failback policy отсутствует. `Topology.Preflight`
возвращает полный `Snapshot`, после чего runner
повторно fail-closed проверяет `run-id`, признак disposable local E2E, четыре
уникальные пары DC/zone и точные имена kube context, cluster, container и
network без glob. До успешной проверки снимка cleanup не запускается, потому
что принадлежность ресурсов ещё не доказана. После проверки адаптеры получают
защитные копии точных targets.

## Симметричный сценарий

Один запуск последовательно выполняет одинаковый набор фаз сначала для DC-A,
затем для DC-B. Обе стороны и оба вида outage обязательны.

1. **Baseline.** Проверить полную readiness обоих DC, запустить read и mutating
   probe, дождаться устойчивого окна и сохранить исходный health snapshot.
2. **Managed drain.** Перевести выбранный DC в draining и дождаться
   подтверждения, что новые запросы больше не назначаются его endpoint и tunnel.
3. **Outage.** После повторной проверки точных identifiers остановить только
   DMZ- и internal-кластеры выбранного DC. Отдельная итерация без drain
   моделирует внезапный отказ.
4. **Failover.** Дождаться исключения DC из health pool в пределах аварийного
   SLO. Все новые eligible-запросы должны обслуживаться вторым DC; stale
   endpoint и tunnel не должны принимать новые запросы.
5. **Diagnostics.** До любого cleanup сохранить timeline событий cluster,
   container и network, безопасные Kubernetes events, health snapshots и
   результаты probe.
6. **Restore.** Восстановить точные кластеры выбранного DC, затем PKI,
   workloads и tunnel. DC остаётся неeligible до полной readiness каждого
   слоя.
7. **Controlled failback.** Вернуть DC в health pool с ограниченной скоростью,
   проверить отсутствие всплеска одновременных reconnect и устойчивое
   распределение новых запросов.
8. **Reconciliation.** Остановить probe с bounded drain и сверить client и
   internal ledgers: acknowledged mutations не потеряны и не продублированы;
   неполный ledger или неизвестный интервал означает failure.
9. **Cleanup.** После сохранения diagnostics идемпотентно удалить только
   ресурсы текущего run и подтвердить отсутствие принадлежащих ему cluster,
   container и network.

После первой стороны стенд обязан снова достичь полного baseline до начала
второй. Ошибка восстановления, диагностики или cleanup не должна скрывать
первичную ошибку: итоговый отчёт сохраняет обе причины.

## Обязательные проверки результата

Сценарий успешен только при одновременном выполнении условий:

- второй DC обслуживал eligible read и mutating requests в пределах
  аварийного SLO;
- draining и stale endpoints не получили новых назначений;
- все acknowledged mutations присутствуют ровно один раз;
- восстановленный DC стал eligible только после PKI, workloads и tunnel
  readiness;
- failback не превысил заданные limits reconnect и распределения трафика;
- обе стороны прошли managed и sudden outage;
- diagnostics собраны до cleanup, а cleanup удалил только ресурсы текущего run.

Unit-тесты orchestration должны использовать fakes и проверять симметрию,
порядок фаз, отмену по context, пределы retry, fail-closed preflight и
сохранение первичной ошибки. Интеграционные тесты проверяют адаптерные границы
без разрушения кластеров. Destructive E2E запускается отдельно и только после
успешного preflight одноразового стенда.
