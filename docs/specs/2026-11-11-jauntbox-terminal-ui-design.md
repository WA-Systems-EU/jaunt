# jauntbox terminal UI — design

Date: 2026-11-11. Owner: Thomas (WA Systems). Status: agreed in conversation, to be planned.

## Why

`jauntbox up` feels silent: it prints "created …" and the rest (tailnet join, provisioning, supervisor install, unlock) happens in the background, so you don't know what the box is doing or when it is ready. `jauntbox init` is a plain question-and-answer script. The owner wants "something modern in terms of animation and logs of the steps": you see each step happen, how long it took, and where it stands, and setup feels like a guided tool rather than a script.

Success: running `jauntbox up` you watch the box come to life step by step and end on a clear "Ready" with how to connect; running `jauntbox init` is a short guided form with real choices instead of typing names from memory.

## Goals

1. **`up` as a live step list** (the owner chose option A of three mockups).
2. **`down`, `resize`, `status` in the same step style.**
3. **`init` as an interactive form** (the owner chose option B of two mockups).
4. **A clean plain fallback** whenever the output is not an interactive terminal.

## Non-goals (for now)

- `setup github`, `unlock`, `secrets`, `night`, `ssh` and everything on the box (`jauntctl agent …`, its runs table, `logs`) keep their current output. The owner: "setup github might be good to improve but for now those can be left as is; we will see with usage."
- No full-screen dashboard for `up` (option C was not chosen) and no live log tail under the provisioning step (option B for `up` was not chosen).
- No change to what the commands do, their flags, exit codes or the order of operations, except `up`'s foreground behaviour (below).

## Anti-goals

- Output that breaks or turns into escape-code noise in a pipe, a log file, CI, `TERM=dumb`, or with `NO_COLOR` set.
- A look that hides errors: a failed step must be as visible as a successful one, with the message and the fix.
- Losing the record: after `up` finishes, the scrollback must still show every step and its time.

## The look, shared by all step-style commands

- **Header line**: `jauntbox` in bold, then dim context. For `up`: `· <user> · <profile> (<server type>) · <location>`.
- **One line per step**, indented two spaces, label column aligned:
  - pending: dim `·` and the label;
  - running: a braille spinner (⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏) in the yellow accent, the label, and a live elapsed timer;
  - done: teal `✓`, the label, the duration (`0.2s`, `41s`, `1m12s`), then optional dim detail (server id, box name and tailnet IP, config path);
  - failed: red `✕`, the label, the duration, and below it, indented, the error message and what to do (and, when the failure is on the box, the log path there).
- **End line** for `up`: teal `●` and bold `Ready in 2m31s.`, then when the box deletes itself (evening cutoff time or idle limit, from the config), and how to connect (`ssh jauntbox-<user>` or `jauntbox attach`/the existing connect command).
- **Palette** from the site: teal `#4fc2b9` (done, ok), yellow `#f3d36b` (running, attention), red (failure), dim grey for secondary text. Use the terminal's 256-colour/true-colour approximations; never rely on colour alone (the ✓/✕/· symbols carry the state).

## `jauntbox up` (and bare `jauntbox`, before it attaches)

- **Stays in the foreground until the box is ready** (today this is `up --wait`; it becomes the default). Steps, in order, mapped to what `client.Up` and `PushBinary` already do:
  1. Policy and limits
  2. SSH key, firewall, volume
  3. Server created (detail: server id)
  4. Joined the tailnet (detail: `jauntbox-<user>` and its tailnet IP)
  5. Provisioning (waiting until the box user exists and root ssh works; spinner and timer only)
  6. Install supervisor
  7. Unlock secrets
- **Existing box**: the header says the box already exists; steps already true show ✓ immediately (or are skipped), then "make sure the supervisor is installed" and unlock run as steps.
- **Ctrl-C**: the box keeps finishing in the background exactly as today's background push does, and `up` prints one line saying so and where its log is (today `push.log` in the state dir).
- **Warnings** (today printed as `warning: …`, e.g. a retrying root ssh refusal) appear as dim/yellow lines under the running step, without breaking the list.
- `--wait` keeps working (now a no-op, or documented as the default) so existing scripts don't break. A way to keep today's return-immediately behaviour is the builder's call (e.g. `--detach`); the owner did not ask for one.

## `down`, `resize`, `status`

