# Writing a profile

A **profile** is a named recipe describing how one style of structured logging is
read: which fields carry the time, level, message and **subject**, how the time
value is to be read, which fields are **context fields**, and which lines a
**noise rule** discards. One file per profile, so handing a colleague a profile
is a copy.

Profiles live in `$XDG_CONFIG_HOME/sheesh/profiles/` (`~/.config/sheesh/profiles/`
if `XDG_CONFIG_HOME` is unset). **The file name is the profile name**, so
`backup.yaml` is the profile `backup`; there is no `name:` key for it to drift
from.

sheesh writes an example profile there on its first run, as a profile that works
and as the thing to copy.

## What a profile cannot do

A profile says how to *read* a log style. It never says how the output looks.
The layout, the colours and the order of the slots are fixed in code and
identical for every profile — see
[ADR-0001](adr/0001-profiles-describe-log-style-not-layout.md), which sets out why,
and what it buys.

## Naming a profile after its workload

The file name is free, but there is one convention worth following: **name the
profile after the Kubernetes workload whose logs it reads** — `k8s-loki`,
`velero`, `my-operator-controller-manager` — rather than after a pod, a
ReplicaSet or the log style.

That is what `--profile-for-pod` resolves against. Given a pod name it tries
three candidates, most specific first:

1. the pod name as given;
2. the name with its last hyphen-delimited suffix removed;
3. the name with its last two removed.

The first one with an installed profile wins. Two removals is the deepest shape
Kubernetes produces — a Deployment's ReplicaSet hash plus the pod's own suffix —
so the same three candidates also cover a DaemonSet's single generated suffix
and a StatefulSet's ordinal:

| Pod                                               | Profile it finds                 |
|---------------------------------------------------|----------------------------------|
| `my-operator-controller-manager-7c45d48bb4-cgkp2` | `my-operator-controller-manager` |
| `k8s-loki-canary-wq7zz`                           | `k8s-loki-canary`                |
| `k8s-loki-0`                                      | `k8s-loki`                       |
| `velero`                                          | `velero`                         |

With no profile for any candidate, the stream is passed through byte for byte
and one line on stderr names what was tried. It is deliberately **not** rendered
with the remembered profile: that is some other service's reading recipe, and a
confident-looking wrong reading is worse than a raw one. `--profile-for-pod`
never changes what is remembered either, so it is safe to fire on any pod.

### The k9s plugin

[`contrib/k9s/plugin.yaml`](../contrib/k9s/plugin.yaml) binds this to a key, so
that reading a service means pressing it on the pod rather than leaving k9s to
change the remembered profile and coming back. Merge it into
`~/.config/k9s/plugins.yaml`; the file has the installation notes.

