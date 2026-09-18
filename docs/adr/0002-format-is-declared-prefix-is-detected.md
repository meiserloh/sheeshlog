# The format is declared, the stream prefix is detected

A **profile** states its stream's **format** — the key `format:`, `json` or
`logfmt`, defaulting to `json`. The parser only ever attempts that one format,
and never asks whether a line is JSON. The **stream prefix**, in contrast, is
not stated anywhere: it is found by trying 0, 1 and 2 leading
whitespace-separated tokens and taking the first count for which the *whole*
remainder decodes in the declared format. A stripped token must contain no
whitespace, `=`, `"` or `{`. A line for which no token count yields a payload is
written out verbatim.

These are two decisions, and they only work together — which is why they are one
ADR. Knowing the format is what makes "does the remainder parse?" a question
with a trustworthy answer, and that question is the whole of the prefix
detection.

## Why the format is declared

The alternative is per-line sniffing: try each format the tool knows and take
whichever succeeds. We rejected it.

The user has already chosen the profile. Sniffing would reintroduce, on every
single line, an ambiguity that the choice of profile had just resolved — a
self-inflicted one. And the failure mode is bad in a way that is hard to notice:
a line read as the wrong format renders with plausible-looking slots filled from
the wrong places, and nothing about the output says so. Worse, it is not fixable
by the person who sees it, because there is no key to edit; the fix would be a
change to the sniffing heuristic, in the tool, for everyone.

With the format declared, a line sheesh reads wrongly is a line the reader can
fix by editing one key in their own profile.

The cost is a key that nearly every profile leaves at its default, and a second
thing a profile author can get wrong. Both are paid for by the same rule the
`timeformat` key already follows: an unknown name is refused when the profile is
loaded, by name, listing what was expected.

## Why the prefix is detected

The mirror-image alternative is a per-profile key — `prefix: stern`, or a token
count. We rejected that too.

The prefix belongs to the *viewing tool*, not to the log style. The same
operator's logs arrive bare when a container is read directly, as one token
through k9s on a multi-container pod, as two through stern, and as one bracketed
token through `kubectl logs --prefix`. Declaring it would mean one profile per
tool for every stream — four profiles saying the same thing about a log style
and differing only in how somebody happened to open it. The profile would be
recording a fact about the reader's terminal.

So the prefix is detected. What makes that safe is the requirement that the
*whole* remainder parse, rather than that a payload be found somewhere in the
line. A parser that took everything before the first `{` as a prefix — which is
what sheesh did before this decision — discarded the real timestamp, level and
message of every positional line that merely *ends* in a JSON object: zap
console renders as its JSON context alone, and a klog line ending in `{}`
renders as nothing at all. Requiring the whole remainder is what tells those
apart from a genuinely prefixed record.

The token rules are the second half of that safety. Two tokens is the cap
because it covers every viewing tool in use, and going further would start
eating the leading fields of positional formats, whose first two tokens are
typically a timestamp and a level. The character rules are the same argument at
the token level: `=`, `"` and `{` belong to the payload of some format, so a
token holding one is a field, and stripping it would be reading data as a pod
name.

## What logfmt asks of the prefix rule

The prefix detection above rests on "does the whole remainder decode?" being a
question with a sharp answer. For JSON it is: a payload opens with `{`. For
logfmt it is not, because a bare word is a legal logfmt field — so, read
literally, every line decodes, no line ever fails, and the prefix could never be
found. `velero time="..." level=info msg="..."` would read at nought tokens with
`velero` as a field holding an empty value, and the one-token prefixes of
velero, grafana and prometheus would vanish into the payload they precede.

So a logfmt payload must *open* with an assignment. After the first token a bare
word is what the format says it is: a field with an empty value. That one rule
is the whole of the difference, and it puts logfmt's answer back on the same
footing as JSON's — sharp enough for the token loop to trust.

It has the same shape of cost as the rest of the heuristic: a genuinely
unprefixed logfmt line that happens to open with a bare word loses that word to
the prefix slot. It stays addressable as `_prefix`, and `check` counts it, so
the mistake is visible rather than silent.

## Consequences

Stern-prefixed, k9s-prefixed and unprefixed lines of one stream render
identically under one profile, which is the point.

A stream whose format is neither `json` nor `logfmt` renders nothing until that
format is read; a profile naming one is refused at load rather than passing
every line through verbatim, which would look exactly like a stream sheesh
cannot help with.

Prefix detection is a heuristic, and it can still be wrong. A positional record
whose first two tokens are both name-shaped and which ends in a bare JSON object
would be read as a prefixed record. No line in the corpus is that shape, and the
cost of being wrong is one line rendered oddly rather than a stream lost — but
it is a heuristic, and the corpus is the only thing keeping it honest. The
unsupported formats are therefore held as fixtures and asserted to pass through
byte for byte.
