# ADR-0014: E2E topology на OrbStack VM с k3s вместо kind

- Статус: Принято
- Дата: 2026-09-03
- Авторы: команда проекта
- Заменяет: нет
- Заменено решением: нет

## Контекст

Disposable двух-DC E2E topology (MM-28) строилась на четырёх kind-кластерах —
Docker-контейнерах с pinned node image. Такая модель имела три проблемы:

- изоляция зон держалась на контейнерных Docker-сетях и iptables внутри
  контейнера, то есть «узел» был процессом в общем ядре host, а не
  самостоятельной машиной;
- fault targets (MM-38..MM-41) были привязаны к Docker-специфичной identity
  (SandboxID, NetNS, network endpoints), что делало контракт хрупким и
  непереносимым;
- загрузка локально собранных образов в кластеры (`kind load`) вообще не была
  реализована, поэтому манифесты полагались на `imagePullPolicy: IfNotPresent`
  без гарантии наличия образа.

Пользователь принял решение (2026-09-03) полностью заменить kind-контур, а не
поддерживать два варианта.

## Решение

E2E topology реализуется на четырёх одноразовых OrbStack VM
(`ubuntu:24.04`, 2 vCPU, 2 GiB, 20 GiB) с именами `<instance>-<logical>`
(instance по умолчанию `mm44`). В каждую VM устанавливается закреплённый по
SHA-256 бинарник k3s `v1.36.4+k3s1` (Kubernetes 1.36.4), запускаемый
каноническим systemd unit с `--disable=traefik --disable=servicelb
--disable=metrics-server --write-kubeconfig-mode=0600 --node-ip=<vm-ip>
--tls-san=<vm-ip>`.

- Kubeconfig извлекается из VM, переписывается на `https://<vm-ip>:6443` и
  context `<instance>-<logical>` и хранится с правами `0600` в
  `.cache/e2e-topology/<instance>/`.
- Зональная изоляция выполняется идемпотентными iptables-цепочками внутри VM
  как полная mesh-политика: каждая VM направляет INPUT от IPv4 каждой из трёх
  других VM в dedicated цепочку, принимает ESTABLISHED,RELATED, а DMZ VM —
  дополнительно tcp/30443 только от internal VM того же DC; весь остальной
  VM-to-VM трафик (cross-DC, DMZ↔DMZ, internal↔internal) отбрасывается. Это
  паритет kind-модели, где cross-DC трафик был невозможен из-за разных
  Docker-сетей. Трафик host не ограничивается. Матрица проверяется
  TCP-probe через transient systemd units.
- Fault targets переопределены на VM identity: стабильный machine ID (ULID)
  OrbStack + MAC/IPv4 первого non-loopback интерфейса + boot ID
  (`/proc/sys/kernel/random/boot_id`) как поколение запуска. Схемы
  `targets/v1`, `target-validation/v1`, `target-rebind/v1` и digest-цепочки
  сохранены; stopped target валидируется только со стороны host без входа в VM.
- Netfilter state не переживает stop/start VM, поэтому rebind после
  доказательства identity пересоздаёт zone firewall rebound машины (peer-IP
  не меняются, правила соседей остаются валидными) и ждёт `Ready` узла k3s,
  прежде чем выдать новый snapshot chaos-потребителю.
- Загрузка образов реализована с нуля как `e2e-topology load-images`:
  `docker save` на host → передача tar в каждую VM →
  `k3s ctr -n k8s.io images import` → проверка точного тега в `images ls`.
  Манифесты E2E workloads используют `imagePullPolicy: Never`.
- Network chaos для будущих сценариев выполняется через `tc netem`/`iptables`
  внутри конкретной VM; topology tool предоставляет только resolve/validate
  identity.

## Рассмотренные альтернативы

### Сохранить kind поверх OrbStack Docker

