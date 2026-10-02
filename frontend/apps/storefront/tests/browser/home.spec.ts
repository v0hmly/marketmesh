import AxeBuilder from '@axe-core/playwright';
import { expect, test, type BrowserContext, type Route } from '@playwright/test';

// Витрина пока на образцовых данных (MM-125): API нужен только для гостевой сессии.
async function guestApi(context: BrowserContext) {
  async function unauthenticated(route: Route) {
    await route.fulfill({
      status: 401,
      headers: { 'content-type': 'application/json', 'cache-control': 'no-store' },
      body: JSON.stringify({ code: 'unauthenticated', message: 'Request failed' }),
    });
  }
  await context.route('**/auth.v1.*/**', unauthenticated);
  await context.route('**/user.v1.UserService/**', unauthenticated);
}

test('guest home lists new batches, filters, keeps the cart and stays accessible', async ({
  page,
  context,
}) => {
  await guestApi(context);
  await page.goto('/');
  await expect(page.getByRole('heading', { name: 'Новые партии от мастеров.' })).toBeVisible();
  const tiles = page.getByRole('list', { name: 'Партии' }).getByRole('listitem');
  await expect(tiles).toHaveCount(8);
  await expect(page.getByText('В партии 8 из 10')).toBeVisible();
  // Бирка партии: полная партия и малый остаток называются по-разному.
  await expect(page.getByText('Вся партия: 7')).toBeVisible();
  await expect(page.getByText('Осталось 2 из 6 · партия от 29 сентября')).toBeVisible();
  // Шкала графитовая по умолчанию и охряная только у заканчивающейся партии.
  const meterColor = (text: RegExp) =>
    tiles
      .filter({ hasText: text })
      .locator('.storefront-tag-meter > span')
      .evaluate((node) => getComputedStyle(node).backgroundColor);
  const ink = await page.evaluate(() =>
    getComputedStyle(document.documentElement).getPropertyValue('--text-primary').trim(),
  );
  const lowStock = await page.evaluate(() =>
    getComputedStyle(document.documentElement).getPropertyValue('--status-low-stock').trim(),
  );
  const rgb = (hex: string) =>
    `rgb(${[1, 3, 5].map((i) => parseInt(hex.slice(i, i + 2), 16)).join(', ')})`;
  expect(await meterColor(/В партии 8 из 10/)).toBe(rgb(ink));
  expect(await meterColor(/Осталось 2 из 6/)).toBe(rgb(lowStock));
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);

  await page.getByRole('button', { name: 'Показать ещё партии' }).click();
  // Фокус не теряется: он переходит к первой показанной карточке.
  await expect(tiles.nth(8)).toBeFocused();
  await expect(tiles).toHaveCount(12);
  await page.getByRole('button', { name: 'Керамика' }).click();
  await expect(page.getByRole('button', { name: 'Керамика' })).toHaveAttribute(
    'aria-pressed',
    'true',
  );
  await expect(tiles).toHaveCount(3);
  await expect(page.getByText('Партия распродана')).toBeVisible();

  await page.getByRole('button', { name: /^В корзину\s*: Кружка «Пена»/ }).click();
  // Подтверждение видно в самой карточке и звучит в live-регионе; кавычки внутри названия — „“.
  const mug = tiles.filter({ hasText: 'Кружка «Пена», 300 мл' });
  await expect(mug.locator('.storefront-card-status')).toHaveText(
    'Добавлено в корзину. Пока вы не вошли, корзина хранится в этом браузере.',
  );
  await expect(page.getByRole('status')).toContainText('«Кружка „Пена“, 300 мл» в корзине.');
  // Повторное нажатие открывает корзину, а не повторяет «уже в корзине».
  await mug.getByRole('button', { name: /^Открыть корзину/ }).click();
  await expect(page.getByRole('dialog', { name: 'Корзина' })).toBeVisible();
  await page.getByRole('button', { name: 'Продолжить покупки' }).click();
  // Смена категории сбрасывает подтверждение в карточке.
  await page.getByRole('button', { name: 'Одежда' }).click();
  await page.getByRole('button', { name: 'Керамика' }).click();
  await expect(mug.locator('.storefront-card-status')).toHaveCount(0);
  await page.getByRole('button', { name: /^Корзина/ }).click();
  const cart = page.getByRole('dialog', { name: 'Корзина' });
  await expect(cart.getByText('2 400 ₽').first()).toBeVisible();
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
  await cart.getByRole('button', { name: 'Оформить заказ' }).click();
  const signIn = page.getByRole('dialog', { name: 'Вход в MarketMesh ID' });
  await expect(signIn).toContainText('Войдите, чтобы оформить заказ.');
  await expect(signIn.getByRole('button', { name: 'Не сейчас' })).toBeFocused();
  await signIn.getByRole('button', { name: 'Не сейчас' }).click();
  await expect(signIn).toBeHidden();
  // Окно входа открыто из корзины: фокус возвращается к кнопке корзины в шапке.
  await expect(page.getByRole('button', { name: /^Корзина/ })).toBeFocused();

  await page.reload();
  await expect(page.getByRole('button', { name: /^Корзина/ })).toContainText('1');

  await page
    .getByRole('button', { name: /^Сообщить о пополнении\s*: Керамическая тарелка/ })
    .click();
  await expect(signIn).toBeVisible();
  await signIn.getByRole('button', { name: 'Войти' }).click();
  await expect(page).toHaveURL(/\/login$/);
});

test('search narrows the feed and the phone layout does not scroll sideways', async ({
  page,
  context,
}) => {
  await guestApi(context);
  await page.goto('/');
  await page.getByLabel('Поиск по изделиям и мастерским').fill('глина');
  await page.getByRole('button', { name: 'Найти' }).click();
  await expect(page.getByRole('heading', { name: 'Поиск: «глина»' })).toBeVisible();
  await expect(page.getByRole('list', { name: 'Партии' }).getByRole('listitem')).toHaveCount(3);
  await page.getByLabel('Поиск по изделиям и мастерским').fill('янтарь');
  await page.getByRole('button', { name: 'Найти' }).click();
  await expect(page.getByRole('heading', { name: 'Ничего не нашли' })).toBeVisible();
  await expect(page.getByRole('heading', { name: 'Поиск: «янтарь»' })).toBeVisible();
  await expect(page.getByText('Найдено: 0.')).toBeVisible();
  await page.getByRole('button', { name: 'Сбросить поиск' }).click();
  await expect(page.getByRole('heading', { name: /^Поиск:/ })).toBeHidden();
  await expect(page.getByRole('list', { name: 'Партии' }).getByRole('listitem')).toHaveCount(8);
  await page.setViewportSize({ width: 390, height: 844 });
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(
    true,
  );
  // Телефон: категории — одна строка, первая карточка начинается в первом экране.
  const chipTops = await page
    .getByRole('group', { name: 'Категория' })
    .getByRole('button')
    .evaluateAll((nodes) => nodes.map((node) => Math.round(node.getBoundingClientRect().top)));
  expect(new Set(chipTops).size).toBe(1);
  await page.evaluate(() => window.scrollTo(0, 0));
  const firstCard = await page
    .getByRole('list', { name: 'Партии' })
    .getByRole('listitem')
    .first()
    .boundingBox();
  expect(firstCard!.y).toBeLessThan(600);
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
});
