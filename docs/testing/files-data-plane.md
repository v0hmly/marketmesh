# Проверки приватного файлового контура MM-43

Исходная проверка: 2026-09-21. База ветки: `85d536223204a698513a65b0a9cd3a155e3d7cd5` (`dev`).
Исторические результаты ниже относятся к указанным датам. Текущая область
приёмки dev и остаточные CVE — в [решении владельца](../security/files-dev-acceptance.md);
[полный локальный DC E2E](files-dc-e2e.md) описан отдельно.

## Повторная проверка замечаний PR #91 — 2026-09-22

Ветка обновлена слиянием `dev` (`91e0993f4bf7d1511b91faad6916d3c4bd3040ea`).
Конфликт Taskboard разрешён без потери новых задач; MM-43 остаётся `in_progress`.
Внутренние Go-зависимости затронутых модулей закреплены на опубликованный
merge `210a89db568890938557d070a1254411ff5045cc`: он содержит одновременно новые
Files-контракты/Scope и актуальные Auth-контракты из dev. Внешние версии сохранены.

Исправлены приоритет SCANNING/REPLICATING перед периодической очисткой,
передача проверенного Auth subject/session в control-аудит и безопасная
структурированная диагностика worker по file ID, состоянию, этапу и классу ошибки.
ADR явно ограничивает текущую модель личным tenant == subject; README содержит
пропускную способность одноразового sandbox и влияние последовательного worker.

Повторно пройдены:

- Форматирование, архитектура, vet, race и сборка всех 11 Go-модулей.
- Настоящие `GOWORK=off go test/build -mod=readonly ./...` для каждого модуля,
  затем `go mod verify`, без временных replace и модификации модулей при проверке.
- Регрессия Claim на PostgreSQL 18.6: 128 старых READY/tombstone,
  новые SCANNING/REPLICATING, живой lease, неистёкший upload и `cleanup_after`;
  после обработки очистка продолжается, при следующем часовом проходе новый
  SCANNING снова выбирается раньше cleanup.
- Проверки аудита успешного/запрещённого доступа, отказа Auth и поддельных
  metadata; принципал только от успешного Verify, без JWT/URL/сырых ошибок.
- Регрессии worker: пустая очередь, missing object, storage/DB/CDR/AV,
  конфликт, timeout, отклонение содержимого и ошибка фиксации REJECTED;
  сохранены `errors.Is`, в логах есть класс/этап/файл без текста зависимости.
- Frontend lint/typecheck/unit/build; protobuf lint/breaking/generate-check
  относительно текущей dev и self-test; actionlint, негативные self-tests CI.
- `govulncheck`: 0 достижимых и импортируемых уязвимостей; три находки только
  на уровне требуемых модулей без вызова уязвимого кода. `pnpm audit`: 0.
- Локальная сборка runtime и sandbox без публикации образов.

Команда SQL-регрессии из корня (DSN указывает на **пустую одноразовую** тестовую БД;
схема и записи создаются внутри откатываемой транзакции):

```sh
FILES_POSTGRES_TEST_DSN='postgres://postgres@127.0.0.1:PORT/queue_test?sslmode=disable' \
  go -C backend test -race -tags=integration ./services/files/internal/adapter/out/postgres -v
```