Достоинства: минимальные изменения, Kubernetes 1.37. Отказано: сохраняет
контейнерную модель изоляции и Docker-привязанные fault targets, не закрывает
дыру с загрузкой образов.

### k3s в одной общей VM с namespaces

Достоинства: меньше ресурсов host. Отказано: не даёт изоляции на уровне машин
и независимых control plane на DC/зону, ломает модель fault targets.

### colima/lima вместо OrbStack

Достоинства: независимость от OrbStack. Отказано: OrbStack уже является
обязательным локальным runtime проекта; второй VM-стек удвоил бы поддержку без
выигрыша для E2E.

## Последствия

### Положительные

- Изоляция зон поднята на уровень отдельных машин с собственным ядром.
- Fault target identity опирается на стабильные атрибуты машины, а не на
  внутренности Docker engine.
- Дыра с загрузкой образов закрыта; пропущенная загрузка обнаруживается сразу
  (`imagePullPolicy: Never`).
- Kubeconfig/context контракт для потребителей сохранён; ripple ограничивается
  источником IP-адресов (VM IP вместо container IP).

### Отрицательные и риски

- Kubernetes снижается с 1.37.0 (kind node image) до 1.36.4 (последний
  стабильный k3s на дату решения); для E2E функционально достаточно,
  расхождение пересматривается при обновлении пинов.
- Четыре VM × 2 GiB создают заметную нагрузку на host; при нехватке ресурсов
  допускается уменьшение лимитов VM отдельным изменением пинов.
- OrbStack становится жёсткой зависимостью локального E2E (он уже обязателен
  для локального окружения проекта).
- Базовый образ OrbStack `ubuntu:24.04` не содержит iptables/nft, поэтому `up`
  устанавливает pinned apt-версию `iptables=1.8.10-3ubuntu2` (iptables-nft
  поверх nf_tables) в каждую VM. Это добавляет egress VM в репозиторий Ubuntu
  во время `up`; при смене candidate в noble установка падает fail-fast и пин
  пересматривается явно, а не drift-ит молча.
- Стоп-времена VM не наблюдаемы через orbctl, поэтому rebind доказывает
  переход stop→start сменой boot ID, а не временными метками контейнера.

## Защитные и эксплуатационные требования

- Все операции с VM выполняются через argv-only вызовы orbctl с bounded
  timeouts и bounded output; shell-строки запрещены; sudo только с `-n`.
- Любая мутация предваряется ownership-проверкой точного имени
  `<instance>-<logical>`; cleanup fail-closed удаляет только ресурсы instance.
- Бинарники k3s и kubectl закреплены версией и SHA-256; скачивание только по
  HTTPS с allowlist hosts.
- VM одноразовые; iptables-правила внутри VM не сохраняются между запусками.

## Проверяемые инварианты

- `task e2e:topology:verify` выполняет два полных цикла up → ready → targets →
  down без остаточных VM.
- `ready` доказывает разрешённый same-DC internal→DMZ:30443 и запрещённые
  internal→DMZ:30444, DMZ→internal:30443, cross-DC internal→DMZ:30443 и
  DMZ↔DMZ:30443 для обоих DC.
- `load-images` завершается ошибкой, если точный тег отсутствует в containerd
  любой из четырёх VM.
- `targets validate` отклоняет snapshot после неучтённого reboot VM (смена
  boot ID) и после замены machine ID/MAC/IPv4.

## Отложенные вопросы

- Перевод destructive-сценариев MM-32/34/35/36 на новую topology выполняется в
  их собственных ветках после слияния MM-44.
- MM-37 (пересмотр kind node image) становится obsolete: kind node image
  больше не используется.
- Независимое версионирование микросервисов и CI-интеграция E2E (MM-6) не
  затрагиваются.

## Связанные документы

- [Обзор архитектуры](../architecture/overview.md)
- [README topology](../../tools/e2e-topology/README.md)
- [ADR-0001: зоны доверия и модель доступа](0001-trust-zones-and-access-model.md)
