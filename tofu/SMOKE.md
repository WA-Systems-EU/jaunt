# Live smoke test (opt-in, manual)

**Not run in CI.** This is the one real create/destroy check — it stands up an
actual billed server, so it only runs when you have real credentials and choose
to run it. CI covers everything that doesn't cost money (`decide()` tests, `tofu
fmt`/`validate`); this covers the rest.

Run it once per provider after a change that could affect provisioning
(cloud-init, the module, or a root).

## Procedure

Pick a root: `tofu/hetzner` or `tofu/upcloud`. Then:

1. **Export credentials.**
   - Hetzner: `export HCLOUD_TOKEN=...`
   - UpCloud: `export UPCLOUD_USERNAME=... UPCLOUD_PASSWORD=...`
   - Both: `export TAILSCALE_API_KEY=... TAILSCALE_TAILNET=...`

2. **Configure vars.**
   ```sh
   cd tofu/<provider>
   cp terraform.tfvars.example terraform.tfvars
   # edit: set `user` and `ssh_public_key`
   ```

3. **Stand the box up.**
   ```sh
   tofu init
   tofu apply -var box_on=true
   ```

4. **Verify the box.** Wait for it to appear on your tailnet (MagicDNS
   `jauntbox-<user>`), then:
   ```sh
   ssh jauntbox-<user>                 # Tailscale SSH (no port 22 is open)
   df -h /home                          # /home is the mounted persistent volume
   systemctl status jaunt-idle.timer    # active (armed for self-teardown)
   ```

5. **Prove the volume survives a toggle.**
   ```sh
   ssh jauntbox-<user> 'echo hello > /home/<user>/SMOKE_MARKER'
   tofu apply -var box_on=false          # deletes the server, KEEPS the volume
   tofu apply -var box_on=true           # brings a fresh box up, re-attaches volume
   ssh jauntbox-<user> 'cat /home/<user>/SMOKE_MARKER'   # → hello  (survived)
   ```

6. **Tear down completely.**
   ```sh
   tofu destroy      # removes the volume too — do this when fully done
   ```

## What "good" looks like

- **Warm `up` time:** the box is reachable and `/home` mounted in **under ~2
  minutes** on a re-attach (validates the target in `jaunt-cli-plan.md` §12).
- **Cost (Hetzner, CAX41):** ≈ **€0.045/hr** for the server while it's up; the
  100 GB volume is ≈ **€5.72/mo** and persists across toggles. The whole point:
  when the box is down you pay only for the volume, not the server.
- **Self-teardown:** left genuinely idle, the box deletes *itself* after
  `idle_day` / `idle_night` — `df -h /home` on the next `up` still shows your
  data.
