Every page opens with the same three lines: `.eyebrow` naming the area, the serif `h1` as a full sentence, then a `.lede`. A `.section-number` sits right-aligned against the heading inside `.page-heading`.

Two nav patterns sit below the heading, and they are not interchangeable. `.nav-link` is for top-level destinations that exclude each other (profile, sign-in, registration) — active state is a 1px underline in `text-link`. `.account-nav` is for sub-sections inside one signed-in area (about you, addresses, checkout) — active state is a heavier 2px underline. At most one of each per screen.

Both actives rely on an underline plus a colour change and nothing else, which is enough only because the underline is a shape, not just a hue — never swap it for a colour-only state.

Across breakpoints: `.section-number` is hidden below 720px, so nothing may live there that isn't stated elsewhere; the profile grid collapses from `280px minmax(0, 1fr)` to one column at the same width; sidebars narrow to 230px between 720 and 1000px; above 1400px the page gains top padding and the identity column widens to 295px.

Only one `h1` per page. Decorative glyphs (↗ ↳ ✳) are always `aria-hidden`, and the section number is presentational too.

The eyebrow now sets at `eyebrow` (0.73rem) rather than the source's 0.65rem — uppercase at that tracking was the smallest text on the page and the first thing to fail on a dim screen.
