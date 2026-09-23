# Frontend

Frontend строится на Vue и pnpm workspace. До появления независимых продуктовых команд используется одно модульное приложение, описанное в [ADR-0010](../docs/adr/0010-microfrontend-composition-and-deployment.md).

- `apps/storefront` — приложение онлайн-магазина с продуктовыми областями
  (покупатель `modules/account`, продавец `modules/seller`, сотрудник `modules/staff`);
  shell владеет запуском, композицией маршрутов, сессией и темой;
- `packages` — только действительно общие frontend-пакеты;
- `gen/` — сгенерированные API-контракты.

Рабочее приложение и команды проверки описаны в [storefront](apps/storefront/README.md).
Общие пакеты добавляются при реальном переиспользовании; пустые workspace-пакеты не создаются.

Согласованные профиль, адресная книга и тема, границы shell/модуля аккаунта описаны в [модели аккаунта](../docs/product/account.md). Текущая готовность и последующие работы перечислены в [аудите кабинета](../docs/product/account-remaining.md).

Установка из корня: `pnpm --dir frontend install --frozen-lockfile`.
В каталоге `frontend/` доступны обычные `pnpm build`, `pnpm test`, `pnpm lint`
и `pnpm typecheck`; корневые `task frontend:*` делегируют им работу.