It needs [`ov`](https://github.com/noborus/ov) alongside `sheesh`, as the pager
the logs open in — `q` returns to k9s, `/` searches, `F` toggles following the
tail.

The profile is resolved from the *pod*, in both the pod view and the container
view. A sidecar logging in a different style from its workload is therefore read
with the workload's profile — its lines will usually fail to parse and pass
through unchanged, which is the intended failure and not something a profile can
currently fix.

## A complete profile

Every option, in one file. Copy it to
`~/.config/sheesh/profiles/<name>.yaml` and edit the paths.

```yaml
# How this stream's payload is decoded into fields. Omitted, it is `json`.
# It is spelled out here only because this profile is meant to show every key.
# One of: json, logfmt.
format: json

# The three fixed slots, as dotted paths into the parsed line.
time: ts
level: level
message: msg

# How the time value is read. Omitted, it is detected from the value, which is
# what nearly every profile should do — including, honestly, this one: detection
# reads these timestamps correctly. It is spelled out here only because this
# profile is meant to show every key.
#
# State it when detection would have to guess: epoch seconds and epoch
# milliseconds are told apart by magnitude, so a style logging instants far from
# the present day is the case that needs telling.
# One of: auto, rfc3339, epoch, epochmillis.
timeformat: rfc3339

# Subject candidates, tried in the order written. The first one present fills the
# subject slot, so a Backup line and a Restore line each show the thing they are
# about. `_prefix` last means a line with neither still shows which pod it came
# from instead of a bare `-`.
subject:
  - Backup.name
  - Restore.name
  - _prefix

# The context fields: those whose value repeats on nearly every line and so
# distinguish nothing. Top-level field names, not dotted paths.
context:
  - logger
  - controller
  - namespace
  - reconcileID
  - Backup
  - Restore

# The noise rules. A line whose named field matches is discarded whole, whatever
# its level. One shape only: a dotted-path field and a regex.
noise:
  - {field: msg, matches: "^(Starting|Serving|Stopping)"}
  - {field: logger, matches: "^controller-runtime"}
  - {field: _prefix, matches: "istio-proxy$"}
```

Unknown keys are refused rather than ignored: a typo that silently does nothing
is the failure this tool exists to stop being silent about.

Make it the current profile with `sheesh use backup`, or use it for one run with
`sheesh --profile backup`.

## Dotted paths

`time`, `level`, `message`, every `subject` candidate and every noise rule's
`field` are dotted paths into the parsed line. A bare name is a top-level field;
a dot walks into a nested object.

Given this line:

```json
{"ts":"2026-08-21T05:03:44Z","level":"info","msg":"backup completed",
 "Backup":{"name":"backup-20260821-0503"},"duration":"31.8s"}
```

* `msg` resolves to `backup completed`
* `Backup.name` resolves to `backup-20260821-0503`
* `Backup` resolves to the whole object, rendered as compact JSON
* `Backup.title` resolves to nothing

A path that is missing, or that lands on a non-object part way down, leaves its
slot **empty** rather than erroring — the rest of the line still renders. That is
what makes a misspelled path so easy to miss, and why `check` exists.

## The format

`format:` says how a line's **payload** is decoded into the fields the paths
above address. There are two values, `json` and `logfmt`, and `json` is the
default, so a profile that says nothing about its format is saying `json`. A
name sheesh does not read is refused when the profile loads, by name — the same
way an unknown `timeformat` is. The format is **declared, never guessed**.

### logfmt

`format: logfmt` reads `key=value` pairs separated by whitespace, values
optionally quoted:

```
velero time="2026-09-02T11:00:44Z" level=info msg="Validating BackupStorageLocation" backup-storage-location=platform/default
```

```yaml
format: logfmt
time: time
level: level
message: msg
subject:
  - name
  - _prefix
context:
  - controller
  - logSource
```

```
11:00:44 I velero  Validating BackupStorageLocation  backup-storage-location=platform/default
```

Everything above this section applies unchanged: the same fixed layout, the same
dotted paths, the same context deny-list, the same noise rules, the same
detected stream prefix — `velero` here is a one-token prefix, and it fills the
subject slot because no `name` field is present.

**Every value is a string.** logfmt has no types, so `attempts=1` is the string
`1`. Nothing in a profile is worse off for it: the time and level readers accept
their string spellings, and paths address fields by name rather than by type.

**Quoted values are unquoted Go-style.** `\n`, `\t`, `\"` and `\\` become the
characters they stand for, which is what makes a JSON object carried as a
field's value arrive readable:

```
object="{\"name\":\"web-1\",\"namespace\":\"platform\"}"
```

resolves `object` to `{"name":"web-1","namespace":"platform"}`. An escaped
newline becomes a real one, exactly as it does in a JSON payload, so a value
holding one renders across several terminal lines.

**An unquoted value runs to the next whitespace**, so an `=` inside one is part
of it: `query=a=b&c=d` resolves `query` to `a=b&c=d`. `key=` with nothing after
it is a field with an empty value.

**A duplicate key is last-wins.** `msg=first msg=second` resolves `msg` to
`second`.

**A bare word among assignments is a field with an empty value** — but a payload
must *open* with an assignment, or it is not a record at all. That second half is
what lets the stream prefix still be found: without it `velero time=...` would
read as a field called `velero`, and the prefix would vanish into the payload.

**A malformed quoted value fails the whole line** to verbatim passthrough: an
unterminated quote, a trailing backslash, an escape that is not one (`\q`), or a
closing quote butted against something other than whitespace (`k="a"b`, which is
neither one value nor two). Half a record read is worse than none: it would
render with plausible slots and nothing would say which half was lost.

#### What the time field is called

logfmt fixes no field names, so loggers disagree about what to call the time.
Three spellings cover most of what you will meet:

- `time: time`   # velero, Prometheus, anything logging through logrus
- `time: ts`     # Loki, go-kit
- `time: t`      # Grafana

Name whichever one your stream writes — and if it writes `timestamp` or
`@timestamp`, name that instead. There is no candidate list here and none is
needed: a stream writes one spelling, and `check` tells you if you picked the
wrong one.

All three above carry an RFC3339 timestamp, so `timeformat` can stay unstated
and be detected.

The two ways to get it wrong look different in `check`, and the difference is
which line of the profile to go and edit:

* `time  time 0  (N empty)` — the path found nothing, so the **name** is wrong.
  Check **fields seen** for the spelling this stream actually writes.
* `time  time 0  (N not read as a time)` — the path found a value and it could
  not be read, so the **`timeformat`** is wrong, not the name.

### The stream prefix

The tokens a log-viewing tool puts ahead of the payload to say which pod or
container a line came from are split off before decoding and exposed as the
reserved field `_prefix`. It is a path like any other, so it can fill the
subject slot, or be matched by a noise rule to drop a sidecar's lines. It only
exists on lines that had a prefix.

There is **no key for the prefix**, because it belongs to the viewing tool and
not to the log style: the same stream arrives bare when you read a container
directly, as one token through k9s on a multi-container pod, as two through
stern, and as one bracketed token through `kubectl logs --prefix`. Declaring it
would mean one profile per tool. So it is detected: sheesh tries 0, 1 and 2
leading tokens and keeps the first count for which *everything after them*
decodes in the declared format. Nothing decodes at any count ⇒ the line is
written out exactly as it came in.

That "everything after them" is why a positional line which merely *ends* in a
JSON object — zap console, klog — passes through whole instead of being rendered
as its JSON tail with the real timestamp, level and message thrown away.

## Subject candidate ordering

The candidates are tried **in the order written** and the first one *present*
wins. Order them most specific first:

```yaml
subject:
  - Backup.name     # a Backup line shows the backup
  - Restore.name    # a Restore line shows the restore
  - _prefix         # anything else shows where it came from
```

Reverse those and `_prefix` would win on every prefixed line, because it is
present on every prefixed line — the later candidates would never be reached.
You can see such a behaviour with the `check`-subcommand: it prints the candidates
in your order with a count each, so a first candidate at 0 and a last one at 40 says 
the order is wrong.

When no candidate is present the slot renders as `-`, so the line still reads as
a line with a subject that happens to be missing.

## The context deny-list

`context` is a **deny-list**, not an allow-list. Fields on it are suppressed from
the **extras**; every field that is not spoken for some other way is appended to
the rendered line as `key=value`.

With `namespace` on the list, this line:

```json
{"level":"warn","ts":"2026-08-21T05:05:00Z","msg":"velero backup still pending","namespace":"platform"}
```

renders as:

```
05:05:00 W backup-operator-6d9f manager  velero backup still pending
```

Take `namespace` off the list and the same line renders as:

```
05:05:00 W backup-operator-6d9f manager  velero backup still pending  namespace=platform
```

Nothing else changed. That is the whole of what the list does: a listed field is
suppressed, an unlisted one is an extra.

This is deliberate: a field the profile has never heard of still shows up, which
is how new information gets noticed. A short `context` list is safe — the output
is just noisier.

Two rules to keep in mind:

* **Top-level names only.** A dotted path here is refused at load time. Extras
  render a top-level value whole, so there is no half of one to suppress: listing
  `Backup` suppresses the whole object, and `Backup.namespace` is not a thing you
  can suppress on its own.
* **A field filling a slot does not need listing.** It is spoken for either way.
  Only fields that would otherwise land in the extras belong here. The time field
  is the one exception: it is claimed only if its value could be *read* as a time,
  so a `ts` that fails its `timeformat` leaves the slot empty and turns up in the
  extras rather than vanishing. That is on purpose — a value that is not a time is
  still information — and it is a second way the same mistake becomes visible.

## Noise rules

A noise rule discards a whole line as uninteresting to a human, **independent of
its level** — startup chatter is not made interesting by being logged as a
warning. It is one shape only, a field and a regex:

```yaml
noise:
  - {field: msg, matches: "^(Starting|Serving|Stopping)"}
```

The `field` is a dotted path, so a nested field and `_prefix` are both matchable.
A field a line does not have simply does not match, so a rule costs nothing on
the lines it was not written for. The regex is Go's `regexp` syntax and is
compiled when the profile loads, so a broken one is a start-up failure rather
than something discovered halfway down a tail.

A line is counted against the *first* rule that matched it, so a rule reporting 0
in `check` may be correct and merely shadowed by an earlier one.

## Debugging a profile with `check`

`sheesh check <name> < sample.log` reports what a profile *did* to a sample and
renders none of it. It makes the same decisions a render makes, in the same
order — parse, prefix, noise, slots, level — so the numbers are the render's
numbers. `--level` applies, because the threshold is part of what a render would
do.

Capture a sample once and keep it:

```sh
kubectl logs deploy/backup-operator --prefix --tail=200 > sample.log
sheesh --level debug check backup < sample.log
```

For the profile above, against the sample at the bottom of this page:

```
profile backup, threshold debug
sample 8 lines: 1 passed through, 3 noise, 0 below debug, 4 rendered

slots (of 4 lines reaching them)
  time     ts 4
  level    level 4
  message  msg 4
  subject  Backup.name 2, Restore.name 1, _prefix 1

noise dropped
  msg      ^(Starting|Serving|Stopping)  2
  logger   ^controller-runtime           0
  _prefix  istio-proxy$                  1

context suppressed (of the same 4 lines)
  logger       0
  controller   1
  namespace    2
  reconcileID  1
  Backup       2
  Restore      1

fields seen (of 7 lines parsed)
  Backup       2
  Restore      1
  _prefix      7
  ...
```

How to read it:

* **The summary** accounts for every line. *Passed through* are the lines the
  declared format does not read: they are not dropped, the renderer writes them
  out verbatim, so they are output the profile had no part in.
* **Slots** counts, per declared path, how often it filled its slot, in your own
  order. Every declared path is listed even at 0 — that is what a misspelled path
  looks like. A trailing `(N empty)` counts the lines where no path filled the
  slot at all.
* **Noise dropped** is one row per rule, in file order, with the lines it
  actually discarded. `logger ^controller-runtime 0` here is shadowed, not
  wrong: the one `controller-runtime` line was already dropped by the `msg` rule
  above it.
* **Context suppressed** is one row per listed field. A field at 0 either never
  appears in this log style or is misspelled.
* **Fields seen** is every field on every parsed line, including lines dropped as
  noise. This is the list to write your `context` and `subject` entries *from*,
  rather than from memory.

### A wrong path

Suppose `message: message` and a subject candidate of `backup.name` — the field
is `Backup`, capitalised. Nothing errors; lines render with a blank message and
the wrong subject. `check` says so:

```
slots (of 4 lines reaching them)
  time     ts 4
  level    level 4
  message  message 0  (4 empty)
  subject  backup.name 0, Restore.name 1, _prefix 3
```

`message 0 (4 empty)` — the path was declared, and it filled the slot on none of
the four lines that reached it. `backup.name 0` with `_prefix 3` behind it is the
same failure in the candidate list: the specific candidate never matched, so the
catch-all took over. Compare against **fields seen** to find the real spelling.

A time slot has a third outcome of its own:

```
  time     ts 0  (4 not read as a time)
```

The path is right and the value was found; it could not be read as the stated
format. That is the `timeformat` line to go and edit, not the `time` line.

## The sample used above

```
backup-operator-6d9f manager {"level":"info","ts":"2026-08-21T04:58:04Z","logger":"config","msg":"Starting in development mode!"}
backup-operator-6d9f manager {"level":"debug","ts":"2026-08-21T05:03:12Z","msg":"reconciling backup","controller":"backup","Backup":{"name":"backup-20260821-0503","namespace":"platform"},"namespace":"platform","reconcileID":"9f3c1b20"}
backup-operator-6d9f manager {"level":"info","ts":"2026-08-21T05:03:44Z","msg":"backup completed","Backup":{"name":"backup-20260821-0503"},"duration":"31.8s"}
backup-operator-6d9f manager {"level":"error","ts":"2026-08-21T05:04:02Z","msg":"restore failed","Restore":{"name":"restore-20260821-0504"},"error":"velero: timeout waiting for volume"}
backup-operator-6d9f manager {"level":"info","ts":"2026-08-21T05:04:10Z","logger":"controller-runtime.metrics","msg":"Serving metrics server"}
backup-operator-6d9f istio-proxy {"level":"info","ts":"2026-08-21T05:04:11Z","msg":"envoy proxy is ready"}
backup-operator-6d9f manager {"level":"warn","ts":"2026-08-21T05:05:00Z","msg":"velero backup still pending","namespace":"platform"}
backup-operator-6d9f default-schedule-cr CR 'nightly-schedule' already exists, skipping creation
```

Rendered with the profile above, at `--level debug`:

```
05:03:12 D backup-20260821-0503  reconciling backup
05:03:44 I backup-20260821-0503  backup completed  duration=31.8s
05:04:02 E restore-20260821-0504  restore failed  error=velero: timeout waiting for volume
05:05:00 W backup-operator-6d9f manager  velero backup still pending
backup-operator-6d9f default-schedule-cr CR 'nightly-schedule' already exists, skipping creation
```

Three noise rules dropped the startup, metrics and sidecar lines. `duration` and
`error` are extras — neither slotted nor listed as context — so they are appended
as `key=value`. `logger`, `controller`, `namespace`, `reconcileID`, `Backup` and
`Restore` are suppressed. The last line is not JSON, so it passed through
verbatim.
