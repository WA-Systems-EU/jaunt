# jaunt — Tofu + Site Milestone — Design

> **Status:** approved design (2026-07-21). This spec covers the *first public push* of jaunt:
> a working OpenTofu jauntbox on Hetzner then UpCloud, plus a static site with launch blog
> content, published under `WA-Systems-EU/jaunt`. The Go CLI (`jaunt-cli-plan.md`) is a
> **later** milestone and is deliberately out of scope here.
>
> **Source material:** `../../../../papers/jaunt-cli-plan.md` (full product design),
> `../../../../papers/remote-devbox.md` (the cost pattern). The three
> `opentofu-*-best-practices.md` production-hosting references are **out of scope** for this push.
>
> **Amendment (2026-07-22):** after the tofu foundation was built and merged, **per-engineer
> isolation** was added — one box per engineer, reachable only by that engineer. This introduced
> per-user Tailscale tags (`tag:jaunt-<user>`) and a new admin-run `tofu/tailnet` root that owns
> the tailnet ACL. Sections 2, 4, 5, and the new **§6** reflect this; the original "one box per
> dev" non-goal (§1) is superseded.

---

## 1. Purpose & scope

jaunt is **an ephemeral remote development environment you pop into and out of** — a powerful
remote dev box you spin up in the morning and delete at night, with all work kept on a
persistent volume and reachable over a stable Tailscale MagicDNS hostname.

Secondary goal (the reason this repo is public): a **credibility artifact** for EU-sovereign,
cost-honest infrastructure work. Not a SaaS — the value is the pattern, not a hosted service.

**This milestone delivers, tofu-only (no Go CLI yet):**

1. A **repo scaffold** at `WA-Systems-EU/jaunt` (local: `Code/WaConstellation/jaunt`).
2. A **Hetzner jauntbox** buildable end-to-end with `tofu apply` (toggle-based).
3. A **UpCloud jauntbox** — the same pattern ported to a second EU provider.
4. **Per-engineer isolation** (added post-approval): one box per engineer, reachable only by that
   engineer, via per-user tags + a tailnet ACL (`tofu/tailnet`). See §6.
5. A **static site + launch blog post** (Next.js + Tailwind, static export).
6. **Publish**: public repo + site deployed via Odysseus.

### Success criteria

- `tofu apply -var box_on=true` in `tofu/hetzner/` produces a **reachable, usable** jauntbox:
  persistent volume mounted at `/home`, Tailscale SSH working, idle self-teardown armed.
- The same is true in `tofu/upcloud/` with the **same variable surface**.
- Destroying the server (`-var box_on=false`) **never loses work** — data is on the volume.
- The **idle-watch `decide()`** logic and cloud-init are defined **once** (shared module) and
  demonstrably run on both providers.
- The repo reads as a finished, credible open-source artifact: README with the "short
  excursion" hook, MIT license, working examples, a launch blog post.
- **Isolation holds:** with the tailnet ACL applied, engineer A can reach only `jauntbox-A`;
  A cannot reach `jauntbox-B`. Enforced default-deny at both the cloud firewall and Tailscale.

### Non-goals (this milestone)

- The Go CLI / fast API hot path — later milestone (`cmd/`, `internal/` reserved but empty).
- Remote tofu **state backend** setup — examples use local state with a "use remote for real"
  note. Wiring a real backend is a separate task.
- A **Packer base image** — deferred; cloud-init on stock Ubuntu for now (base image is a var,
  so a snapshot slots in later without structural change).
