---
target: frontend/apps/storefront/src/modules/catalog
total_score: 26
max_score: 40
na_heuristics: 
p0_count: 0
p1_count: 2
target_identity: "file:/Users/vh/projects/marketmesh/frontend/apps/storefront/src/modules/catalog"
timestamp: 2026-10-05T08-28-27Z
slug: frontend-apps-storefront-src-modules-catalog
---
Method: dual-agent (A: design review + marketplace research · B: detector + browser). dev 515fcb1. Focus: card slot drift from wraps (owner complaint).

## Design Health Score — 26/40
1 Visibility 3 (date meaning unclear) · 2 Real world 3 («Партия из 7, вся в наличии» heavy) · 3 Control 3 (no product link) · 4 Consistency 2 (slots misaligned in row, photo ratio changes) · 5 Error prevention 3 · 6 Recognition 3 (prices re-searched per card) · 7 Flexibility 2 (row not scannable) · 8 Aesthetic 2 (ragged rows, 8× «Мастерская», wrap inside date) · 9 Recovery 3 · 10 Help 2 (batch unexplained).

## Diagnosis (measured)
Card is flex column; only the button aligns. Drift from 3 variable slots: tag (stock + date glued with «·», 1–3 lines), title (1–4 lines, units split «300 / мл»), shop (wraps on narrow). Worst row spread: 1440/1280 21.7px (meter/price/title/shop), 721 21.7, 1100 shop 21.7, 320 shop 43.4. Sold-out action wraps (+14px) on 1100/phone. Photo fixed height → ratio changes. Slack piles above the button (up to 51px). Detector: CLI 0; browser undersized-ui-text ×2 (footer eyebrow, real), overused-font, cream-palette, edge-flush-cards (FP).

## Marketplace research
Baymard: same attributes in same places (64% violate); titles 2–3 lines not single-line ellipsis; CSS subgrid aligns slots (Baseline 2023); Shopify Dawn same issue, line-clamp alone insufficient; equal image ratio; Etsy «Only X left» separate line; Ozon «Осталось N шт» on tile (placement unverified); WB title ≤60 chars at input. Live marketplace pages not fetchable this session.

## Priority Issues
- [P1] Slots unlinked across row → subgrid (grid-row: span 8; grid-template-rows: subgrid). Cmd: layout
- [P1] Tag = one long phrase of two facts → split stock (never truncated, one line) + date (caption); shorten full-batch copy. Cmd: clarify + typeset
- [P2] Fixed photo height → aspect-ratio. Cmd: adapt
- [P2] Shop wraps, repeats «Мастерская» → name only, single-line ellipsis. Cmd: distill
- [P3] Title clamp 2 (phone 3), text-wrap pretty, nbsp for units; sold-out action one line. Cmd: typeset

## Recommended structure
Photo(aspect-ratio) → Stock(1 line, never clamp) → Meter → Date(caption) → Price → Title(≤2/3) → Shop(1 line …) → Action; all direct children, subgrid rows.

## Persona Red Flags
Casey: 3-line tag, prices at different heights, «300 / мл». Jordan: date meaning, heavy full-batch copy, no product page. Sam: clamped title at 200% without product page; reading order starts with favorite.

## Questions
Date per tile vs grouping by day? Limit title length at input? Stock louder than price / on photo?
