# Changelog

## 0.1.0 — 2026-10-18

First release.

- Sanitized initial release of the two-vendor opencode v2 orchestration configuration, role
  prompts, usage readers, routing, CLI bridges, mailbox, manifests, workers and supervisor.
- Reversible managed-entry symlink installation, optional copy mode, private force backups,
  ownership checks and archive-preserving uninstall.
- Root setup, routing, security, recovery and contribution documentation; MIT license.
- Retained 14 fast runtime unit tests and added 29 isolated installer integration cases under
  separate root `tests/`, outside the doctor's unit-test timeout.
- Initial packaging observation: Codex exec CLI argument parsing failed before delegation in that
  environment; this is not a fresh reproduction on the latest live CLIs. The original runtime is
  retained, not represented as end-to-end verified.

See [orchestrator/CHANGELOG.md](orchestrator/CHANGELOG.md) for the original implementation notes.
