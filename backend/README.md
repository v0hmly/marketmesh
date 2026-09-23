# Backend

Go workspace находится в `backend/go.work`. Сервисы — в `services/`, общие
библиотеки — в `platform/`, контракты — в `api/gen/go/`, декодер туннеля —
в `api/tunnel/`. `tools/` содержит Go-автоматизацию, `e2e/` — тесты туннеля,
`fixtures/` — Go-помощники инфраструктурных проверок и контейнерные workspace.

Из корня репозитория используйте `task build`, `task test-race`, `task arch`
и `task verify`. Для отдельного пакета:

```bash
cd backend
go test ./services/auth/internal/domain/...
```

Если команда должна работать из корня, задайте `GOWORK="$PWD/backend/go.work"`.
Модули и версии зависимостей описаны в [ADR-0012](../docs/adr/0012-monorepository-and-go-workspace.md).
Go import paths сохранены совместимыми с версиями до переноса; они не являются
путями для `cd` или Docker `COPY`.

API обновляется из корня: `task api:generate`. Go workspace не управляет схемами
или frontend-зависимостями: они находятся в `api/` и `frontend/` соответственно.
