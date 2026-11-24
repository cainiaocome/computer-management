## Rules

- Don't modify this AGENTS.md unless I ask me

## Documentation language

Bilingual documentation is a **requirement for all future development**, not a
one-off:

- Every user-facing document must exist in both English (`<name>.md`) and
  Simplified Chinese (`<name>.zh-CN.md`) — e.g. `README.md` / `README.zh-CN.md`,
  `docs/design.md` / `docs/design.zh-CN.md`.
- When you change one language, update the other **in the same commit**. A doc
  that drifts out of sync is a bug.
- Each document links to its counterpart at the top (or immediately after the
  title), and section structure / heading order must match across languages so
  cross-references (`docs/design.zh-CN.md` §2.2) stay valid.
- Keep technical identifiers, commands, file paths, registry keys, policy keys
  and code blocks **untranslated** — translate the prose around them.
- New directories that hold docs (`docs/platform/*`, `test/e2e/`) follow the same
  rule: add the `.zh-CN.md` sibling in the same change.
- Chinese docs use Simplified Chinese with a space between CJK text and inline
  Latin/code fragments, and use the same terminology glossary:
  agent → agent，server → 服务端，policy bundle → 策略包，enrolment → 注册，
  drift → 漂移，consent gate → 同意闸门，identity gate → 身份闸门，
  architectural gate → 架构闸门，enforcement class → 强制类。

## Testing expectations

- A behaviour change needs a unit test.
- A change to a **platform-facing** module also needs an assertion in
  `test/e2e/guest/verify.ps1` (Windows) and/or `test/e2e/guest/verify.sh` (macOS),
  because the point of that layer is that the real OS agrees with the code.
- Never assert that a consent gate (TCC, UAC) or an identity gate (`UCPD.sys`,
  Gatekeeper) was bypassed. Assert the *reported* status instead, and that the
  agent never claims success when the effect is absent.
