# Changelog

All notable changes to jaunt are recorded here, in the style of
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/). Versions follow the
numbering rules in `CONTRIBUTING.md`.

## [0.4.0] - 2026-11-13

jauntbox gives you a personal, self-deleting Hetzner box on your tailnet. Secrets
live in a private GitHub App you own, agents run in their own containers, you can
reach your own services from your laptop through allowed host ports and previews,
and a terminal UI shows the whole thing at a glance.

### Upgrade notes

First release. Install with `install.sh`. `box_binary` is now only needed for
development builds; the next `jauntbox up` reinstalls the box binary at 0.4.0.