- Production-hosting stacks (LB / k3s / UKS / managed Postgres) from the best-practices docs.
- Any web UI / dashboard; billing / SaaS. (Multiple engineers, one isolated box each, **is** now
  in scope — see §6 — but each engineer still runs their own tofu; there's no orchestration layer.)

---

## 2. Repository layout

```
jaunt/
├── README.md                  # "ephemeral remote dev env you pop into and out of" hook
│                              #   + 30-second try + WA Systems cross-link
├── LICENSE                    # MIT
├── .gitignore                 # *.tfvars (except *.example), .terraform/, tfstate, node_modules, out/
├── tofu/
│   ├── modules/
│   │   └── jauntbox/            # provider-NEUTRAL shared pieces
│   │       ├── cloud-init/    # templated: volume mount, Tailscale join, jaunt.yaml setup runner
│   │       ├── agent/         # systemd unit + idle-watch decide() script (POSIX shell)
│   │       ├── variables.tf   # inputs the shared templates need (hostname, tag, idle windows, …)
│   │       └── outputs.tf     # rendered cloud-init user-data string
│   ├── hetzner/               # hcloud implementation (root config)
│   │   ├── main.tf  providers.tf  variables.tf  outputs.tf
│   │   └── terraform.tfvars.example
│   ├── upcloud/               # upcloud implementation (root config, mirrors hetzner)
│   │   ├── main.tf  providers.tf  variables.tf  outputs.tf
│   │   └── terraform.tfvars.example
│   └── tailnet/               # admin-run: tailnet ACL that isolates engineers (§6)
│       ├── main.tf  providers.tf  variables.tf  outputs.tf
│       └── terraform.tfvars.example
├── site/                      # Next.js + Tailwind, static export
│   ├── app/ (or src/)         # landing page + blog
│   ├── content/blog/          # Markdown/MDX posts
│   ├── Dockerfile             # container build → static `out/`
│   ├── tailwind.config / next.config (output: 'export')
│   └── deploy/                # Odysseus deploy config (target, service)
├── examples/
│   └── jaunt.yaml             # per-project config (plan §6): plan, repos, setup, ports, idle
├── docs/
│   └── superpowers/specs/2026-07-21-jaunt-tofu-site-design.md   # this file
└── cmd/  internal/            # RESERVED — Go CLI milestone; .gitkeep only for now
```

**Two-provider structure — chosen approach (A).** The jauntbox pattern is ~80% provider-neutral
(persistent volume, default-deny firewall, Tailscale join via cloud-init, idle self-teardown,
discovery by label) and ~20% provider-specific (resource shapes, IP/network model, self-delete
API call). We put the neutral 80% in `tofu/modules/jauntbox` (cloud-init templates + the idle
agent script) and keep thin `hetzner/` and `upcloud/` roots that wire provider resources and
call the shared module. The signature idle-`decide()` behavior — the thing the blog is about —
is written once and provably runs on both. (Rejected: two fully independent trees, which would
duplicate the agent + cloud-init and let them drift.)

---

## 3. Shared jauntbox module (`tofu/modules/jauntbox`)

Provider-agnostic. Renders the artifacts both roots consume:

- **cloud-init user-data** (templated) that on first boot:
  1. mounts the attached persistent volume at `/home` (idempotent; format only if empty),
  2. installs Tailscale and runs
     `tailscale up --hostname <prefix>-<user> --advertise-tags=tag:jaunt-<user> --ephemeral --ssh`
     using an ephemeral auth key passed in as a template var (the key is read from a `0600` file,
     not the command line, so it never lands in cloud-init logs),
  3. runs the `jaunt.yaml` `setup` steps (idempotent; fast when the volume is warm),
  4. installs the **idle-watch** systemd unit + timer and arms it.
- **the idle-watch agent** (`agent/`): a POSIX-shell `decide()` plus a systemd timer (~every
  1–2 min). Pure decision:

  ```
  decide(active_sessions, cpu_over_window, keep_marker, now) → KEEP | WARN | DELETE
  ```

  1. **Any active SSH/mosh/tmux session ⇒ KEEP** (unconditional — working at 2am never costs
     you the box).
  2. **keep_marker in the future ⇒ KEEP.**
  3. Else, idle (no session **and** CPU below threshold) longer than the window
     (**day 2h / night 1h**, from `jaunt.yaml`) ⇒ **WARN** → short grace → **DELETE** own server
     via the provider API using a project-scoped token.

  The provider-specific **self-delete call** is injected into the agent as a small templated
  snippet (hcloud API vs upcloud API) — the *only* provider-aware part of the agent.

**Module inputs:** `hostname_prefix`, `user` (→ `<prefix>-<user>` + label `jaunt=<user>`),
`tailscale_tag`, `tailscale_auth_key` (ephemeral, sensitive), `idle_day`, `idle_night`,
`setup_steps` (from jaunt.yaml), `self_delete_snippet` (provider template).
**Module output:** rendered `user_data` string.

---

## 4. Hetzner root (`tofu/hetzner`)

Provider `hetznercloud/hcloud` (operates within one project — the token *is* the project;
use a dedicated **`jauntboxes`** project so the on-box teardown token can never touch prod).

| Resource | Notes |
|---|---|
| `hcloud_ssh_key` | dev's public key |
| `hcloud_volume` | persistent `/home`; **declared independently** of the server so teardown never touches it |
| `hcloud_server` | `count = var.box_on ? 1 : 0`; `image = var.base_image` (Packer snapshot id *or* stock Ubuntu); `user_data` from the shared module; label `jaunt=<user>` |
| `hcloud_volume_attachment` | `count = var.box_on ? 1 : 0`; `automount = true` |
| `hcloud_firewall` | **default-deny inbound**; **no port 22** (Tailscale SSH only); apply by label |
| `tailscale_tailnet_key` | ephemeral, tagged **`tag:jaunt-<user>`** (per-engineer — the isolation hinge), fed to the module |

**Network:** IPv6 enabled; IPv4 optional (`var.public_ipv4`, default off to drop the fee) — the
box needs only *egress* to reach the tailnet; *ingress* is tailnet-only. **Variables** (the
canonical surface both providers mirror): `box_on` (bool), `user`, `plan` (default `cax41`),
`volume_size` (default 100), `ssh_public_key`, `hostname_prefix` (default `jauntbox`), `location`
(default `fsn1`), `public_ipv4` (default false), `idle_day` (default `2h`), `idle_night`
(default `1h`), `base_image`. **Outputs:** `hostname` (`jauntbox-<user>`), `magicdns`, `volume_id`.
`tofu apply -var box_on=true` → box up; `-var box_on=false` → box deleted, volume kept.

---

## 5. UpCloud root (`tofu/upcloud`)

Provider `UpCloudLtd/upcloud`. **Mirrors the Hetzner variable surface** so the two feel like one
product (`box_on`, `user`, `plan`/size, `volume_size`, `ssh_public_key`, `hostname_prefix`,
`zone`, `idle_day`, `idle_night`, `base_image`).

| Resource | Notes |
|---|---|
| `upcloud_storage` | persistent jauntbox data; independent of the server |
| `upcloud_server` | toggle via count; `user_data` from the shared module; **labels** for discovery (`jaunt=<user>`) |
| storage attach | attach persistent storage to the server |
| firewall | default-deny inbound + Tailscale SSH only |

Only differences from Hetzner: provider resources, the zone/network model, and the
`self_delete_snippet` (upcloud API delete-self). Everything else — cloud-init, the idle agent,
the Tailscale join, the `jaunt.yaml` runner — comes from the shared module unchanged.

---

## 6. Multi-engineer isolation (`tofu/tailnet`)

**Goal:** one box per engineer, reachable **only** by that engineer. This rests on two
independent, default-deny layers — a box is safe only when *both* hold:

1. **Cloud firewall** (both providers): default-deny inbound, no port 22. Nothing reaches a box
   over its public IP. (On UpCloud this is `firewall = true` on the server plus explicit
   accept-egress / drop-ingress rules; on Hetzner the label-applied `hcloud_firewall`.)
2. **Tailscale ACL** (the `tailnet` root): each box advertises **`tag:jaunt-<user>`**, and the
   tailnet policy grants each engineer SSH + network access to **only their own tag**,
   default-deny for everyone else. Without this layer a box is merely *not public* — any tailnet
   member could still reach it.

**The `tofu/tailnet` root** (admin-run, its own single state) generates the whole policy from one
input, `engineers` (a map `username → Tailscale login`):

- `tagOwners` for each `tag:jaunt-<user>` (a box whose tag isn't declared is **rejected** by
  Tailscale at join — hence tailnet is applied **before** any box),
- an SSH grant and a network ACL from each engineer's login to only their tag,
- an optional admin catch-all (`admin_full_access`, default on) so admins keep reach and can't
  self-lock-out.

Guardrails: `overwrite_existing_content` (default **false**) must be set true to let this root own
the tailnet ACL — a first apply won't silently clobber an existing policy; `reset_acl_on_destroy`
(default true) reverts to Tailscale's permissive default on teardown. Prefer not to let tofu own
the ACL? `tofu plan` emits the policy as the `policy_json` output to paste into the admin console.

**Per-engineer state.** A provider root describes *one* box, so N engineers = N states: a
`tofu workspace` per engineer for local/solo use, or a per-engineer remote-backend state key for
teams (so two engineers never share a state file). All resource names are already keyed by `user`,
so there are no cross-engineer collisions.

**Apply order:** `tofu/tailnet` (once, by an admin) → then each engineer runs a provider root with
their own `user`. The engineer's `user` must match a key in the `engineers` map.

---

## 7. Secrets & state

- **Secrets**, **never committed**: the Hetzner token and UpCloud creds are **tofu variables**
  (`hcloud_token`; `upcloud_username`/`upcloud_password`, which also authenticate the UpCloud
  provider — set once, no env needed). **Tailscale** authenticates from the environment
  (`TAILSCALE_API_KEY` + `TAILSCALE_TAILNET`) for all three roots; the key needs **auth-key + ACL
  write** scope (the `tailnet` root manages the ACL). `.gitignore` excludes `*.tfvars` (keeps
  `*.tfvars.example`), `.terraform/`, `*.tfstate*`. SOPS is documented as the recommended manager
  but not required to run the examples.
- **State:** examples use **local state** with a prominent "use a remote backend for real
  multi-user use" note. Wiring a real remote backend is explicitly out of scope for this push.

---

## 8. Site + blog (`site/`)

- **Stack:** Next.js + Tailwind, `output: 'export'` (fully static / close to it), low complexity.
- **Build/deploy:** containerized build (`Dockerfile` → static `out/`), pushed and deployed via
  **Odysseus** to an EU host (on-brand sovereign hosting). Deploy config lives in `site/deploy/`.
- **Look:** comes from a **Google Stitch** export the user provides; this milestone builds the
  structure and finer details around that, not a bespoke visual language from scratch.
- **Content:** one strong **launch post** — *"jaunt — an ephemeral remote development environment
  you pop into and out of"* — plus **outlines** (not full drafts) for 2–3 follow-ups:
  EU-sovereign cost-honest infra; the same jauntbox on two providers; the idle-auto-down guarantee.
- **Landing copy** reuses the one canonical hook line everywhere (README + site + launch post).

---

## 9. Publish (§ prerequisites are the user's)

- Public repo `WA-Systems-EU/jaunt` pushed; README cross-links WA Systems.
- Site deployed via Odysseus.
- **User-provided prerequisites** (built to slot in, not blocked on):
  1. **Domain** for jaunt's own site (plan floated `jaunt.sh` / `getjaunt.dev`) — placeholder
     until registered.
  2. **Odysseus deploy target** — which host + credentials.
  3. **Google Stitch** export for the site look.

---

## 10. Build order (milestones for the implementation plan)

1. **Repo scaffold** — layout above, README stub, MIT LICENSE, `.gitignore`, `examples/jaunt.yaml`.
2. **Shared module** — cloud-init templates + idle-watch `decide()` agent (the most-tested part).
3. **Hetzner root** — full toggle-able jauntbox; validate `apply box_on=true` reaches a live box.
4. **UpCloud root** — mirror; validate same behavior + same variable surface.
5. **Per-engineer isolation** (`tofu/tailnet`) — per-user tags + generated tailnet ACL. *(Done
   post-plan; §6.)*
6. **Site** — Next.js/Tailwind static scaffold + launch post + Odysseus deploy config.
7. **Publish** — push repo, deploy site (once domain / Odysseus target / Stitch look are in hand).

Steps 1–5 are **complete and merged to `trunk`**. Steps 6–7 (site + publish) await their own plan.

---

## 11. Testing

- **Idle `decide()`** is the primary tested unit: table tests over active-session veto (all
  hours), keep-marker, day vs night thresholds, CPU boundary. Encodes the "never kill active
  work" guarantee. (Shell test harness now; ports 1:1 to the Go `idle.decide()` later.)
- **tofu:** `tofu validate` + `fmt` in CI for all three roots (`hetzner`, `upcloud`, `tailnet`).
  One **opt-in** smoke test (`tofu/SMOKE.md`) that really creates+destroys a box in a throwaway
  project, guarded by real creds; it also verifies the volume survives a toggle-off/on.
- **Site:** builds clean in the container; static export produces valid `out/`.
