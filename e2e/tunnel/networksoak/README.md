# Network soak bridge

Пакет содержит только test-only adapters MM-36. Production-код не импортирует
`platform/testkit`, а ownership fault lifecycle остаётся в исходных пакетах:

- `networkchaos.Observer` переводится в MM-31 markers `before`, `started` и
  `recovered`; для combined network faults steady sample снимается только после
  последнего marker каждой фазы;
- MM-33 `servicechaos.Observer` переводит фиксированный lifecycle Service и
  Pod faults в уникальные markers по DC и occurrence, не повторяя Kubernetes
  mutations или restore;
- continuous probe, resource sampler и chaos runner объединяются одной bounded
  session. Chaos не стартует до принятого resource baseline; преждевременное
  завершение probe или sampler отменяет session и сохраняет частичные ledgers.

Resource и probe failures не могут быть преобразованы в flaky pass. Итоговый
SLO вычисляет merged MM-27 `spec.Evaluate`, а resource ledger проверяет
`networkchaos.EvaluateResources`.

До публикации task-ветки standalone-модуль `e2e/tunnel` не может получить
локальную версию нового `platform/testkit/networkchaos` без запрещённого
`replace`. Поэтому bridge сейчас проверяется из корневого `go.work`; после push
нужно закрепить точный pseudo-version `platform`, повторить standalone race и
убрать это ограничение. Destructive suite остаётся запрещён до merge MM-42 и
готовности MM-32/MM-34/MM-35.

Локальная недеструктивная проверка:

```bash
go test -race -count=20 ./e2e/tunnel/networksoak
```
