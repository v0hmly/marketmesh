# MarketMesh — как строить интерфейс

У этой дизайн-системы нет React-компонентов: продукт написан на Vue, а язык системы —
**CSS-классы и токены** из `styles.css`. `window.MarketMesh` пуст. Пишите обычную
разметку (`<button>`, `<input>`, `<section>`) и назначайте ей классы ниже. Не придумывайте
свои классы для того, что уже есть, и не пишите hex-цвета или пиксели вместо токенов.

## Что прочитать перед работой

- `styles.css` → `_ds_bundle.css`: все классы и токены (светлая и тёмная темы).
- `guidelines/README.md`: тон текста, цвет, типографика, фокус, состояния.
- `guidelines/Button.md`, `Card.md`, `Field.md`, `Feedback.md`, `Navigation.md`,
  `Dialog.md`, `Badges.md`, `Footer.md`, `AddressRow.md`: правила по каждому компоненту.
  Если класс упомянут в guideline, но отсутствует в `_ds_bundle.css`, не используйте его.

## Настройка

- Тема: светлая по умолчанию, тёмная следует `prefers-color-scheme`. Явно тема задаётся
  через `<html data-theme="dark">` или `data-theme="light"`. Отдельной обёртки нет.
- Шрифты: `--font-sans` (Inter с системным fallback) и `--font-serif` (Georgia) только для одного
  заголовка `h1` на странице.

## Токены — только роли

`var(--surface-page|raised|tint|sunken)`, `var(--text-primary|secondary|disabled|link)`,
`var(--border-subtle|control|hover)`, `var(--action-primary)` + `var(--on-action-primary)`,
`var(--status-danger|pending)`, `var(--brand-accent)`. Отступы: `var(--sp-1)`…`var(--sp-10)`.
Радиусы: `var(--r-xs|sm|md|lg|pill|round)`. Тень одна: `var(--elevation-card)`.
Высоты: `var(--control-h)`, `var(--button-h)`. Палитру (`--moss-*`, `--sage-*`, `--clay-*`)
не используйте напрямую.

## Классы

| Задача | Классы |
|---|---|
| Каркас | `site-wrap`, `site-header`, `brand` (+ `brand-mark`, `brand-dot`), `header-nav`, `nav-link`, `site-footer`, `footer-meta`, `skip-link` |
| Заголовок страницы | `page-heading` > `eyebrow` + `h1` + `lede` |
| Карточки | `card`, `card-heading`, `card-footer`, `state-card`, `auth-layout`, `auth-card` |
| Поля | `field` > `label` + `input` + `field-help` / `field-error`; `label-line`, `field-count`, `select-wrap` |
| Кнопки | `button` + одно из `primary` / `secondary` / `text-button`; `wide`, `button-row`, `form-footer` |
| Состояния | `notice`, `notice error`, `notice success`, `loading-dot`, `subtle` |
| Диалог | `dialog-scrim` > `dialog` + `dialog-actions` |
| Служебное | `visually-hidden`, `theme-choice` |

Правила: на экране одна кнопка `primary`, и только она несёт `↗` (`aria-hidden="true"`).
Нельзя делать toast-уведомления и спиннеры: вместо спиннера disabled-кнопка с текстом «Сохраняем…».
Текст по-русски, на «вы», без восклицательных знаков. Статус всегда выражен словами, а не только цветом.

## Пример

```jsx
<main className="site-wrap">
  <div className="page-heading"><div>
    <span className="eyebrow">ЛИЧНЫЙ КАБИНЕТ</span>
    <h1>Рады вас видеть.</h1>
    <p className="lede">Здесь хранятся ваши заказы и адреса.</p>
  </div></div>
  <section className="card">
    <div className="card-heading"><h2>Личные данные</h2></div>
    <div className="field">
      <label htmlFor="name">Имя</label>
      <input id="name" />
      <p className="field-help">Так к вам обратится продавец.</p>
    </div>
    <div className="form-footer">
      <button className="button primary">Сохранить <span aria-hidden="true">↗</span></button>
    </div>
  </section>
</main>
```
