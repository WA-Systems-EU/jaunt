# jaunt — Google Stitch design brief

> Paste this into Google Stitch to generate the marketing site + blog. It defines
> the product, the design system (colors, type, style), and one ready-to-use prompt
> per screen. Feed the **Design system** section first as the global theme, then
> generate each **Screen** with its prompt.

---

## 1. Product in one line

**jaunt — an ephemeral remote development environment you pop into and out of.**

A powerful remote dev box you spin up in the morning with one command and delete at
night. Your work stays on a persistent volume; the box is always reachable at a stable
Tailscale hostname; and when you stop working it **tears itself down** — because a
stopped cloud server still bills, only a deleted one stops the meter. Runs on EU
commodity cloud (Hetzner + UpCloud): cheap and sovereign by construction. It's an
open-source pattern, not a SaaS. Built by WA Systems (wa-systems.eu).

- **Audience:** professional software engineers / infra-minded developers. They live in
  a terminal, respect frugality, and distrust marketing fluff.
- **Tone:** confident, cost-honest, technical, calm. Show real commands. No hype words,
  no gradient-soup startup clichés, no stock photos of people.

## 2. Design concept — "Dawn to Dusk"

The product *is* a day cycle: **apply at dawn → work → idle → self-teardown at dusk.**
The whole visual language rides that arc. The signature accent is a **dawn-amber → dusk-indigo**
gradient, and the site ships **two paired themes** — a warm "Day" light mode and a deep
"Night" dark mode — with an obvious toggle (default to Night, developers' habitat).
Restraint over decoration: lots of quiet space, one strong accent, monospace where the
product speaks in commands.

## 3. Design system (feed this to Stitch as the global theme)

### Color — Night (default / dark)
| Token | Hex | Use |
|---|---|---|
| bg | `#0C0F17` | page background (deep near-black indigo) |
| surface | `#141924` | cards, terminal window, code blocks |
| border | `#232A38` | hairline dividers, card outlines |
| text | `#EDEFF4` | primary text |
| text-muted | `#8B93A5` | secondary text, captions |
| **amber (primary)** | `#F5A73C` | CTAs, links, key highlights, "dawn" |
| **indigo (secondary)** | `#7C7FF0` | accents, "dusk", secondary buttons |
| green (status) | `#45D48A` | live/"● up" status dots only — sparing |

### Color — Day (light)
| Token | Hex | Use |
|---|---|---|
| bg | `#F6F3EC` | warm off-white paper |
| surface | `#FFFFFF` | cards, code blocks |
| border | `#E7E1D5` | hairlines |
| text | `#14171F` | primary text |
| text-muted | `#5B6270` | secondary text |
| **amber (primary)** | `#E8942C` | CTAs, links (slightly deeper for contrast) |
| **indigo (secondary)** | `#3D3F8C` | accents, secondary buttons |
| green (status) | `#2FB673` | status dots |

### Signature gradient
`linear-gradient(135deg, amber → indigo)` — dawn to dusk. Use *sparingly*: hero headline
underline/accent, the "up → down" day-arc graphic, one hero glow. Never as a full-bleed
background wash.

### Typography
- **Display / headings:** `Space Grotesk` — geometric, technical, a little character.
  Tight tracking on large sizes. Weights 500/700.
- **Body / UI:** `Inter` — 400/500. Comfortable line-height (1.6).
- **Mono:** `JetBrains Mono` — every CLI command, hostname, code, and inline token
  (`jauntbox-you`, `tofu apply`). Mono is core to the identity, not decoration.
- Scale: big confident hero (clamp ~40–72px), generous section headings, calm body.

### Shape, space, motion
- **Radii:** 10px cards/buttons, 12px terminal window. Soft, not pill-shaped.
- **Borders:** 1px hairline in `border` token; the aesthetic is line-drawn, not shadowed.
  Shadows only faint and cool, never heavy drop-shadows.
- **Spacing:** roomy. Generous vertical rhythm between sections (96–128px desktop).
  Max content width ~1080px, comfortable side padding.
- **Iconography:** thin line icons (Lucide-style), 1.5px stroke. No filled/3D icons.
- **Imagery:** NO photos of people/offices. Visuals are diagrams, terminal windows, and
  the day-arc motif. A subtle dotted-grid or faint topo-line texture is welcome.
- **Motion (if offered):** minimal — a status dot pulse, a gentle sunrise→sunset color
  shift on the hero arc. Nothing bouncy.

### Reusable components
- **Terminal card:** dark `surface` window, three faint traffic-light dots top-left, mono
  body, a muted `$` prompt, amber for the command the user types, muted for output. This
  is the hero prop and recurs throughout.
- **Buttons:** primary = solid amber, ink-dark label; secondary = 1px border + text, no
  fill. Mono or Inter-500 labels.
- **Status pill:** small rounded chip with a green pulsing dot + `jauntbox-you · up`.
- **Feature card:** hairline-bordered, line icon top, short bold title, one-sentence body.
- **Badges:** small mono tags — `EU-sovereign`, `MIT`, `Hetzner`, `UpCloud`, `Tailscale`,
  `OpenTofu`.

---

## 4. Site map

1. **Landing** (long single page, sections below)
2. **Blog index** (list of posts)
3. **Blog post** (article template — the launch post lives here)

Global: sticky slim nav (wordmark `jaunt` left; `How it works · Providers · Blog · GitHub`
right; Day/Night toggle). Footer: WA Systems cross-link, MIT, GitHub, repo `WA-Systems-EU/jaunt`.

---

## 5. Screens (one Stitch prompt each)

### Screen A — Landing

> Prompt to paste:
> "A dark-mode landing page for a developer infrastructure tool called **jaunt**. Slim
> sticky nav: wordmark 'jaunt' in Space Grotesk left; links How it works, Providers, Blog,
> GitHub, and a Day/Night toggle right.
>
> **Hero:** oversized headline 'An ephemeral remote development environment you pop into and
> out of.' with a dawn-amber-to-dusk-indigo gradient accent under the key phrase. Subhead:
> 'Spin a powerful dev box up in the morning, delete it at night. Your work persists on a
> volume; the box tears itself down when idle.' Two buttons: solid amber 'Read the launch
> post' and outline 'View on GitHub'. To the right, a dark terminal window card showing a
> monospace session: `$ tofu apply -var box_on=true` (command in amber) then muted output
> lines ending in a green '● jauntbox-you · up' status pill.
>
> **Section 'The cost-honest insight':** a single large quiet statement — 'A stopped cloud
> server still bills. Only a deleted one stops the meter.' — with a short paragraph.
>
> **Section 'How it works':** a horizontal day-arc graphic (sunrise amber left → dusk indigo
> right) with 4 steps beneath as hairline-bordered cards with thin line icons: 1) Morning:
> `tofu apply` — box up, volume attached. 2) Work over a stable Tailscale hostname. 3) Idle:
> the box notices no active session. 4) Dusk: it deletes itself — volume kept.
>
> **Section 'Never kills active work':** highlight the idle self-teardown guarantee — a card
> explaining a session at 2am always keeps the box; only genuine idle triggers teardown.
>
> **Section 'Two EU providers, one workflow':** two side-by-side cards Hetzner and UpCloud
> with mono badges, showing the same command works on both; caption 'cheap and sovereign by
> construction'.
>
> **Section 'Per-engineer isolation':** short card — one box per engineer, reachable only by
> that engineer, over Tailscale.
>
> **Quick start:** a wide terminal card with the 30-second try (cd tofu/hetzner; cp tfvars;
> tofu init; tofu apply -var box_on=true; ... tofu apply -var box_on=false).
>
> **Footer:** 'Built by WA Systems', links GitHub / MIT license / wa-systems.eu, small mono
> badges EU-sovereign, OpenTofu, Tailscale.
>
> Style: warm off-white OR deep near-black indigo background depending on theme; Space Grotesk
> headings, Inter body, JetBrains Mono for all commands; single amber accent; hairline borders,
> generous whitespace, no photos, thin line icons."

