# SLO и модель отказов E2E для reverse tunnel

Документ задаёт проверяемый контракт доступности reverse tunnel между двумя DC.
Его исполняет Go-пакет `github.com/v0hmly/marketmesh/e2e/tunnel/spec`.
Пакет не создаёт Kubernetes-кластеры, не генерирует трафик и не выполняет fault
injection: эти обязанности остаются у topology, workload, probe и fault runners.

## Версии форматов

Контракт использует три независимые версии:

| Документ | `schema_version` |
| --- | --- |
| конфигурация сценария | `marketmesh.tunnel.slo.scenario/v1` |
| полный журнал запуска | `marketmesh.tunnel.slo.run/v1` |
| итоговый JSON-отчёт | `marketmesh.tunnel.slo.report/v1` |

JSON разбирается строго: неизвестное или повторяющееся поле, несколько JSON-документов
в одном потоке и превышение размера отклоняются. Несовместимое изменение поля,
допустимого значения или формулы требует новой версии. Добавление поля в `v1` также
считается несовместимым, потому что старый reader обязан отклонить его.

Machine-readable сценарии находятся в
`e2e/tunnel/spec/testdata/scenarios`. Они описывают ожидание, но не содержат
команды удаления, остановки, partition или изменения Kubernetes-ресурсов.

## Границы времени и warm-up

Пусть:

- `T0 = run.started_at`;
- `Tw = T0 + scenario.warm_up`;
- `T1 = run.ended_at`;
- измеряемое окно `W = [Tw, T1)`.

`T1` обязан быть позже `Tw`. Все запланированные запросы, включая warm-up,
учитываются при сверке `planned` и `recorded`. Запросы с `scheduled_at` до `Tw`
не входят в SLO, но не могут быть удалены из журнала. Warm-up нужен только для
установления tunnel, прогрева connection pools и стабилизации readiness; его нельзя
использовать для сокрытия fault interval.

## Eligible request

Журнал содержит только валидные запросы probe к заранее разрешённым route ID.
Запрос `r` является eligible тогда и только тогда, когда:

```text
eligible(r) =
  r.class ∈ {read_idempotent, mutating}
  ∧ Tw ≤ r.scheduled_at < T1
  ∧ r.scheduled_at не принадлежит подтверждённому explicit exclusion
```

Невалидный class, request ID, timestamp или attempt не превращает запрос в
ineligible: это отдельное нарушение `integrity`, и весь отчёт имеет статус `fail`.
Для каждого class `planned.count` обязан точно совпадать с количеством записей.
Поэтому пропущенный результат нельзя скрыть уменьшением знаменателя.

## Успех и повторы

Логический запрос успешен только при одновременном выполнении условий:

```text
successful(r) =
  ¬r.missing
  ∧ len(r.attempts) = 1
  ∧ r.attempts[0].outcome = success
  ∧ timestamps attempt валидны и конечны
```

Любой retry делает логический SLO sample неуспешным, даже если последний attempt
завершился успешно. Так transport retry остаётся видимым и не маскирует исходную
ошибку. `read_idempotent` означает допустимость безопасной реализации повтора вне
этого измерения, а не право переписать историю первого attempt. Для `mutating`
повтор дополнительно является нарушением `mutating_retry`.

`failure` — известный неуспешный terminal outcome. `unknown` и пустой список
attempts без `missing=true` означают неизвестный результат. Любой eligible
`missing` или `unknown` всегда завершает проверку с `fail`, даже если численный
error budget ещё не исчерпан.

## Availability и error budget

Для каждого class вычисляются целые значения:

```text
E = количество eligible requests
S = количество successful eligible requests
F = E - S
availability_ppm = floor(S × 1_000_000 / E), если E > 0, иначе 0
allowed_failures = floor(E × max_error_rate_ppm / 1_000_000)
consumed_failures = F
remaining_failures = max(allowed_failures - consumed_failures, 0)
```

Class проходит проверку, только если одновременно:

- `E >= min_eligible`;
- `F <= allowed_failures`;
- нет eligible `missing` или `unknown`;
- каждое downtime window не превышает `max_downtime`.

Для планового rolling update при сохранённой capacity конфигурация обязана иметь
`max_error_rate_ppm = 0` и `max_downtime = 0s` для обоих classes. Это исполняемое
требование 100% успешных eligible requests, а не округлённый процент.

## Downtime window

Для каждого class запросы упорядочиваются по `scheduled_at`. Downtime window —
максимальная последовательность неуспешных eligible requests:

- начало — `scheduled_at` первого неуспешного запроса;
- конец — `finished_at` первого следующего успешного запроса;
- если успешного запроса до конца запуска нет, конец равен `run.ended_at`.