В dev теперь есть [базовый CI MM-6](../ci.md); состояние обязательного `verify`
и отчёт независимого ревью сохраняются в [PR #91](https://github.com/v0hmly/marketmesh/pull/91).
Ниже сохранён исторический отчёт полного стенда от 21.09. Его результаты
AV/CDR/S3/KMS не подменяют новый четырёх-VM прогон. Остаточные CVE приняты только
для текущего dev с пересмотром 22.10.2026; обязательного Files-off условия нет.

## Воспроизведение

```sh
task verify
python3 infra/files-local/local.py up
python3 infra/files-local/local.py test
python3 infra/files-local/control_e2e.py
# Повторить только миграции и отказ синхронной реплики:
python3 infra/files-local/database_checks.py
```

Стенд имеет отдельные project, сети и volumes. Команды последовательные:
тестовые процессы управляют worker и репликой только этого стенда.
Для fuzz из корня, отдельно для каждого target:

```sh
go -C backend test ./services/files/internal/adapter/out/content -run '^$' -fuzz Fuzz -fuzztime 30s -parallel 1
go -C backend test ./services/files/internal/adapter/out/raster -run '^$' -fuzz Fuzz -fuzztime 30s -parallel 1
```

## Пройденные проверки

- `task verify`: protobuf lint/breaking/generate-check/toolchain self-test,
  форматирование, vet, все модульные/race-тесты workspace, сборка и архитектура.
- Девять форматов: PNG, JPEG, PDF, DOCX, XLSX, PPTX, ODT, ODS, ODP. Полный путь
  quarantine → структурная проверка/CDR → AV исходника и производной →
  internal-clean → две проверенные delivery-копии → download SHA → tombstone.
- Настоящие HTTPS Auth/два gateway-in/gateway-out/Files: все пять операций,
  перезапуск gateway-out и возобновление с тем же ключом, отказ до READY,
  изоляция чужого владельца, запрет после удаления и после logout.
  Клиент — Connect/cookie jar, браузерного UI в задаче нет. CORS проверяется
  отдельным тестом разрешённого и чужого Origin.
- Conditional PUT, подмена метода/key/размера/checksum/подписи, replay,
  конкурентные записи, восстановление принятых частей, потерянные ответы,
  owner isolation, гонки Create/Complete/Delete и fencing worker.
- Реальные EICAR, ZIP-бомба, ограничения MIME/XML/архивов; отрицательные
  AV/CDR/репликация сценарии, fuzz структурного фильтра и RGB framing по 30 с.
- Квота 24 параллельных Create: 10 успешных и 14 отказов, RO не пишет,
  подтверждённые метаданные сразу видны на синхронной реплике.
- Up/down/up миграций в отдельной временной БД. При остановке реплики
  подтверждение записи ждёт серверного `SyncRep`; после запуска реплики
  commit завершается и запись сразу читается на ней.
- Scoped mTLS: неверные environment/cluster/namespace/service account,
  legacy URI и отсутствие TLS отвергаются, смена pod UID права не меняет.
  Auth проверяется заново, отозванная сессия отвергается. В аудите нет URL/JWT.
- `govulncheck` для Files/Auth/gateway: 0 достижимых и 0 уязвимостей импортируемых
  пакетов; 3 записи в требуемых модулях без использования уязвимого кода.
- Trivy 0.74.0 secret scan изменённых исходников: 0 находок.

## Сканирование образов

Trivy 0.74.0, база на дату проверки; без ignore-списков и без удаления package
metadata. Числа — сырые записи package/advisory, включая повторения пакетов
и возможные ошибки сопоставления версий, а не число эксплуатируемых дефектов.

| Образ | Critical | High | Medium | Low | Unknown |
| --- | ---: | ---: | ---: | ---: | ---: |
| Files scratch | 0 | 0 | 0 | 0 | 0 |
| Auth и gateway (E2E) | 0 | 0 | 6 | 0 | 3 |
| CDR sandbox | 1 | 72 | 130 | 210 | 11 |
| ClamAV 1.5.4 | 0 | 55 | 50 | 56 | 2 |
| SeaweedFS 4.41 | 0 | 13 | 18 | 17 | 7 |
| OpenBao 2.6.2 | 0 | 11 | 12 | 13 | 1 |
| PostgreSQL 18.6 | 16 | 102 | 197 | 156 | 9 |

Команда для каждого локального образа или digest из Compose:

```sh
trivy image --scanners vuln --format json --output report.json IMAGE
```

Исходные JSON и логи хранятся локально в `.cache/files-*-vulnerabilities-final.json`,
`.cache/files-final-verify.log`, `.cache/files-final-integration.log`,
`.cache/files-control-e2e.log`, `.cache/files-database-checks.log`.
Это локальные отчёты, их наличие не является результатом GitHub CI.
На дату исходной проверки 21.09 в репозитории не было GitHub Actions workflows; внешний CI
для этого PR не подтверждён. Набор обязательных локальных проверок выполнен,
но отсутствие CI не оформляется как автоматическое исключение.
Digest runtime образов и findings можно повторно получить указанной командой;
результат зависит от версии advisory database и пакетов parser image при сборке.

Остаточные находки сохраняются; их явная приёмка только для текущего dev
оформлена [отдельным решением](../security/files-dev-acceptance.md):

- В sandbox Debian 13.7 остаётся Critical `CVE-2026-6653` в libxml2; Debian
  отмечает текущий trixie пакет как vulnerable. Предварительный запрет DTD,
  отсутствие сети и одноразовый контейнер ограничивают сценарий атаки,
  но не заменяют исправление/согласованное решение по риску.
  [Debian security tracker](https://security-tracker.debian.org/tracker/CVE-2026-6653).
- Для ClamAV Trivy сопоставляет официальный upstream `.deb` с advisory Debian.
  Как минимум CVE-2026-20339/20345/20346/20347/20348 исправлены в используемой
  1.5.4 согласно [официальному релизу](https://github.com/Cisco-Talos/clamav/releases/tag/clamav-1.5.4).
  Они сохранены в сырых числах; остальные записи этим утверждением не закрываются.
- У pinned SeaweedFS/OpenBao/PostgreSQL есть исправляемые находки в зависимостях
  и базовых пакетах. В OpenBao встречается псевдоверсия Go module `v0.0.0-…`
  при binary 2.6.2, поэтому часть сопоставлений требует ручной проверки.
  Это не основание скрывать остальные findings. До приёмки нужны обновлённые
  образы/сборки и повторный S3/KMS/DB compatibility E2E либо явное ограниченное
  исключение с владельцем и сроком. На 21.09 исключение ещё не было получено;
  решение владельца для текущего dev от 22.09 приведено выше.

## Текущая приёмка и последующие проверки

[Решение владельца для dev](../security/files-dev-acceptance.md) заменяет
предложение принять только отключённый код. MM-88 сохраняет CVE Files,
MM-67 — общие PostgreSQL/account образы. MM-89 относится к будущему окружению
с физически независимыми fault domains; локальный полный stop/start DC
входит в текущую MM-43 и должен быть пройден до merge.

Независимое ревью и обязательный CI сохраняются. Статус MM-43 меняется на done
только после merge и успешной проверки интеграционной ветки. Конкретный результат
четырёх-VM сценария фиксируется в [DC отчёте](files-dc-e2e.md).
