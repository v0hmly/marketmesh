Four kinds of "something happened," each inline in the flow — never a toast — and each tied to an ARIA live role so the state is announced, not just painted.

`.notice.error` (`role="alert"`) and `.notice.success` (`role="status"`) share one shape and differ only in colour. Error copy names what failed, what it means, and what to do next — never an apology. Success copy is past tense, then the next step. Both wrap `overflow-wrap: anywhere` so a long message from a downstream system can't break the layout. Their borders are decorative: the fill and the text carry the meaning, so a notice never depends on its 1px edge being visible.

`.loading-dot` pairs a filled circle with a present-tense sentence ending in an ellipsis — "Проверяем сессию…" — for anything in flight, inside a container with `role="status"`. The same ellipsis convention carries into button labels; never add a spinner beside it. The dot's colour was darkened from the source so the mark itself clears 3:1 against both page and card grounds rather than relying on the sentence next to it — if you ever show the dot alone, it now holds up.

`.reconcile-panel` (`aria-labelledby` on its own heading) is for one situation: the server and a local draft disagree. It is deliberately neither error nor success — `surface-reconcile` is a muted olive-grey, because disagreeing isn't a fault — and it always offers exactly two ways forward, phrased as a choice: take the server's version, or keep the draft.

Statuses are never colour alone: every notice carries words, the reconcile panel carries a heading, and the pending dot carries its sentence. Keep that rule when adding a state — the palette's success and danger are a green and a red, and they are told apart by the copy, not the hue.

Map one event to exactly one of these four; never show two at once for the same thing. Nothing here changes across breakpoints — notices are full-width in their column at every size.
