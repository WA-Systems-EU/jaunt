# jauntbox website

The public site for jauntbox: four static pages in `site/`, with no build step. The
header, footer and `<head>` are duplicated on each page; change all four together.

| Page | Purpose | Content |
|------|---------|---------|
| `index.html` | Landing, marketing | Hero, the problem, the scare, short isolation and agents teasers, how it works, first-morning onboarding, luggage (server vs. volume), roadmap teaser, cost, call to action |
| `quickstart.html` | From nothing to the first agent | Prerequisites, build from source, `init`, tailnet policy, `setup github`, secrets, `up`, attach, `unlock`, first `jauntctl agent`, `down` |
| `docs.html` | Feature documentation | Box lifecycle and limits, GitHub App and secrets, root and sudo, agents, isolation, wrap-up, configuration (with a table of contents) |
| `roadmap.html` | Project plans | Status legend and one card per plan, linking to the plan file and to `SMOKE.md` |

Preview it locally:

```sh
python3 -m http.server -d site 8000   # then open http://localhost:8000
```

- Styles use the Tailwind play CDN (pinned to 3.4.17) and Google Fonts, with the same inline
  Tailwind config and colour tokens on every page. Dark mode follows the browser's
  `prefers-color-scheme` (or `data-theme` on `<html>`). Before a wide launch, compile
  Tailwind to a static stylesheet so the pages don't build their CSS in the browser.
- The nav is the same on every page and marks the current page with `aria-current="page"`.
  Keep in-page links pointing at the page that holds the content (for example
  `docs.html#agents`), and check them after moving a section.
- Commands and behaviour come from the repo `README.md` and `examples/jauntbox.yaml`. Don't
  describe features that aren't there.
- Status labels (Done, Next, Later) on the roadmap page and the roadmap teaser on the
  landing page must match the plans in `docs/superpowers/plans/` and the results in
  `docs/jauntbox/SMOKE.md`. Update them when a plan lands.
- Deployed to https://jauntbox.eu by GitHub Pages (`.github/workflows/pages.yml`) on every push to `trunk` that touches `site/`; `site/CNAME` holds the domain.
