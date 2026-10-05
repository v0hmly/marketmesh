---
target: frontend/apps/storefront/src/modules/catalog
total_score: 26
max_score: 40
na_heuristics: 
p0_count: 0
p1_count: 3
target_identity: "file:/Users/vh/projects/marketmesh/frontend/apps/storefront/src/modules/catalog"
timestamp: 2026-10-01T13-56-21Z
slug: frontend-apps-storefront-src-modules-catalog
---
Method: dual-agent (A: design review · B: detector + browser). Build 27b096c (MM-126 batch tag).

## Design Health Score — 26/40 (Acceptable, was 23)
| # | Heuristic | Score | Key Issue |
|---|---|---|---|
| 1 | Visibility of System Status | 2 | add-to-cart notice above h1, off-screen; header not sticky |
| 2 | Match System / Real World | 3 | meter ambiguous: full bar on «Вся партия» reads as sales progress |
| 3 | User Control and Freedom | 3 | restock subscription cannot be undone |
| 4 | Consistency and Standards | 2 | «Каталог» anchor as button; cart on ConfirmDialog; 3 action styles in header |
| 5 | Error Prevention | 3 | «Мои заказы» silently sends guest to login |
| 6 | Recognition Rather Than Recall | 2 | search silently scoped to category |
| 7 | Flexibility and Efficiency | 2 | no path to product or shop from card |
| 8 | Aesthetic and Minimalist Design | 3 | 8–12 ochre meters turn the tag into a pattern |
| 9 | Error Recovery | 3 | empty search has no action |
| 10 | Help and Documentation | 3 | restock mechanics only explained in sign-in dialog |

## Design Specificity
Half product-authored (Georgia h1, lede promising stock, 4-state tag copy), half marketplace template. New risk: ochre 4px meter reads as Ozon/Ali scarcity bar, not a workshop tag; maker is the smallest text. Detector CLI: 0 findings (catalog + App.vue); style.css: overused-font (FP). Browser headless: undersized-ui-text ×2 (footer .eyebrow 10.24px, unchanged), overused-font (FP), cream-palette (FP). No overflow 390/1280; only contrast fail brand-dot 2.23:1 (decorative); footer links 16px tall (~34px pitch); first card top y=655 at 390×844.

## Priority Issues
- [P1] Add-to-cart confirmation off-screen → status line inside card, «Открыть корзину», or sticky compact header with counter. Cmd: harden
- [P1] Tag reads as scarcity meter; maker demoted → tag as written record (stock + batch date), meter graphite or removed, ochre only for low stock, maker 0.875rem link. Cmd: bolder → colorize
- [P1] Phone: feed starts at bottom of first screen → single scrollable chip row, compact sort, 2 columns ≤720px, shorter lede. Cmd: adapt
- [P2] Cart on ConfirmDialog loses stock/qty/maker; checkout-unavailable outside cart → dedicated cart panel. Cmd: shape
- [P2] IA dead ends: card not openable, «Каталог» anchor, «Мои заказы» → login, support form on home. Cmd: distill

## Persona Red Flags
Casey: chips eat first screen, no sticky cart, heart near scroll. Riley: silent category+search, stale notice on filter change, repeated actions no-op, silent cart pruning, mid-word wraps. Jordan: meter meaning unclear, «Вся партия: 7» odd, no product page. Sam: 0.73rem maker, 0.64rem eyebrow, «Корзина 0» name, notice far from focus.

## Minor Observations
~80px gap header→h1; title weaker than tag and price; sold-out photo barely distinct; no dates for «новые»; small «Сбросить поиск» target; ochre meters louder in dark; brand line hidden in footer.

## Questions
Why a meter, not a written tag? Feed or catalog? What happens when a batch sells out while in cart?
