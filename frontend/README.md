# Frontend

Frontend строится на Vue и pnpm workspace. До появления независимых продуктовых команд используется одно модульное приложение, описанное в [ADR-0010](../docs/adr/0010-microfrontend-composition-and-deployment.md).

- `apps/storefront` — приложение онлайн-магазина;
- `packages` — только действительно общие frontend-пакеты;
- `../api/gen/ts` — сгенерированные API-контракты.

Приложение и пакеты будут созданы отдельными задачами; пустые workspace-пакеты не добавляются.
