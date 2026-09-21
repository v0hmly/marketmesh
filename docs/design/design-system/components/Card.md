One shell, `.card` — hairline `border-subtle`, `r-lg` corners, `surface-raised` fill and the single `elevation-card` shadow — with three fills on top of it. All three now ship: identity, auth/profile, and state.

`.identity-card` tints to `surface-tint`, caps at 300px, and holds exactly an `.initials` avatar (two letters, `aria-hidden`, serif), an `h2` name, one line of body copy, and a `.privacy-note` pinned under a top border. The `↳` glyph and the wording "доступны только вам" are reserved for that line — don't reuse either elsewhere.

`.auth-card` is the plain fill holding a form: a heading, fields, and one `.wide` primary button at the bottom. It caps at 410px, which is the width of the auth column in the page grid.

`.state-card` centres everything and holds a single message: an optional status mark (a `.loading-dot`), a short `h2`, one line of copy capped near 44 characters, and at most one button. It's for whole-screen or whole-section states — preparing a profile, signed out, unavailable — never for inline errors, which use `Feedback`.

Across breakpoints: below 720px the identity card turns horizontal, `55px 1fr` with the avatar shrinking to 55px beside the name, and drops its max-width; card padding steps down from 34–38px on desktop to 26–28px at tablet and 22–27px on phones. Above 1400px the identity column widens from 280px to 295px. The auth and state cards keep their shape and only lose padding.

Separation between a card and the page is deliberately near-threshold — a 1.22:1 hairline over a near-invisible shadow. That's the house look, but it means a card boundary can never be the only thing communicating structure: give the card a heading or a label as well.