Отдельные окна не объединяются через подтверждённый успешный запрос. Report
сохраняет все окна, а не только максимальное.

## Recovery time

Emergency-сценарий задаёт для каждого fault:

- `anchor`: `fault_started` или `fault_ended`;
- `max_duration`;
- `success_streak`;
- classes, для которых recovery измеряется независимо.

Recovery считается подтверждённым при `success_streak` последовательных successful
eligible requests после anchor. `recovered_at` — `finished_at` последнего запроса
этой серии; любой неуспешный запрос сбрасывает серию. Формула:

```text
recovery_time = recovered_at - anchor_at
```

Отсутствие достаточной серии до `run.ended_at` даёт `recovery_not_observed`.
Превышение границы даёт `recovery_time_exceeded`. Консервативно используется конец
серии, поэтому частичное или кратковременное восстановление не объявляется recovery.

## Mutating requests и внутренний ledger

Каждый `mutating` request обязан содержать:

- уникальный для логического запроса opaque `idempotency_key`;
- `ledger_known=true` после сверки client ledger и internal service ledger;
- `applied_count <= 1`;
- для успешного запроса ровно `applied_count = 1`;
- ровно один attempt.

Повтор ключа, неизвестный ledger и `applied_count > 1` являются самостоятельными
нарушениями независимо от availability. Ключи и request IDs не переносятся в
итоговый JSON/JUnit report. Они не должны содержать email, username, token, payload
или иные PII/secret; допустимы только непрозрачные случайные идентификаторы.

## Capacity ledger и единственное исключение

`capacity` обязан без gap и overlap покрывать всё окно `[Tw, T1)`. Для двух-DC
модели допустимы значения `physically_available_dc` от 0 до 2. Неизвестный участок,
неверная граница или значение больше 2 даёт `unknown_capacity_interval` либо
`invalid_capacity_interval` и запрещает применять все exclusions.

Единственная допустимая причина исключения:

```text
all_dc_physically_unavailable
```

Explicit exclusion применяется только если каждый его момент покрыт capacity
interval с `physically_available_dc = 0`. Нулевой capacity interval без explicit
exclusion остаётся в метрике. Недоступность одного DC при живом втором DC никогда
не исключается.

## Матрица отказов и начальные пороги

| Boundary | Machine target/mode | Availability | Recovery/downtime |
| --- | --- | --- | --- |
| gateway-in, gateway-out и internal service rolling update | соответствующий target / `rolling_update` | 100%, budget 0 | downtime `0s` |
| gateway-in pod outage | `gateway_in` / `pod_outage` | budget 100000 ppm | не более `10s` |
| gateway-out pod outage | `gateway_out` / `pod_outage` | budget 100000 ppm | не более `10s` |
| internal service pod outage | `internal_service` / `pod_outage` | budget 100000 ppm | не более `10s` |
| Kubernetes Service/endpoints | `kubernetes_service` / `service_endpoints_outage` | budget 100000 ppm | не более `15s` |
| network partition | `network` / `network_partition` | budget 100000 ppm | не более `20s` |
| полный отказ DC | `dc` / `dc_outage` | budget 100000 ppm | не более `30s` |

Emergency fixtures требуют минимум 100 eligible requests каждого class и пять
последовательных successes для recovery. Error budget 100000 ppm — верхняя граница
10% в конечном тестовом окне; ограничение recovery/downtime действует одновременно
и не позволяет распределить ошибки по всему запуску. Изменение этих порогов требует
review конфигурации, а не флага fault runner.

Fault runner обязан симметрично повторять применимые сценарии для DC-A и DC-B.
Компонентные задачи собирают Kubernetes events, logs и diagnostics до cleanup, но
не меняют формулы этого документа.

## Формат отчёта и интеграция probe

JSON report содержит только агрегаты по конечным classes/targets, downtime,
recovery и checks. `status=pass` возможен только когда каждый check имеет
`passed=true`. JUnit содержит один `<testcase>` на check; нарушения кодируются как
`<failure type="slo_violation">`. Таким образом CI не может получить зелёный JUnit
при красном JSON report.

Probe runner использует интерфейс в следующем порядке:

```go
scenario, err := spec.DecodeScenario(scenarioReader)
run, err := spec.DecodeRun(runReader)
report, err := spec.Evaluate(scenario, run)
err = spec.WriteJSONReport(jsonWriter, report)
err = spec.WriteJUnitReport(junitWriter, report)
```

Любая ошибка decode/evaluate/write или `report.Status == spec.ReportStatusFail`
обязана дать ненулевой exit code runner. Ожидания, очереди, concurrency и shutdown
самого runner остаются bounded и реализуются в задаче continuous probe.