### Screen B — Blog index

> "Blog index for jaunt matching the same dark/light theme. Slim page header 'Writing' with a
> one-line subtitle. A vertical list of post cards, each a hairline-bordered row: date (mono,
> muted), title (Space Grotesk), one-line excerpt, and a small amber 'Read →' link. First/
> featured post is the launch post 'jaunt — an ephemeral remote development environment you pop
> into and out of'. Same nav and footer as the landing page. Roomy spacing, no images required."

### Screen C — Blog post (article template)

> "A long-form blog article page for jaunt, same theme. Centered reading column ~680px. Article
> header: mono date + reading time, large Space Grotesk title, subtitle. Body in Inter at
> comfortable size and line-height, with styled headings, blockquotes (amber left border), and
> monospace code blocks in a dark surface card with subtle traffic-light dots. Inline code in
> mono with a faint tinted background. A back-to-blog link and 'Built by WA Systems' footer.
> Clean, text-first, developer-doc quality."

---

## 6. Do / Don't

- **Do:** show real commands; keep one accent; use whitespace; make Night mode the hero.
- **Do:** let the terminal window and day-arc be the only 'illustrations'.
- **Don't:** stock photos, people, 3D blobs, multi-color gradients, glassmorphism, pill buttons,
  heavy shadows, emoji, exclamation-mark hype.

---

## 7. Assumptions to confirm

- **Logo:** wordmark-only `jaunt` (lowercase, Space Grotesk) — no icon mark yet.
- **Domain:** placeholder; the plan floated `jaunt.sh` / `getjaunt.dev`.
- Fonts are Google-available (Space Grotesk, Inter, JetBrains Mono) so Stitch can use them
  directly.
