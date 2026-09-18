# Profiles describe a log style, not an output layout

A profile tells sheeshlog *how to read* one style of structured logging — which dotted paths hold time, level, message and subject, which fields are context fields, and which noise rules discard a line. It deliberately does **not** control how the rendered line looks: the layout, the colours and the ordering of slots are fixed in code and identical for every profile.

(This ADR was written when "format" was the everyday word for a log style, and its file name still says so. **Format** has since become a defined term meaning something narrower — how a payload is decoded into addressable fields — and a profile *does* state that, in its `format:` key; see [ADR-0002](0002-format-is-declared-prefix-is-detected.md). This ADR is about the log style as a whole. The file name is left as it is, because an ADR's name is its identifier.)

The alternative was a per-profile template DSL. We rejected it because the fixed layout is the part of the original `oplog` script we set out to preserve, and because a template language becomes a public contract the moment it ships — one we would have to design, document and support before the tool has done its job once. Templating remains additive later; a badly-shaped DSL would not be.

## Consequences

Two profiles for two log styles produce visually identical output, which is the point: the eye learns one shape. Anyone wanting different colours or a different column order has no escape hatch in v1 and must patch the binary.
