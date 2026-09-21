Three fills for one shape: `.button` plus `.primary`, `.secondary` or `.text-button`. Never combine two fill classes on one button.

Use `.primary` for the one action that moves the user forward on a screen (save, sign in, create). Use `.secondary` for a supporting action beside it (re-check, re-read, cancel). Use `.text-button` only for a low-stakes action such as sign-out — it renders as an underlined link, not a filled control.

The trailing `↗` belongs to `.primary` and nothing else. The older rule ("arrow when something is sent or opened") didn't discriminate — a secondary re-fetch also sends a request — so read it as a rank marker, not a transport marker: the primary action on the screen carries the arrow, secondary and text buttons never do. Mark it `aria-hidden="true"`.

Heights come from tokens: `button-h` (45px) by default, `button-h-compact` (42px) with `.compact`, which exists for session controls and nothing else. 42px is the floor for anything interactive in this system. Add `.wide` inside a narrow container — an auth card, a form footer — to fill the row and push a trailing icon to the far edge.

The secondary button's fill sits within 1.02:1 of the page ground on purpose, so its `border-control` boundary is the only thing that says "button" — that border must never be softened below the 3:1 it now holds. Hover darkens both fill and border together.

Disabled buttons drop to 55% opacity and switch their label to a present-tense progressive form ("Сохраняем…"), never a spinner or a separate loading component.

Focus is the system-wide two-tone ring: 3px `ring-focus` outside, 3px `ring-focus-inner` hugging the control. Both halves are required — the olive alone clears 3:1 by only a tenth of a point on light grounds. Never remove or restyle it.
