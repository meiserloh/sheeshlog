# sheeshlog

A command-line tool that renders structured log lines into a compact, human-readable form. It reads log lines on stdin and prints them using a named profile chosen by the user.

## Language

**Profile**:
A named recipe describing how one style of structured logging is read — which fields carry the time, level, message and subject, how the time value is to be read, which fields are context, and which lines are noise.
_Avoid_: configuration, format, preset, template

**Subject**:
The single identifier a human scans for to know *what* a line is about, taken from the first field present in the profile's ordered list of candidates.
_Avoid_: name, entity, resource

**Context field**:
A field whose value repeats on nearly every line of a stream and is therefore suppressed from the rendered output.
_Avoid_: boilerplate, metadata

**Extras**:
The fields of a log line that are neither rendered in a dedicated slot nor suppressed as context fields; they are appended to the line as `key=value` pairs.
_Avoid_: rest, leftovers, attributes

**Noise rule**:
A rule in a profile that discards a whole log line as uninteresting to a human, independent of its level.
_Avoid_: filter, boring, exclusion

**Payload**:
The part of a line that carries the structured fields, once any stream prefix has been removed. What a Format decodes.
_Avoid_: body, content, rest

**Format**:
How a payload is decoded into addressable fields — JSON or logfmt. A profile states its stream's format; a Format says nothing about which field means what, which is the Profile's job.
_Avoid_: parser, syntax, encoding

**Stream prefix**:
The tokens a log-viewing tool puts ahead of the payload to say which pod or container a line came from — one token for k9s, two for stern, none when reading a container directly. It belongs to the tool, not the log style, so it is detected rather than declared, and is exposed to a profile as the reserved field `_prefix`.
_Avoid_: pod prefix, tag
