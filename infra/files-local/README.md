# Изолированный стенд Files (MM-43)

Стенд проверяет приватный файловый контур: PostgreSQL с синхронной физической
репликой, четыре SeaweedFS зоны, OpenBao transit, сетевой sandbox и ClamAV.
Он имеет собственный Compose project `marketmesh-files-local`; остальные
сервисы репозитория, Mailpit и Rybbit не затрагиваются.

```sh
python3 infra/files-local/local.py up
python3 infra/files-local/local.py test
python3 infra/files-local/local.py status
python3 infra/files-local/local.py down
```

Нужны Go 1.27, Python 3, Docker Compose/BuildKit, доступ к официальным registries,
GitHub Releases, Debian repositories и зеркалам ClamAV. Рекомендуется выделить
Docker не менее 10 ГБ RAM. Контейнеры собираются локально, без публикации.
Пакеты ClamAV 1.5.4 для arm64/amd64 проверяются по зафиксированным SHA256.

`up` создаёт `.cache/files-local` с правами 0700, генерирует credentials,
одноразовый CA и сертификаты на 12 часов. CA private key не сохраняется.
Root/unseal token OpenBao остаётся в приватном локальном состоянии и не
монтируется в worker/parser. Это bootstrap для локальных тестов, а не
production key management. Короткие KMS tokens также живут 12 часов.

```sh
# После истечения сертификатов/tokens: пересоздать только этот контур,
# сохранив данные, метаданные и ключи transit.
python3 infra/files-local/local.py renew
```

`test` временно останавливает worker, создаёт шесть безопасных офисных fixtures,
запускает race integration binary и возвращает worker. Fixtures генерируются
без пользовательских документов; LibreOffice работает без сети. Тестируются
девять форматов, SHA, две delivery-копии, повторы/возобновление/удаление,
owner isolation, квоты, RO-права и синхронная видимость метаданных,
EICAR/ZIP-бомба, восстановление объектов приложения после рестарта,
недоступность одной delivery-копии, CORS и tamper/replay подписанных частей.
Проверяются также up/down/up миграции и остановка синхронной реплики:
commit ждёт `SyncRep`, затем после восстановления реплики подтверждённая запись
сразу видна на ней. Потерю целого DC этот тест не моделирует.

```sh
python3 infra/files-local/control_e2e.py
```

Отдельный E2E создаёт временный Compose project с настоящими Auth/User,
двумя gateway-in и gateway-out, выполняет все пять Files RPC, перезапускает
gateway-out и возобновляет upload с тем же ключом. Проверяет запрет до READY,
другого владельца и logout. Клиент — HTTPS Connect с cookie jar, а не браузерный
UI; CORS проверяется отдельными S3 тестами. Временные account volumes удаляются
только у созданного этим запуском project, Files volumes сохраняются.
Не запускайте одновременно с `local.py test`: оба теста управляют worker.

## Сети и полномочия

На loopback опубликованы только S3 quarantine `18343`, delivery A `18344`,
delivery B `18345` и bootstrap OpenBao `18210`. Внутренний clean не публикуется.
Временный CA не устанавливается в системное хранилище доверия.
CORS допускает только `https://localhost:8443`; у quarantine только PUT,
у delivery только GET, с конечным списком заголовков. Анонимного чтения нет.
JSON-конфигурация fixtures использует внутренние DNS имена для test runner;
публичные origins реального клиента задаются отдельно при принятии ADR.

| Роль | Полномочия |
| --- | --- |
| Control | HEAD quarantine/delivery (в IAM S3 это GetObject); нет internal-clean |
| Upload capability | Только PutObject в quarantine; явный запрет Get/List/Delete |
| Download capability | Только GetObject соответствующей delivery зоны |
| Worker | Get/Put/Delete/List в своей зоне, без административных операций |
| Bootstrap | Создание buckets, CORS/lifecycle/IAM и ограниченных KMS tokens |
| Parser/AV | Нет сети, S3/KMS/DB credentials; только private Unix sockets |

S3 mini объединяет внутренние компоненты одного storage узла в контейнере.
В production их внутренние порты отдельно ограждаются network policy;
сам локальный mini не является доказательством production isolation/HA.
OpenBao имеет отдельные ключи/политики quarantine, internal-clean и delivery A/B.
Один локальный KMS и физический Docker host не моделируют независимые DC.

Files worker — статический binary в scratch, без shell. Sandbox имеет read-only
root, tmpfs, одну CPU, 1 ГБ RAM и 128 PID; принимает одно задание и перезапускается.
AV имеет 2 ГБ RAM, актуальность сигнатур проверяется до каждого scan.
`signatures` получает сеть только для обновления базы; daemon работает без сети.
Обновить сигнатуры можно повторным `up` (данные сохраняются).

`down` не удаляет volumes. Обычный rollback также сохраняет volumes и KMS keys.
Для удаления тестовых данных оператор должен явно удалить volumes именно
проекта `marketmesh-files-local` и его `.cache/files-local`; глобальный
`docker system prune` для этого не используется.

## Границы проверки перед публичным включением

Основной стенд не запускает публичные маршруты. Только отдельный `control_e2e.py`
включает их на loopback `18443` в одноразовом проекте. `control.json` содержит
только приватную конфигурацию S3/DB для тестов, без включённого listener.
Параметры control/Auth/gateway перечислены в [README Files](../../services/files/README.md).

До включения нужны принятие [ADR-0015](../../docs/adr/0015-private-file-data-plane.md),
независимое security review и полный E2E на двух DC: обрыв туннеля/worker pod,
потеря DC с подтверждённой синхронной сохранностью метаданных, fencing старого
writer, переключение routing и восстановление загрузки тем же idempotency key.
Наличие двух delivery buckets само по себе не подтверждает весь DC failover.

Результаты и незакрытые условия приёмки: [проверки MM-43](../../docs/testing/files-data-plane.md).
