A saved delivery address in the account area: a name for the address, the address itself, and its actions. Rows stack in an `.address-list` at a single column with `sp-5` between them, at every breakpoint — the source's address grid was already one column with a 20px gap, so this component only gives that layout a name and a shell.

The shell is `r-md` with a `border-subtle` hairline on `surface-raised` — a panel, not a card: addresses are a list of peers, and giving each one a card's `r-lg` and shadow would make a list of five look like five separate sections.

The consumer provides the label, the formatted address lines, and the action handlers. Mark the default address with a `.draft-badge` in the head row rather than a separate colour or an icon. Put the address in a real `<address>` element with `<br>` between lines — not a paragraph per line, which reads as separate items to a screen reader.

Actions are compact text buttons, always in the same order (edit, then delete); a delete opens the confirm `Dialog` rather than removing the row immediately. If the list is empty, don't show an empty row — use a `state-card` with the one action that fills it.

This is an intentional addition: the source's layout table specifies the address list's grid but never documented the row itself.
