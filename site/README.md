# jauntbox website

The public landing page for jauntbox: one static file, `index.html`, with no build step.

Preview it locally:

```sh
python3 -m http.server -d site 8000   # then open http://localhost:8000
```

- Styles use the Tailwind play CDN (pinned to 3.4.17) and Google Fonts. Before a wide
  launch, compile Tailwind to a static stylesheet so the page doesn't build its CSS in
  the browser.
- Status labels (Live, Plan 03, Plan 04, Later) and the Roadmap section must match the
  plans in `docs/superpowers/plans/` and the results in `docs/jauntbox/SMOKE.md`.
  Update them when a plan lands.
- Deploying it (the earlier site spec named Odysseus) is not set up yet.
