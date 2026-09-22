# Files: полный отказ логического DC в OrbStack

MM-43, [ADR-0015](../adr/0015-private-file-data-plane.md),
[решение для текущего dev](../security/files-dev-acceptance.md).
Harness: [`infra/files-dc-e2e`](../../infra/files-dc-e2e/run.py).

## Топология и границы результата

Используются четыре одноразовые OrbStack VM с k3s из
[ADR-0014](../adr/0014-orbstack-vm-k3s-e2e-topology.md): internal и DMZ в DC A/B.
Всё состояние сервисов находится на дисках соответствующих VM. Host запускает
клиент, управляет отказами и сохраняет приватные артефакты; он не подменяет Auth,
Files, PostgreSQL, S3, KMS или AV.

| Зона | Workloads |
| --- | --- |
| internal A | PostgreSQL writer/standby, отдельный local RO standby, Auth, Redis, Files, gateway-out, worker, AV/CDR, internal-clean и его KMS |
| DMZ A | два gateway-in, TCP balancer, HTTPS frontdoor, quarantine, delivery A и отдельный KMS |
| internal B | PostgreSQL writer/standby, отдельный local RO standby, Auth, Redis, Files, gateway-out |
| DMZ B | два gateway-in, TCP balancer, HTTPS frontdoor, delivery B и отдельный KMS |

Проверяется реальный путь cookie → HTTPS gateway-in → обратный mTLS tunnel →
Auth → scoped mTLS Files → PostgreSQL/S3; PUT/GET байтов идут напрямую в S3.
Регистрация событий User/NATS и UI не участвуют в файловом сценарии. Неиспользуемый
legacy gRPC client gateway-out соединён с Auth; разрешённые Files routes используют
свои отдельные Auth/Files clients. Все пароли и пользователи синтетические.

Два reverse tunnel подключаются к разным gateway-in через ограниченный TCP
round-robin helper на `30443`; он передаёт TLS без расшифровки. Gateway-in слушают
loopback `30444/30445` и имеют разные instance ID. Helper собирается из исходников,
его SHA-256 сверяется на каждой DMZ VM; одновременно допускается до 16 соединений.

