Four small identity pieces that share this system's restraint: no filled colour pills beyond what's here, no decorative icon set.

`.draft-badge` is a pill for one meaning — unsaved changes exist ("Есть изменения") — on `surface-badge` with a `border-badge` edge. It also marks a default item in a list (see `AddressRow`). Don't stretch it into a generic status tag; other states belong to `Feedback`.

`.brand` is the wordmark: a circular `.brand-mark` (a lowercase "m" set tight in Georgia on `action-primary`) followed by "marketmesh" with a `.brand-dot` full stop in `brand-accent`. There is no logo file — the mark is drawn from a letterform in CSS, so reproduce it as CSS or text, never as a rasterised image. In dark mode `brand-accent` is now a deeper olive than the primary green, so the dot stays visible as an accent instead of merging into the wordmark's surroundings.

`.skip-link` is the first focusable element in the DOM on every page, a small filled control in `action-primary` at `r-xs`. It moves focus to `main`; `main:focus` is the one place in this system that intentionally has no focus ring, because the skip link's own ring already did that work.

`.theme-choice` is a radio row for the three theme options (system, light, dark). Checked tints the row to `surface-tint` with a `text-link` border — border plus fill, never fill alone. The 20px radio sits inside an 18px-padded label, well past the 42px minimum target this system holds every interactive element to; the two-tone focus ring lands on the radio itself.

None of these change across breakpoints, but the masthead they sit in does: it drops from 116px to 87px below 720px and hides its caption, so don't attach meaning to anything that lives beside the wordmark up there.
