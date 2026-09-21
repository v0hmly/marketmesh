# Проверки приватного файлового контура MM-43

Дата проверки: 2026-09-21. База ветки: `85d536223204a698513a65b0a9cd3a155e3d7cd5` (`dev`).
Реализация и стенд подготовлены для ревью; публичный запуск и закрытие задачи
не разрешены этим отчётом. [ADR-0015](../adr/0015-private-file-data-plane.md)
остаётся предложенным, `FILES_BROWSER_ENABLED` по умолчанию выключен.

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
go test ./services/files/internal/adapter/out/content -run '^$' -fuzz Fuzz -fuzztime 30s -parallel 1
go test ./services/files/internal/adapter/out/raster -run '^$' -fuzz Fuzz -fuzztime 30s -parallel 1
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
В репозитории на дату проверки нет GitHub Actions workflows; внешний CI
для этого PR не подтверждён. Набор обязательных локальных проверок выполнен,
но отсутствие CI не оформляется как автоматическое исключение.
Digest runtime образов и findings можно повторно получить указанной командой;
результат зависит от версии advisory database и пакетов parser image при сборке.

Остаточные находки не считаются автоматически принятым риском:

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
  исключение с владельцем и сроком. Исключение в MM-43 не запрашивалось и не получено.

## Открытые условия приёмки

1. Независимое ревью ADR, IAM/KMS, sandbox и реализации; принятие ADR.
2. Разбор/устранение контейнерных CVE и подтверждённый обязательный CI.
3. Полный E2E на двух независимых DC: потеря DC, fencing старого writer,
   продвижение синхронной реплики, routing и продолжение подтверждённой загрузки.
   Локально проверены рестарт туннеля/объектов приложения, отказ delivery endpoint
   и остановка реплики. Один Docker host, один KMS и два bucket не доказывают
   отказоустойчивость независимых DC.
4. После этих проверок — слияние в dev. Только затем допускается `done` MM-43.

[Runbook Files](../../services/files/README.md) описывает ограничения производной,
ротацию, инциденты и откат с сохранением DB, bucket и ключей KMS.
