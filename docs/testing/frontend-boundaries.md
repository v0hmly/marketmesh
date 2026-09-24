# Приёмка MM-87 на локальном OrbStack

Дата: 24.09.2026. Решение — [ADR-0016](../adr/0016-frontend-application-boundaries.md).
Стенд поднят обычным `task dev:up`, без reset, второго PostgreSQL-кластера
или переноса пользовательских данных. Сохраняются общий primary/replica и
остальные общие хранилища. Бизнес-портал сотрудников остаётся в MM-59.

## Проверки

| Область | Результат |
| --- | --- |
| Go workspace | fmt, arch, vet, race, build, isolated и mod-verify прошли |
| Протокол | lint, additive breaking check относительно dev, повторная генерация и self-test прошли |
| Frontend | 223 storefront и 18 staff unit-тестов; lint/typecheck приложений и общих пакетов |
| Границы импортов | 4 группы отрицательных проверок реального ESLint и политики |
| Production browser | 3: lazy chunks, отказ старого чанка с сохранением fragment, прямой seller CSS |
| Storefront UI browser | 15 существующих сценариев и доступность |
| Staff UI browser | 5: вход/приглашения/axe, поздний SSO после перехода и межвкладочной смены |
| Реальный staff browser | 3: OIDC, mTLS, PG, приглашения, чужая почта, повторный вход, отзыв, cookies и обе темы |
| Реальный buyer browser | 12 passed; 2 существующих opt-in сценария skipped на постоянном dev |
| PostgreSQL staff | race integration: одноразовое состояние входа, конкурентное приглашение, чужая почта, срок сессии |
| TLS | отсутствие cert, чужой CA и serverAuth-only отклонены; доверенный clientAuth допущен |
| Инфраструктура | 20 Python-тестов, в том числе прерванное обновление PKI |
| Документация/автоматизация | layout, локальные ссылки, actionlint, CI self-tests |

В buyer-прогон вошли профиль/CAS, адреса, темы, identity, аватар, аналитика,
регистрация с автоматическим входом, пересланная ссылка без входа другого браузера,
email-код и уведомления Mailpit. Пропущены специальный режим незавершённой проекции
User и отдельный opt-in тест перезапуска/renewal аватара; они не объявлены проверенными
этим прогоном. Изменения buyer-сессии покрыты существующими race/ownership сценариями.

Staff-прогон использует проверяемый серверный CA и client certificate, без
ignoreHTTPSErrors. Вторая вкладка с персональными данными скрывает их сразу после
смены сессии, пока загрузка нового login-чанка намеренно удерживается тестом.
Сырые cookies, пароли и invite-ссылки не попадают в browser-отчёт.

## Периметр

`task staff:perimeter` проверяет loopback bindings, состав корпоративной сети,
права RW/RO/TLS и запрет доступа staff-ролей к чужим БД. Из публичного ingress
запросы к `/staff`, assets и Staff RPC с правильными CA/SNI/Host/Origin и поддельными
forwarded-заголовками отклонены TLS (`certificate_required`). Проверены прямой IP
контейнера и опубликованный порт через `host.docker.internal`.

Отдельный bridge и IP-фильтр оказались недостаточны при SNAT OrbStack. Исправление —
обязательный корпоративный clientAuth на всём listener. Положительный реальный
browser-прогон доказывает доступность того же сервиса для разрешённого клиента.
Это локальная модель корпоративного доступа, не физически независимая сеть/VPN.

## Безопасность артефактов

Govulncheck всех модулей не обнаружил достижимых уязвимостей; в существующих
require-зависимостях account-local есть неиспользуемые findings, они не скрываются.
Pnpm audit: известных находок нет. Trivy 0.74.0 с базой 24.09.2026,
без ignore-списков, проверил OS и Go-бинарники новых образов — 0 findings:

| Образ | Проверенный config ID |
| --- | --- |
| staff | `sha256:c8f36f52f4c4d71401cad0de7b5d05faffe51dbbce159002c1f7a942535868ef` |
| staff-oidc | `sha256:638acf2c84b13a72631a6959fc350049ebbeacf0a54fa96d550496628256912a` |

Сканер: `aquasec/trivy@sha256:62b1e65e8869bc4b4c6aa4fa2b21595256c7c2f6018a9d9ad61caf87187c1969`.
Исходные JSON и tar находятся в игнорируемой `.cache/mm87-security/`, не в Git.
SHA-256 отчётов: staff `4a1036ed5badc77a4ed0b8dc18f3d253d71b083b33a427ff187129dc1d5fa4a9`,
staff-oidc `9f17c5aba595d51cd9b1b1c7214bc42c6efd05fab8d71eb21bc79d08d2dca345`.
Ранее принятые [CVE Files/dev](../security/files-dev-acceptance.md) этим изменением
не объявляются исправленными.

GitHub сохраняет существующий один `verify` job. Финальные head/base SHA,
независимое ревью и hosted CI фиксируются в исходном PR. Подготовленный `done`
в его последнем коммите становится фактическим завершением после merge и CI.
