A field is always label, optional counter, control, help text, then error, in that DOM order — errors append below help text, they never replace it. Wire the control's `aria-describedby` to whichever of help, error and counter ids are present, and set `aria-invalid="true"` on the control itself when there's an error.

The consumer provides: the label, the control's value and change handler, an optional character counter (shown only where the source enforces a maximum), optional help text, and an optional error. Controls are `control-h` (48px) tall with 13/15px padding, whether `input`, `textarea` or `select`; a textarea additionally gets a 110px minimum height and vertical-only resize.

A `select` is the same shell: wrap it in `.select-wrap` and give it `.ctl`. The chevron is drawn in CSS by the wrapper's `::after`, so a select needs no image and no JavaScript; use the native control rather than building a listbox, and keep option text short enough not to truncate at 320px.

The border tells the whole interaction story: `border-control` at rest, `border-hover` under the pointer, `text-link` on focus, `status-danger` when invalid. These are four distinct values on purpose — don't collapse hover and focus. The resting border was darkened from the source's original tone so it clears the 3:1 required of a control boundary, which also means it now reads slightly heavier than the old screenshots.

A disabled control takes `surface-sunken` and `text-disabled` and drops its hover behaviour. Never disable a field without the reason being visible nearby — if a pending server state is what disabled it, say so in a notice or lede above the form.

Validate on submit, not on keystroke (`novalidate` on the form, `aria-busy` while submitting); an error appears only once the user has tried to move past it.

Fields don't change across breakpoints — they're full-width in their column at every size. What changes is the column: the profile grid is `280px minmax(0, 1fr)` on desktop and collapses to one column below 720px, and the auth column is a fixed 410px.
