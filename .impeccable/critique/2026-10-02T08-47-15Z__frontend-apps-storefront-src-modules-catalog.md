---
target: frontend/apps/storefront/src/modules/catalog
total_score: 27
max_score: 40
na_heuristics: 
p0_count: 0
p1_count: 3
target_identity: "file:frontend/apps/storefront/src/modules/catalog"
timestamp: 2026-10-02T08-47-15Z
slug: frontend-apps-storefront-src-modules-catalog
---
Method: dual-agent (A: design review · B: detector + browser). Build 4bbde39 (MM-126 tag + MM-127 feedback + MM-128 phone).

## Design Health Score — 27/40 (Acceptable; 23 → 26 → 27)
| # | Heuristic | Score | Key Issue |
|---|---|---|---|
| 1 | Visibility of System Status | 3 | inline card confirmation + live region; muted cart counter, scrolls away on phone |
| 2 | Match System / Real World | 3 | «Вся партия: 7» ambiguous; «Каталог» anchor |
| 3 | User Control and Freedom | 3 | category/sort not in URL |
| 4 | Consistency and Standards | 2 | 5 equal outlined controls; sold-out card breaks bottom line; date wording varies |
| 5 | Error Prevention | 3 | filled «Оформить заказ» leads to dead end |
| 6 | Recognition Rather Than Recall | 3 | search silently scoped to category; stock lost in cart |
| 7 | Flexibility and Efficiency | 2 | no quantity, no «ending soon», no URL state |
| 8 | Aesthetic and Minimalist Design | 3 | 12 identical buttons, 5 equal header controls |
| 9 | Error Recovery | 3 | false advice for search+category |
| 10 | Help and Documentation | 2 | batch/meter never explained |

## Design Specificity
Product expressed in words, not form. New: meter visual weight inverted — full graphite bars (whole/18 of 20) darkest, low-stock ochre bar shorter/lighter. Detector CLI: 0 (catalog + App.vue); style.css overused-font (FP). Browser headless: undersized-ui-text ×2 (footer eyebrow 10.24px, real), overused-font, cream-palette (FP), edge-flush-cards at 390 (FP, intentional scroll row). No overflow 320/390/1280 both themes; text contrast only brand-dot 2.23 (decorative); dark min 7.27. Meter: light graphite 15.71/11.93, ochre 4.35/3.30; dark 13.84/8.18, 8.93/5.28. Tap targets <24: none; <44: 5 links. First card y=480 at 390×844; card grows 418→513 after add.

## Priority Issues
- [P1] Meter weight inverted → thin stone meter (or none for full batch), strong ochre only for ending; «Партия из 7, вся в наличии». Cmd: colorize → bolder
- [P1] Card confirmation shifts grid, repeats browser note → state in the button, note only in cart, counter feedback. Cmd: polish
- [P1] Phone: no cart access after scroll → compact sticky header or bottom bar when cart non-empty; sort after chips. Cmd: adapt
- [P2] Search silently combined with category → reset or explicit scope, URL state. Cmd: clarify
- [P2] Cart drops batch story, dead-end checkout → stock + reservation line in cart, name unavailability before click, remove «Каталог» anchor. Cmd: clarify / distill

## Persona Red Flags
Casey: 3-line tag in narrow column, «140 × 220» wraps, cart off-screen, sort before chips, 160px photo. Riley: silent cart pruning, no quantity, filter lost on reload, false empty search, price/stock change unhandled. Jordan: «Вся партия: 7», meter meaning, dead «Каталог», «Мои заказы» → login. Sam: good live region/focus; 24 tab stops, «Порядок», modal replaces modal.

## Minor Observations
Sold-out bottom line; invisible «0»; footer eyebrow 10.24px; ФОТО wall; sort block floating at 1440; «Витрина» footer label.

## Questions
Ledger instead of grid? Tag over real photos? Say «не отложено» on the card?
