A quiet, paper-and-moss language for a buyer's account area, extracted from `frontend/apps/storefront`'s `style.css` and its Vue account views. Four things the code already believed, consistently, before any of it was written down: a warm paper ground over pixels, one forest green doing the work of brand, link, focus and primary action, privacy stated in the UI every time personal data appears, and states that are honest — pending, uncertain and reconcile are first-class, never disguised as success or failure.

## Content fundamentals

Write in Russian, formal-plural «вы», never an exclamation mark. A headline is a full sentence with a full stop — «Рады вас видеть.», «Начнём знакомство.» — first person plural, an invitation rather than a command; that full stop is what makes the serif `display` read as calm rather than loud. The one exception is the signed-in account area, where the `h1` is simply the section's name, as in its menu (see Navigation). An eyebrow is uppercase, middle-dot separated, and names the place, not the action ("ЛИЧНЫЙ КАБИНЕТ", not "НАСТРОЙТЕ ПРОФИЛЬ"). A lede is one sentence about the person, not the feature. An in-flight label is present tense with «мы» implied and an ellipsis — «Сохраняем…», «Проверяем…» — never a spinner icon. Help text is at most two fragments and states the constraint before the user can hit it ("Без пробелов. Регистр не важен."). An error names what happened, what was done about it, and what the user does next — never «извините». A destructive confirmation asks the question in the user's own words and repeats the verb on the button, never "OK". The privacy line — "Данные этого раздела доступны только вам." — is reserved for wherever personal data is shown, always with the `↳` glyph, and its wording doesn't vary.

## Visual foundations

**Colour.** Tokens come in two tiers. The `moss` / `olive` / `sage` / `clay` / `paper` ramps are the palette — the brand's tonal steps, referenced by name only when you're extending the system. Everything a component touches is a role: `surface-*` for grounds, `text-*` and `on-*` for what sits on them, `border-*`, `action-*`, `status-*`, `ring-*`, `brand-accent`. Build with the roles; reach into the ramp only to add one.

The palette is a green monochrome with a single warm exception. `text-primary`, `action-primary`, `text-link` and `ring-focus` are all steps of the same family, so brand, primary action and interactive text read as one decision rather than three. `status-danger` — a brick red warmed toward the paper — is the only hue outside it, reserved for validation and failed requests. `surface-reconcile` is deliberately neither danger nor success: a server/draft conflict is a decision, not a fault.

Light and dark are separate palettes, not an inversion: dark mode lifts `action-primary` to a pale sage so the primary button flips from dark-fill/light-text to light-fill/dark-text while keeping its role. Where the source's dark values collapsed two roles into one tone, they've been separated again — `brand-accent` is a deeper olive in dark mode so accent and action stay distinguishable.

Every text colour clears 4.5:1 on each ground its usage note names, in both themes; the tightest pair in the system is `text-secondary` on `surface-tint` at 4.87:1. Any mark that carries meaning clears 3:1 — control borders, the focus ring, the pending dot. Decorative hairlines (`border-subtle`, the notice and badge borders) deliberately do not, which is why nothing structural may rely on them alone.

**Type.** Two families: `serif` (Georgia) for the single `display` style, `sans` (Inter) for everything else; `mono` exists for code, not for product copy. The serif appears exactly once per page so that one moment reads as considered. Seven sizes now cover the product — `display`, `title`, `heading`, `subheading`, `body`/`lede`, `label`, `caption`/`micro` — with `title` filling the gap between 24.8px and the display clamp, where card and section headings used to improvise. Weights are 400, 500, 600 and 700 only: the source's 550 and 650 resolve only when variable Inter loads and silently round on the fallback stack, so a page would shift weight between font states.

**Spacing & layout.** A single centred column, `container` (1320px) wide, with `gutter` stepping 64 / 35 / 22px across the breakpoints. Two-column grids collapse to one at 720px. Use the `sp-*` scale for anything new rather than a one-off pixel value — the stylesheet it came from spends 33 different values doing what ten steps cover within 2px.

**Shape & elevation.** Radius grows with the object: `r-xs` on the skip link, `r-sm` on buttons, inputs and notices, `r-md` on panels and list rows, `r-lg` on cards, `r-round` on circles, `r-pill` on the badge. There is one shadow, `elevation-card`, and it is barely visible in light mode on purpose — borders, not shadows, is the house style.

**Focus.** One ring everywhere: 3px `ring-focus` outside, 3px `ring-focus-inner` hugging the element. The olive alone clears 3:1 by roughly a tenth of a point on light grounds, so the dark inner ring is what makes it dependable on any surface. Never remove either half; `main:focus` is the single exception, because the skip link's own ring already did that work.

**States.** Four feedback shapes cover every "something happened" — see `Feedback` — plus `Dialog` for an action that can't be undone. Never invent a fifth, and never a toast. Statuses always carry words, not just colour.

## Iconography

There is no icon system. The interface uses typographic glyphs, each `aria-hidden` and each reserved for one meaning: `↗` marks the primary action on a screen, `↳` prefixes the privacy line, `✳` and `…` are principle-card marks, and a `.` in `brand-accent` closes the wordmark. The select chevron is drawn in CSS, not an asset. There is no logo file — the brand mark is a lowercase "m" in Georgia inside a filled circle, drawn in CSS; reproduce it as CSS or text, never as an image.

## Adopting this system

Generate `tokens.css` from `tokens.json` and let that file be the only place a raw value appears; components reference custom properties and nothing else. Make it enforceable rather than aspirational — a stylelint rule is enough:

```json
{
  "rules": {
    "color-no-hex": true,
    "declaration-property-value-disallowed-list": {
      "/^(border-radius|min-height|gap)$/": ["/^[0-9]/"]
    }
  }
}
```

Run it on `src/**/*.css` in CI and fail the build, not the reviewer. Without a gate, the 33-pixel-value problem returns within a quarter — it is how the source got there in the first place. Component paddings carried over from the extraction (11/19 on buttons, 13/15 on controls, 28/26/22 on the identity card) are exact source values and stay as they are until a deliberate migration; the rule above targets the properties where drift actually happens.

Two things the tokens can't enforce, so check them by hand when adding a component: every text colour against the ground it actually lands on, in both themes, and every meaningful mark at 3:1.

## Known gaps

Three of the source's own audit findings have been resolved here and now differ from the live stylesheet: the resting control border was darkened (it read 1.76:1 and could not bound a control), the pending dot was darkened to clear 3:1 on its own, and the focus ring gained its inner half. Two token names the source separated are now one — the secondary button's border is `border-control`, the same value inputs use, because keeping them apart in light mode and identical in dark communicated nothing.

Still open: the three near-white surfaces (`surface-raised`, `on-action-primary`, and the input fill the source spelled `#fffefa`) remain distinct values within a hair of each other and are worth collapsing; the dark palette is still defined three times over in the live CSS rather than once and referenced; and `sage-300` and `sage-400` now sit on the ramp without a role in light mode, kept because they are the brand's lightest border tones. Whether the storefront actually loads variable Inter is unresolved — the source's audit says the font is system-only while its own document links Google Fonts, and the answer decides whether the weight cleanup above is a fix or a no-op.
