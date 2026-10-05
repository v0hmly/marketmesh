---
target: frontend/apps/storefront/src/modules/catalog
total_score: 23
max_score: 40
na_heuristics: 
p0_count: 0
p1_count: 2
target_identity: "file:/Users/vh/projects/marketmesh/frontend/apps/storefront/src/modules/catalog"
timestamp: 2026-10-01T12-21-25Z
slug: frontend-apps-storefront-src-modules-catalog
---
Method: dual-agent (A: design review · B: detector + browser)

## Design Health Score — 23/40 (Acceptable)
| # | Heuristic | Score | Key Issue |
|---|---|---|---|
| 1 | Visibility of System Status | 2 | «В корзине» notice above the feed, off-screen after scroll, shifts layout |
| 2 | Match System / Real World | 3 | «В партии 7 из 7» odd; «Каталог» is an anchor |
| 3 | User Control and Freedom | 2 | no undo for cart removal, «Показать ещё» irreversible, notice not dismissable |
| 4 | Consistency and Standards | 3 | «Каталог» link styled as button; sold-out card uses text-button |
| 5 | Error Prevention | 2 | search silently scoped to active category → false «Ничего не нашли» |
| 6 | Recognition Rather Than Recall | 3 | active category not shown in search heading |
| 7 | Flexibility and Efficiency | 1 | single category, 3 sorts, «Показать ещё» without count |
| 8 | Aesthetic and Minimalist Design | 2 | 12 identical outlined buttons; stock signal drowned |
| 9 | Error Recovery | 3 | good 3-part load error; misleading search+category advice |
| 10 | Help and Documentation | 2 | «партия» never explained |

## Design Specificity
Disciplined but category-interchangeable. Batch stock (the positioning) is the smallest muted line; no ochre tag; Georgia h1 hidden; sold-out not distinct. Detector CLI: 0 findings on catalog + App.vue (vars hide values); design-system style.css: overused-font. Browser (headless injection, no user-visible overlay): undersized-ui-text ×2 (footer .eyebrow 10.24px, 0.64rem vs token 0.73rem), overused-font (FP, by design), cream-palette (FP, brand paper). No overflow at 390/1280; only contrast fail brand-dot 2.23:1 (decorative); footer links 16px tall.

## Priority Issues
- [P1] Batch stock hidden though it is the positioning → stock as tag under photo, tabular, ink, 600 when ≤2, ochre progress mark, muted sold-out card, «Вся партия: 7». Cmd: bolder (+colorize)
- [P1] Action feedback off-screen; dynamic live region → persistent role=status, confirm on card + open cart link, header counter, checkout-unavailable message inside cart dialog. Cmd: harden
- [P2] Phone: first card below first screen (3 rows of chips, full-width sort) → scrollable chip row, compact sort, shorter photo. Cmd: adapt
- [P2] Card dead end, «Каталог» goes nowhere, search inside category → title/shop links, real catalog or remove, reset/announce category on search. Cmd: layout
- [P2] Page does not explain itself; hidden h1; eyebrow 10.24px vs token → visible Georgia h1 + lede about batches, eyebrow 0.73rem. Cmd: typeset (+onboard)

## Persona Red Flags
Casey: chips 3 rows, off-screen confirmation, no nav on phone, 16px footer links. Riley: false search advice, focus lost after «Показать ещё», silent cart pruning, repeated status not announced. Jordan: «партия»/«7 из 7» unexplained, dead «Каталог», cards not openable. Sam: dynamic live region, 11.7px muted stock, selected chip by tint only, «Корзина 0» name.

## Minor Observations
Nested quotes; no dates for «последняя неделя»; no quantity/stock in cart; «Показать ещё» without count; duplicate empty-cart actions; weak product title; support form on shopping page; «Витрина» footer label.

## Questions
Why is price louder than stock? Why one feed only (no «Заканчиваются»)? Does the support form belong on the home page?
