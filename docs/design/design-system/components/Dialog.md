A confirmation for an action that can't be undone — deleting an address, discarding a draft. It is the one place this system interrupts the flow, and it exists only because the alternatives are worse: an inline `Notice` can't stop a destructive action, and toasts are ruled out everywhere.

The dialog is the `.card` shell at `r-lg` over a `surface-scrim`, holding a question as its heading, one sentence saying what will and won't happen, and two actions right-aligned: the cancel as a text button first, the confirming action as `.primary` last. Never more than two. Never a third "don't ask again".

Write the heading as the question in the user's own terms, naming the thing ("Удалить адрес «Мастерская»?"), and use the body line to bound the consequence — what survives the action matters as much as what disappears. The confirming button repeats the verb ("Удалить адрес"), never "OK" or "Да". Keep to the house voice: no apology, no exclamation.

The consumer provides everything the browser doesn't: `role="dialog"`, `aria-modal="true"`, `aria-labelledby` pointing at the heading, focus moved into the dialog on open and returned to the trigger on close, focus trapped while open, Escape closing it as cancel, and the page behind it locked from scrolling. The scrim is not a close button by default — for a destructive confirm, make clicking outside do nothing.

This is an intentional addition, not an extraction: the source has no dialog. Validate the interaction against the product before treating its copy patterns as settled.