- `down`: steps for what it already does (ask the box to wrap up agents, wait for it, revoke tokens/teardown, box deleted, volume kept), then an end line. `--force` shows its shorter list.
- `resize`: the down steps then the up steps under one header (`resize <from> → <to>`).
- `status`: not an animation; a short styled summary (box, state, server type, created, on tailnet ✓/✕, locked or not, the supervisor's verdict and next deadline) instead of today's line plus raw JSON. The raw JSON stays available behind a flag (e.g. `--json`).

## `jauntbox init`

- **Interactive form** with arrow keys, laid out as numbered steps ("step 4 of 6") with a way back (Esc).
- Fields, with the current detection as the pre-selected default so Enter accepts it:
  - user name: text field, pre-filled;
  - SSH key: menu of the public keys found in `~/.ssh`;
  - timezone: searchable list (`/` to search), detected zone selected;
  - Hetzner token: hidden input, checked on the spot with a spinner; a bad token is asked again inside the form;
  - location: menu of Hetzner locations;
  - server types: for each of small, big, huge, a menu of the types fetched with the token (vCPU, RAM, shared/dedicated, hourly price), with today's automatic `PickProfiles` choice pre-selected;
  - Tailscale API key: hidden input, checked with a spinner.
- **End**: ✓ lines for writing the config, the env file and the ssh config block (as today), then the Tailscale policy step set apart as "one step only you can do" (the snippet printed below it), then `Ready. Next: jauntbox setup github, then jauntbox up`.
- Ctrl-C quits without writing anything.
- Saving the Tailscale snippet to a file (e.g. `~/.config/jauntbox/tailscale-acl.json`) is a suggestion from the mockup; the owner did not object. Builder's call.

## Plain fallback

When stdout (or stderr, for the stream being drawn) is not a terminal, or `NO_COLOR` is set, or `TERM=dumb`, or a `--plain` global flag is given:

- step commands print one line per finished step (`✓`/`✕`, label, duration, detail) as it finishes, with no spinner, no cursor movement and no colour; errors as today;
- `init` falls back to today's line-by-line prompts (same questions, same validation), so it can still be scripted.

## Calls made after the builder check (mine, not the owner's; listed for review)

1. **Bare `jauntbox` and `resize` get the step list too.** Bare `jauntbox` runs the same work as `up` (create or find the box, push, wait for provisioning, ensure the supervisor) before it attaches, so it shows the same steps, then attaches. `resize` stays in the foreground until the new box is ready, like `up`.
2. **Warnings are a third outcome.** A follow-up problem that today prints `warning: …` and exits 0 (unlock failed, an event that could not be recorded, a retrying root ssh refusal) shows as a yellow `!` on that step with the message under it; the run still ends on "Ready" (with "with warnings") and exit codes stay as today. A step that does not apply (no secrets to unlock) shows as skipped (dim `–`).
3. **Ctrl-C by phase.** Before the server exists (steps 1–3): stop as today, nothing more is created. Once the server exists (steps 4–7, and the existing-box path): hand off to the detached `internal-push` (today's background path), print one line saying the box keeps finishing in the background and where `push.log` is, exit. Never two pushes at once: the foreground push is cancelled before the hand-off starts. `down` and `resize` keep today's Ctrl-C behaviour.
4. **`init` keeps its three confirmations as form steps.** "A config already exists, overwrite?" (default No) comes first, before the form. "Save the tokens to the env file" (default Yes) and "Add the ssh config block" (default Yes) are one final review step before anything is written. The plain fallback asks the same questions. The step counter counts the real screens.
5. **`down` shows what the laptop can see**: ask the box to wrap up and stop, wait for the box to delete itself, (force delete if it had to), box deleted and volume kept. No new box-side progress signal.
6. **Location in `init`.** If a home volume `jauntbox-<user>-home` already exists, its location is pre-selected and choosing another one shows a warning that the volume can't move (the next `up` would fail). Otherwise the menu defaults to today's `fsn1`.

## Constraints

- Go, existing cobra CLI. The step model must be testable without a terminal: the logic (`internal/client`) reports step events (start, detail, warning, done, failed) to a small interface; a renderer turns them into the animated view or the plain lines. Tests use the plain renderer or a fake.
- Tests never call real Hetzner, Tailscale or GitHub (existing rule).
- Library choice is the builder's, within the Go ecosystem; the natural fit is Charm (lipgloss for styling; Bubble Tea and its form package for `init`; a light spinner for the step list). Keep the step list out of an alternate screen so the scrollback keeps the record.
- Commits: plain conventional messages, no attribution trailers.

## Left to the builder

- The exact package layout (e.g. `internal/ui`), the event interface, and how `PushBinary`'s retry loop reports progress.
- Whether `status` keeps any JSON by default.
- Spinner frame rate, timer format details, terminal-width handling (truncate details, never wrap the step line).
- Whether `--detach` exists.
- Wording of each step label and end line, following the README's plain style.
