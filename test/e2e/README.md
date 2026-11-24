# End-to-end tests

Three tiers, because "end to end" means different things depending on how much
real machine you are willing to pay for.

| Tier | Where it runs | What is real | Cost | CI |
|---|---|---|---|---|
| **1. logical** — `scripts/e2e.sh` | the host, any OS | the server, the agent binary, HTTP, the signed bundle, files in a scratch root | seconds | every push |
| **2. real OS** — `test/e2e/guest/verify.{ps1,sh}` | a real Windows / macOS machine | everything above **plus** real registry, real local accounts, real services/launchd, real Program Files / /Applications, real TCC and UCPD | minutes | every push (GitHub-hosted runners are real OSes) |
| **3. real machine** — `test/e2e/vm/*/run.sh` | a booted Windows 11 / macOS VM | everything above **plus** a machine that was installed from an ISO, provisioned from scratch, and driven over SSH | 30–60 min first run | manual / self-hosted with KVM |

Tier 1 catches logic bugs. Tier 2 catches "the module lied about what it did".
Tier 3 catches "it works on my laptop but not on a freshly installed machine".

## Tier 1 — logical end to end

```bash
bash scripts/e2e.sh        # or: make e2e
```

Starts a real server, mints a token, enrols a real agent binary, has the server
sign a bundle, has the agent verify the signature and write files into a scratch
root, then asserts idempotency and that a replayed token is refused.

## Tier 2 — real OS, no VM

The same suite the VM harness runs, minus the VM. It needs root (or admin) because
it creates users, writes Program Files / /Applications and installs a service.

Linux is not supported here: the platform-specific assertions are Windows and
macOS only.

```bash
# macOS (as root)
sudo env SERVER=http://127.0.0.1:8080 ADMIN_TOKEN=... \
  AGENT=$PWD/bin/fleet-agent INSTALLER=$PWD/scripts/install-unix.sh \
  bash test/e2e/guest/verify.sh
```

```powershell
# Windows (elevated)
.\test\e2e\guest\verify.ps1 -Server http://127.0.0.1:8080 -AdminToken ... `
  -Agent .\bin\fleet-agent.exe -Installer .\scripts\install-windows.ps1
```

Both scripts mint their own enrolment token, assign their own policy, and print a
PASS/FAIL line per check. They exit non-zero on any failure.

What they check (all against the live OS, no mocks):

- enrolment works and a replayed token is refused
- one report per policy item, with the status the OS actually justifies
- `policies.json` exists at the real path, parses, and force-installs the extension
- `user.account` reports `compliant` for a standard user and `drifted` for an
  admin — cross-checked with `net localgroup Administrators` / `dseditgroup`
- `browser.default` never reports `changed`, and on Windows the `UserChoice` key
  is byte-identical before and after (proving nothing tried to write it)
- a second cycle changes nothing: same generation, same file mtime
- the service/launchd job installs, runs, and uninstalls cleanly (this is what
  caught the agent missing `StartServiceCtrlDispatcher`, i.e. error 1053)
- **no lying**: whenever a module reports `changed`/`compliant`, the observable
  effect must exist on disk

`verify.sh` avoids `python3`/`jq` on purpose: a bare macOS VM has neither.

## Tier 3 — a real Windows VM

```bash
test/e2e/vm/windows/run.sh            # boot (or reuse) and test
RESET=1 test/e2e/vm/windows/run.sh    # wipe the disk, reinstall Windows
DOWN=1  test/e2e/vm/windows/run.sh    # stop the VM when done
```

Requirements: Linux host with `/dev/kvm`, Docker with the compose plugin, ~40 GB
disk, `ssh`/`scp`.

How it works:

1. `oem/install.bat` runs at the end of the unattended install: it installs the
   host's public key into `administrators_authorized_keys`, sets
   `LocalAccountTokenFilterPolicy=1` (so an SSH session of a local administrator
   is elevated — **test VMs only**), and installs OpenSSH **from a local zip**
   staged by `run.sh` in `oem/`.
   `Add-WindowsCapability` is deliberately not used: it pulls the
   Feature-on-Demand from Windows Update and hung for 20+ minutes on a fresh VM.
2. The host starts a fleet server on `:8080`, builds `fleet-agent.exe`, waits for
   SSH, and `scp`s the agent plus the suite into `C:\fleet-e2e`.
   The dockur `/shared` mount is *not* used: its drive mapping depends on the
   image's logon script and was simply absent in the guest.
3. The suite runs under `ssh -R 8080:127.0.0.1:8080`, so the guest talks to
   `http://127.0.0.1:8080` through a reverse tunnel. The guest cannot route to
   the host: `host.lan` resolves to the container, not the Docker host.
4. Results land in `logs/verify.log` and `C:\fleet-e2e-results.json`.

First boot downloads ~8 GB and installs Windows; expect 20–60 minutes on modest
hardware. Later runs reuse `storage/` and start in about a minute.

## Tier 3 — a real macOS VM

dockur/macos **cannot install unattended**: the installer needs a human in the web
console (Disk Utility → erase → reinstall → create account). So the OS install is
a one-time manual step, and everything after it is scripted:

```bash
test/e2e/vm/macos/run.sh --prepare   # boot the VM, print the console URL
# ... finish the installer in the console, then in the VM's Terminal:
#     sudo -S mount_9p shared
#     bash /Volumes/shared/bootstrap-guest.sh
test/e2e/vm/macos/run.sh             # unattended from here on
```

`bootstrap-guest.sh` enables Remote Login, installs the host key, and grants
passwordless sudo (throwaway VM only).

**Licensing:** Apple only permits macOS virtualisation on Apple hardware. Do not
put this in CI; use a real Mac or an Apple-hosted runner instead.

## Why not macOS VMs in CI

GitHub-hosted `macos-latest` runners *are* real macOS on Apple hardware
virtualisation, which is why tier 2 runs there. A nested macOS VM inside a Linux
runner would be both slow and a licence problem.

## Adding a check

1. Add the module behaviour in `internal/agent/modules`.
2. Add a unit test (fast, all platforms).
3. Add an assertion to `verify.ps1` **and** `verify.sh` if the module is
   cross-platform: the point of tier 2 is that the platform agrees with you.
4. If the assertion depends on a human click (a consent or identity gate), assert
   the *reported* status instead of the end state, and make sure the status is
   never `compliant` when the effect is absent.
