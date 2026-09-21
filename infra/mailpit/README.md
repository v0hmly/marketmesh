# Mailpit для локальной разработки (MM-76)

Mailpit принимает письма от локального приложения и показывает их в браузере.
Это отдельный небольшой Compose-контур: для проверки писем не нужны PostgreSQL,
Auth или observability. Нужны Docker Compose и Task; для `mail:verify` — Python 3
со стандартной библиотекой.

```sh
task mail:up
task mail:status
task mail:down
```

Web UI: `http://localhost:8025`. SMTP с хоста: `127.0.0.1:1025`, без TLS и
авторизации. Оба опубликованных порта привязаны к loopback. Письма хранятся в
Docker volume `mail`; обычный restart и `mail:down` их сохраняют. Ограничения:
5000 последних сообщений и 10 МБ на письмо. Внешняя пересылка и relay не настроены.
Проверки новых версий и SMTP reverse DNS отключены.

Для другого worktree или портов передавайте одинаковые значения каждой команде:

```sh
MAILPIT_PROJECT=marketmesh-mailpit-demo MAILPIT_UI_PORT=18025 MAILPIT_SMTP_PORT=11025 task mail:up
MAILPIT_PROJECT=marketmesh-mailpit-demo MAILPIT_UI_PORT=18025 MAILPIT_SMTP_PORT=11025 task mail:down
```

Из контейнера, которому доступен host gateway Docker Desktop/OrbStack, SMTP
доступен по `host.docker.internal:1025`, если этот Docker runtime проксирует
loopback-порты хоста. Более переносимый способ — подключить отправляющий сервис
к сети `<MAILPIT_PROJECT>_mail` и использовать `mailpit:1025`. Сеть создаётся
командой `mail:up`; в другом Compose-файле объявите её как external. Подключайте
только сервис-отправитель, не браузер и не публичный gateway. `infra/account-local`
пока не отправляет email: эта задача предоставляет SMTP-приёмник, а не меняет
сценарии регистрации Auth.

| Команда | Действие |
| --- | --- |
| `mail:config` | Проверить Compose |
| `mail:up` | Запустить и дождаться health check |
| `mail:logs` | Читать логи |
| `mail:down` | Остановить, сохранив письма |
| `mail:clean` | Явно удалить контейнер и все письма выбранного проекта |
| `mail:verify` | Проверить SMTP, API, UI и restart в одноразовом проекте |

Проверка использует отдельные порты 18025/11025; их можно переопределить через
`MAILPIT_TEST_UI_PORT` и `MAILPIT_TEST_SMTP_PORT`. В конце удаляются только ресурсы
созданного проверкой проекта. Обычные команды отказываются менять ресурсы с
меткой другого worktree.

Образ закреплён на `axllent/mailpit:v1.31.2`. Конфигурация основана на
[официальном Docker-образе](https://mailpit.axllent.org/docs/install/docker/) и
[параметрах Mailpit](https://mailpit.axllent.org/docs/configuration/runtime-options/).
Для отката остановите этот контур командой `mail:down` и вернитесь к предыдущему
commit; сохранённый volume можно оставить. Это локальный инструмент, не SMTP
для внешней доставки.
