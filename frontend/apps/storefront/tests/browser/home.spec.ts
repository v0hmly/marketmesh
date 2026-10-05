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
  await expect(page.getByText('В партии 7 из 7', { exact: true })).toBeVisible();
  await expect(page.getByText('Осталось 2 из 6', { exact: true })).toBeVisible();
  // Слоты каждой карточки стоят на одной горизонтали со слотами соседей по ряду (MM-133).
  const slotsAligned = (slots: string[]) =>
    page.evaluate((selectors) => {
      const rows = new Map<number, Element[]>();
      for (const tile of document.querySelectorAll('.storefront-tile')) {
        const top = Math.round(tile.getBoundingClientRect().top);
        rows.set(top, [...(rows.get(top) ?? []), tile]);
      }
      return [...rows.values()].every((tiles) =>
        selectors.every(
          (slot) =>
            new Set(
              tiles.map((tile) =>
                Math.round(tile.querySelector(slot)!.getBoundingClientRect().top),
              ),
            ).size === 1,
        ),
      );
    }, slots);
  const allSlots = [
    '.storefront-stock',
    '.storefront-batch-date',
    '.storefront-price',
    '.storefront-title',
    '.storefront-shop',
    '.storefront-tile > .button',
  ];
  expect(await slotsAligned(allSlots)).toBe(true);
  // Масса шкалы следует смыслу: тонкая каменная у обычной партии, охряная и толще у
  // заканчивающейся, у полной партии шкала скрыта (место остаётся, чтобы ряд не скакал).
  const meterColor = (text: RegExp) =>
    tiles
      .filter({ hasText: text })
      .locator('.storefront-tag-meter > span')
      .evaluate((node) => getComputedStyle(node).backgroundColor);
  const stone = await page.evaluate(() =>
    getComputedStyle(document.documentElement).getPropertyValue('--text-secondary').trim(),
  );
  const lowStock = await page.evaluate(() =>
    getComputedStyle(document.documentElement).getPropertyValue('--status-low-stock').trim(),
  );
  const rgb = (hex: string) =>
    `rgb(${[1, 3, 5].map((i) => parseInt(hex.slice(i, i + 2), 16)).join(', ')})`;
  expect(await meterColor(/В партии 8 из 10/)).toBe(rgb(stone));
  expect(await meterColor(/Осталось 2 из 6/)).toBe(rgb(lowStock));
  const meterHeight = (text: RegExp) =>
    tiles
      .filter({ hasText: text })
      .locator('.storefront-tag-meter')
      .evaluate((node) => node.getBoundingClientRect().height);
  expect(await meterHeight(/В партии 8 из 10/)).toBe(2);
  expect(await meterHeight(/Осталось 2 из 6/)).toBe(4);
  await expect(
    tiles.filter({ hasText: 'В партии 7 из 7' }).first().locator('.storefront-tag-meter'),
  ).toBeHidden();
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

  const mug = tiles.filter({ hasText: 'Кружка «Пена», 300 мл' });
  const neighbour = tiles.filter({ hasText: 'Миска «Туман», 600 мл' }).getByRole('button', {
    name: /^В корзину/,
  });
  const pageTop = () =>
    neighbour.evaluate((node) => node.getBoundingClientRect().top + window.scrollY);
  const before = await pageTop();
  await page.getByRole('button', { name: /^В корзину\s*: Кружка «Пена»/ }).click();
  // Подтверждение — в самой кнопке и в live-регионе; кавычки внутри названия — „“.
  await expect(mug.getByRole('button', { name: /^В корзине\s*, открыть корзину/ })).toBeVisible();
  await expect(page.getByRole('status')).toContainText('«Кружка „Пена“, 300 мл» в корзине.');
  await expect(page.getByRole('button', { name: 'Корзина: 1 изделие' })).toBeVisible();
  // Сетку ничто не раздвигает: кнопка соседней карточки на месте.
  expect(await pageTop()).toBe(before);
  // Повторное нажатие открывает корзину, а не повторяет «уже в корзине».
  await mug.getByRole('button', { name: /^В корзине\s*, открыть корзину/ }).click();
  await expect(page.getByRole('dialog', { name: 'Корзина' })).toBeVisible();
  await page.getByRole('button', { name: 'Продолжить покупки' }).click();
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
  // Карточка и заметная часть фото (160px) — в первом экране.
  expect(firstCard!.y + 160).toBeLessThan(844);
  const chips = page.getByRole('group', { name: 'Категория' });
  const row = await chips.evaluate((node) => {
    const style = getComputedStyle(node);
    return {
      scrolls: node.scrollWidth > node.clientWidth,
      paddingTop: parseFloat(style.paddingTop),
      paddingBottom: parseFloat(style.paddingBottom),
    };
  });
  expect(row.scrolls).toBe(true);
  // Кольцо фокуса выходит за кнопку на 6px: ряд не должен его обрезать.
  expect(row.paddingTop).toBeGreaterThanOrEqual(6);
  expect(row.paddingBottom).toBeGreaterThanOrEqual(6);
  // Кнопка в фокусе прокручивается в ряд целиком.
  const last = chips.getByRole('button', { name: 'Бумага' });
  await last.focus();
  const [lastBox, rowBox] = await Promise.all([last.boundingBox(), chips.boundingBox()]);
  expect(lastBox!.x + lastBox!.width).toBeLessThanOrEqual(rowBox!.x + rowBox!.width);
  // Сортировка идёт после категорий — и на экране, и в порядке фокуса.
  const sortTop = await page
    .getByLabel('Порядок')
    .evaluate((node) => node.getBoundingClientRect().top);
  const chipsTop = await chips.evaluate((node) => node.getBoundingClientRect().top);
  expect(sortTop).toBeGreaterThan(chipsTop);
  // Плашки корзины нет, пока корзина пуста.
  const bar = page.locator('.storefront-cart-bar');
  await expect(bar).toHaveCount(0);
  // Подтверждение в кнопке не сдвигает сетку и в узкой плитке телефона.
  const second = page
    .getByRole('list', { name: 'Партии' })
    .getByRole('listitem')
    .nth(1)
    .getByRole('button', { name: /^В корзину/ });
  const secondTop = () =>
    second.evaluate((node) => node.getBoundingClientRect().top + window.scrollY);
  const firstAction = page
    .getByRole('list', { name: 'Партии' })
    .getByRole('listitem')
    .first()
    .getByRole('button', { name: /^В корзину/ });
  const beforeTop = await secondTop();
  const beforeHeight = await firstAction.evaluate((node) => node.getBoundingClientRect().height);
  await firstAction.click();
  const inCart = page
    .getByRole('list', { name: 'Партии' })
    .getByRole('listitem')
    .first()
    .getByRole('button', { name: /^В корзине/ });
  expect(await inCart.evaluate((node) => node.getBoundingClientRect().height)).toBe(beforeHeight);
  expect(await secondTop()).toBe(beforeTop);
  // Плашка корзины появляется с первым изделием и остаётся под рукой при прокрутке.
  await page.evaluate(() => window.scrollTo(0, 1200));
  await expect(bar).toContainText('В корзине 1 изделие');
  const barBox = await bar.boundingBox();
  expect(barBox!.y + barBox!.height).toBeLessThanOrEqual(844);
  // Плашка не закрывает элемент в фокусе (WCAG 2.4.11): обходим ленту с клавиатуры.
  await page.evaluate(() => window.scrollTo(0, 0));
  await inCart.focus();
  for (let step = 0; step < 12; step += 1) {
    await page.keyboard.press('Tab');
    const [focused, barTop] = await page.evaluate(() => [
      document.activeElement!.getBoundingClientRect().bottom,
      document.querySelector('.storefront-cart-bar')!.getBoundingClientRect().top,
    ]);
    expect(focused).toBeLessThanOrEqual(barTop);
  }
  await bar.getByRole('button', { name: 'Открыть корзину' }).click();
  const cartDialog = page.getByRole('dialog', { name: 'Корзина' });
  await expect(cartDialog).toBeVisible();
  // Опустошённая из плашки корзина: плашка уходит, фокус возвращается в ленту, а не на body.
  await cartDialog.getByRole('button', { name: /^Убрать/ }).click();
  await page.getByRole('button', { name: 'Продолжить покупки' }).click();
  await expect(bar).toHaveCount(0);
  await expect(page.locator('#catalog')).toBeFocused();
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
  await page.setViewportSize({ width: 320, height: 640 });
  // На узком телефоне слоты по-прежнему выровнены по ряду.
  expect(
    await page.evaluate(() => {
      const rows = new Map<number, Element[]>();
      for (const tile of document.querySelectorAll('.storefront-tile')) {
        const top = Math.round(tile.getBoundingClientRect().top);
        rows.set(top, [...(rows.get(top) ?? []), tile]);
      }
      return [...rows.values()].every((tiles) =>
        ['.storefront-price', '.storefront-shop', '.storefront-tile > .button'].every(
          (slot) =>
            new Set(
              tiles.map((tile) =>
                Math.round(tile.querySelector(slot)!.getBoundingClientRect().top),
              ),
            ).size === 1,
        ),
      );
    }),
  ).toBe(true);
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(
    true,
  );
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
});
