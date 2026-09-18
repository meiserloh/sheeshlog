# Changelog

Notable changes to sheeshlog. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project
follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## 1.0.0 — 2026-09-18

First release.

### Added

- Renders structured log lines from stdin into a fixed, compact layout: time,
  level, subject, message, then the fields that are not context.
- **Profiles** — a YAML recipe per log style, saying which fields carry the
  time, level, message and subject, which context fields repeat and so get
  suppressed, and which lines are noise. `json` and `logfmt` payloads; dotted
  paths; ordered subject candidates; regex noise rules.
- Timestamps read as RFC 3339, epoch seconds or epoch milliseconds, detected
  from the value or declared in the profile.
- The `kubectl logs --prefix` stream prefix is detected and used as the subject
  when the line offers nothing better.
- Lines that do not parse are passed through verbatim rather than dropped.
- Level filtering via `--level` or `LEVEL`, colour via `--color` or `NO_COLOR`.
- Commands: `list`, `use`, `check` — which reports what a profile does to a
  sample, field by field — and a picker when run bare in a terminal.
- `--profile` for a single run, and `--profile-for-pod` to resolve a profile
  from a pod name for use inside a log viewer.
- A k9s plugin in `contrib/k9s/plugin.yaml`.
- One example profile, written to the config directory on first run.
- `--version`.