Между DC допускаются только необходимые PostgreSQL/Redis replication и Files→S3
потоки по точным IP/портам. Исходные firewall chains MM-44 сохраняются.
Redis использует TLS, PostgreSQL и S3 — проверенный TLS, сервисы — mTLS.
AV/CDR не получают credentials; для их pod действует deny-all NetworkPolicy.
Kubelet на собственном internal A ограничивает pod до 128 PID; harness сверяет
live cgroup до теста и после восстановления. TCP probes из обоих парсеров должны
блокироваться к доступным PostgreSQL на своём узле и PostgreSQL/S3 в другом DC;
контрольный запрос из отдельного pod с той же CNI сетью без parser-label успешен
до и после. Настройка PID следует [руководству k3s](https://docs.k3s.io/security/hardening-guide).
Это тестовая конфигурация, не production-манифест.

Local RO standby нужен платформенному клиенту, который запрещает направлять
RO-пул на writer. Его `application_name=files_local_a/b` не входит в
`synchronous_standby_names=FIRST 1 (files_ro)`. Единственный подтверждающий
`remote_apply` peer расположен в другом DC. Без него подтверждение новых записей
невозможно; тест не меняет это требование.

Остановка VM сохраняет диски. Этот результат не доказывает восстановление после
необратимого уничтожения quarantine или физическую независимость DC: все VM
работают на одном Mac. Quarantine/internal-clean/worker остаются в A, поэтому
multipart resume при потере A проверяется после его возврата. Результаты девяти
форматов, отрицательного AV/CDR и физической очистки описаны отдельно в
[основном отчёте](files-data-plane.md).

## Сценарий

1. Настоящая Auth-регистрация и login, подготовка чужой и отозванной сессий.
2. Полный путь PNG до READY и проверка SHA-256 скачанного clean-объекта.
3. Двухчастная загрузка PNG: PUT первой части и подтверждение received part
   повторным CreateUpload с тем же ключом идемпотентности.
4. До остановки — запись устойчивого fence старого primary. Остановка обеих VM
   DC и повторное подтверждение их точных ID/состояний; только затем promotion.
5. Переключение маршрута на уцелевший DC. Сохранность READY metadata/checksum,
   чтение из его delivery/KMS, запрет чужому пользователю, anonymous и replay
   отозванной cookie; запрет выдачи незавершённой загрузки.
6. Новая запись наблюдается в серверном `SyncRep`, затем получает отказ по
   deadline/unavailable без успешного подтверждения и capabilities.
7. Возврат DC: подтверждение нового boot ID при том же machine/node identity;
   положительный fence marker текущего boot и отсутствие PostgreSQL listener.
   Старый primary пересоздаётся как standby через basebackup нового timeline.
8. Восстановление меж-DC sync peer, RO и Redis replication; unseal собственного
   KMS. Resume с той же cookie/key/id, без повторной отправки принятой части,
   завершение обработки, checksum и безопасный повтор неопределённой записи.
9. Те же проверки при потере второго DC; после восстановления Delete запрещает
   выдачу новой download capability.

Promotion и маршрутизацией явно управляет harness; это проверка процедуры
переключения, а не утверждение о наличии автоматического failover-контроллера.

## Запуск

Нужны работающий OrbStack, Docker CLI, Go из `go.work`, Python 3 и свободные
ресурсы для четырёх VM/парсеров. Standalone PostgreSQL и пользовательский kubeconfig
не используются. Запуск из корня, последовательно:

```sh
python3 -m unittest discover -s infra/files-dc-e2e -p 'test_*.py' -v
python3 infra/files-dc-e2e/run.py run --instance mm43-check
```

Первая команда занимает около секунды и не создаёт VM. Вторая собирает образы
локально, проверяет базовую topology, запускает зависимости и race-enabled probe;
после успеха удаляет только свой одноразовый стенд. Никаких registry push.
Отдельные этапы для диагностики:

```sh
python3 infra/files-dc-e2e/run.py up --instance mm43-check
python3 infra/files-dc-e2e/run.py test --instance mm43-check
python3 infra/files-dc-e2e/run.py down --instance mm43-check
```

После изменения исходников `test` отклоняет старую сборку. Любое изменение требует
нового стенда: `down` для прежнего instance, затем полный `run` с новым именем.
Это исключает зачёт новых исходников со старой конфигурацией или процессом.

Каждая privileged команда повторно проверяет MM-44 snapshot, использует immutable
machine ID и сверяет boot ID внутри VM непосредственно перед exec. Непрочитанная
или неизвестная роль PostgreSQL закрывает fence. При сбое доказательства harness
не снимает fence и сохраняет свои ресурсы для диагностики; `down` удаляет их
по процедуре владения MM-44. Команды без имени VM и `--all/--force` не используются.

## Артефакты

`.cache/files-dc-e2e/<instance>` имеет права `0700`; секреты/логи — `0600`.
`report.json` появляется только после обеих фаз и успешных утверждений probe.
В нём фиксируются дерево исходников, SHA probe и IDs фактически собранных образов;
дерево должно совпадать до сборки, до и после теста. Проверяются также ссылки и
содержимое образов в container runtime. Snapshot, stopped receipts и rebind proofs
сохраняются рядом. Credentials, cookie и signed URLs в публичный отчёт не входят.

`AUTH_REDIS_CONNECT_TIMEOUT=5s` задан только стенду: TLS bootstrap между VM
измеренно занимает около 1.3 с при прежнем default 1 с. Тайм-аут ограничен 10 с;
дедлайны проверки сессии сохраняются. Updater сигнатур получает 2 ГиБ памяти;
после обновления networked pod завершается, сканер остаётся изолированным.

HEAD перед выдачей download capability ограничен 3 с на delivery-копию:
на небольшом узле первый HTTPS-запрос превышал прежний лимит 1 с и давал ложный
`unavailable` при двух исправных копиях. Две последовательные попытки укладываются
в 10-секундный RPC; срок signed URL рассчитывается из оставшегося TTL.
