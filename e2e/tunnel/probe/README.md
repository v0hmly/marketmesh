# Continuous tunnel probe

Пакет `probe` принадлежит задаче MM-31 и реализует только независимое ядро
непрерывного трафика и сверки журналов.

Границы ответственности:

- `Runner` создаёт отдельные bounded read/mutating потоки, не повторяет
  mutating вызовы и возвращает client ledger даже при ошибке;
- `Invoker` — transport adapter к front door; он получает `context.Context` с
  deadline и возвращает только низкокардинальный `Response` без payload/raw
  error;
- `Mark` только фиксирует внешние lifecycle/fault события. Kubernetes, rollout
  и fault injection остаются у сценария-владельца;
- `WaitSteady` bounded ожидает success streak уже работающих потоков и не
  создаёт дополнительные запросы или retry;
- `Reconcile` сопоставляет client/internal ledger и обнаруживает missing,
  lost response, unexpected, duplicate, late и reordered записи;
- заполненный journal, пустой ledger, незавершённый request, stop timeout или
  неполный internal snapshot делают результат неполным и не могут дать pass.

Конфигурация дополнительно ограничивает поток значениями 100 000 RPS, 1 024
workers и очередью 100 000 элементов; client journal принимает не более
1 000 000 requests, а timeline — 4 000 000 events. Panic transport adapter
преобразуется в безопасный `internal_error`, помечает run неполным и не
переносит panic value в diagnostics.

Marker fields `fault_id`, `dc`, `zone`, `component`, `role`, `phase`, `result`
и `revision` проверяются конечными enum/allowlist; opaque fault ID ограничен
128, а revision — 64 ASCII-символами. Timeline получает монотонные `sequence`
и `offset`, вычисленные от `StartedAt`; отдельный `CompletionSequence`
сохраняет точный порядок concurrent responses даже при одинаковом offset.
Timeline не содержит payload, credentials, PII или raw errors.

Request ID имеет канонический вид из 32 lowercase hex-символов и декодируется
transport adapter в ровно 16 байт, требуемые fake internal contract. Для
mutating потока этот же opaque ID используется как уникальный idempotency key;
internal ledger возвращает только его SHA-256 digest.

Опубликованный контракт MM-29 подключён типами `FakeInvoker`,
`InstanceDirectory` и `LedgerCollector`. Directory строится direct discovery
каждой fake-internal replica до запуска traffic, поэтому response instance
однозначно преобразуется в `dc-a` или `dc-b`. Финальный ledger читается один
раз после остановки runner через явно сконфигурированные direct gRPC clients;
достижение limit, повтор instance, malformed entry, RPC error или panic клиента
делают snapshot неполным. Front door никогда не используется для чтения
ledger, а topology addresses и TLS credentials не попадают в типы artifacts.

Формат SLO/JSON/JUnit принадлежит MM-27 (`e2e/tunnel/spec`). Report adapter MM-31
будет добавлен после появления этого prerequisite в `dev`; пакет не создаёт
параллельную схему отчёта.
