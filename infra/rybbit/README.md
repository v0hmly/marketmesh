# Локальная аналитика Rybbit (MM-75)

> Постоянный dev использует общий [корневой стенд](../dev/README.md) и `task dev:up`.
> Описанные ниже отдельные Compose — интеграционные фикстуры и legacy cleanup;
> прямой persistent `up` отключён. Не создавайте второй набор баз для разработки.

```sh
task analytics:up
task analytics:status
```

Нужны Docker Compose, Task и Python 3 со стандартной библиотекой. Dashboard:
`http://localhost:8310`. Команда создаёт локальный аккаунт, организацию
`MarketMesh local` и отдельный сайт `MarketMesh local storefront` через API.
Путь к `admin.json` с логином и случайным паролем выводится после запуска;
пароль не печатается в логи. Каталог `infra/rybbit/.state/<project>/` и файлы имеют
права `0700`/`0600`, исключены из Git. Не удаляйте состояние при сохранённых volumes:
оно содержит пароли PostgreSQL, Redis, ClickHouse и ключ сессий Rybbit.

Rybbit backend/client закреплены на v2.9.0. Состав контура: backend, dashboard,
ClickHouse, PostgreSQL, Redis и Caddy. Только Caddy публикует loopback-порт;
хранилища доступны во внутренней сети. PostgreSQL Rybbit относится к стороннему
инструменту и не меняет владение базами Auth/User. Сохранён upstream ClickHouse
config с локальными лимитами: 2 ГиБ контейнеру, 512 МиБ запросу, два query threads;
backend использует один worker. Телеметрия самого Rybbit отключена.

## Подключение кабинета

Сначала `analytics:up`, затем:

```sh
export ACCOUNT_RYBBIT_ENABLED=true
export RYBBIT_SITE_ID="$(cat infra/rybbit/.state/marketmesh-rybbit-local/site-id)"
task account:up
```

Эти переменные нужны для всех команд этого account-проекта, включая `down`.
Для другого проекта Rybbit также задайте `RYBBIT_PROJECT` с префиксом
`marketmesh-rybbit-`. Dashboard-порт меняется через `RYBBIT_PORT`. При смене порта
сохраните текущий контур и выберите новый проект; автоматической смены секретов
и конфигурации поверх данных нет.

Storefront собирается с `VITE_RYBBIT_ENABLED=true`; по умолчанию этот флаг выключен.
Браузер отправляет только `POST /analytics/track` на свой HTTPS origin, а локальный
frontdoor соединяется с Rybbit по отдельной Docker-сети. CSP остаётся `self`.
Административный API Rybbit через кабинет не проксируется. Сборки без флага,
обычный `account:test` и остальные тесты не отправляют события. Для внешней
установки потребуется отдельно настроить её endpoint и site; production-аналитика
этой локальной конфигурацией не затрагивается.

Сайт в Rybbit назван `marketmesh.localhost`, поскольку API требует домен с точкой.
Фактический hostname локальных событий — `localhost`; Rybbit поддерживает такие
события независимо от зарегистрированного домена.

## События и приватность

- `pageview`: `/login`, `/register`, `/account`, `/account/addresses`, `/account/settings`.
  Учитывается только успешная навигация; query/hash и повтор текущего пути не создают дубль.
  После MM-97 `/account` и `/account/settings` только перенаправляют в разделы кабинета, поэтому
  из кабинета сейчас приходят лишь просмотры адресов. Новый список путей согласуется в MM-109.
- `registration_request_completed`: завершён запрос регистрации, без утверждения,
  что создан новый пользователь (Auth не раскрывает существование логина).
- `login_succeeded`: успешный вход.

Не отправляются user ID, логин, пароль, cookies, Authorization, referrer, query/hash,
заголовок страницы, значения форм и произвольные event properties. На сервере
есть отдельный allowlist путей и событий, лимит тела 1 КиБ, лишние поля отклоняются.
User-Agent используется Rybbit для типа браузера; идентификация конкретного аккаунта
не выполняется. Session replay, автосбор кликов/форм, outbound, ошибки, web vitals
и запись IP в настройках сайта выключены. Do Not Track останавливает отправку.
Недоступность Rybbit не блокирует UI/Auth: запросы без ожидания, с таймаутом и
ограничением числа одновременных отправок, без повторов и очереди в storage.

## Проверка и остановка

```sh
task analytics:verify
# Проверка реального кабинета с включённой локальной аналитикой:
ACCOUNT_RYBBIT_ENABLED=true RYBBIT_SITE_ID=1 ACCOUNT_LOCAL_PORT=18443 task account:test

task analytics:down
# Явно удалить аналитику и учётную запись выбранного локального проекта:
task analytics:clean
```

`analytics:verify` создаёт одноразовый проект на порту 18310 (переопределяется
`RYBBIT_TEST_PORT`), создаёт сайт, отправляет событие, читает его через API,
перезапускает контур и проверяет сохранность. Затем удаляет только собственные
тестовые containers/volumes. `account:test` проверяет браузерные запросы, исключение
секретов, отсутствие дублей и успешный вход при оборванной отправке аналитики.
Обычный `down` сохраняет данные. Команды отказываются работать с ресурсами другого
worktree; параметры проекта должны быть одинаковыми во всех командах.

Откат: выключить `ACCOUNT_RYBBIT_ENABLED`, пересобрать `account:up`, остановить
`analytics:down`. Аналитические volumes можно сохранить; базы приложений не меняются.

Источники: [Compose](https://rybbit.com/docs/self-hosting-guides/self-hosting-manual),
[HTTP API](https://rybbit.com/docs/api/sending-events),
[localhost](https://rybbit.com/docs/localhost-tracking).
