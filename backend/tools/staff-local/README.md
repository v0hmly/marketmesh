# Локальный тестовый OIDC

Одноразовый IdP для dev/проверок MM-87, не реализация корпоративной IAM.
Issuer и callback жёстко ограничены `oidc.localhost:18445` и
`staff.localhost:18444`; внешний redirect и произвольные клиенты не принимаются.
Две тестовые личности и случайные пароли создаёт `infra/dev/staff.py`.
Ключ подписи генерируется при запуске; запросы и authorization codes живут только
в памяти, ограничены TTL и общим лимитом. Коды одноразовые, требуется Basic client
auth и PKCE. Browser binding и точный Origin защищают отправку формы.

CSP разрешает форму и только фиксированный callback (включая redirect chain).
Referrer-Policy `strict-origin` не передаёт path/query и сохраняет Origin POST-формы;
`no-referrer` превращает его в `null` по Fetch Standard и ломает проверку.

Запускается общим Compose через `task dev:up`; команды и учётные данные описаны
в [портале сотрудников](../../../frontend/apps/staff/README.md). В production
должен использоваться корпоративный IdP, а не этот исполняемый файл.
