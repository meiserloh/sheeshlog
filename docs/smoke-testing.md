# Smoke testing

Build once:

```sh
go build -o /tmp/sheesh ./cmd/sheesh
```

## Levels

```sh
/tmp/sheesh               < testdata/fixtures/levels.log  # default: info and up
/tmp/sheesh --level debug < testdata/fixtures/levels.log  # opens it back up
LEVEL=debug /tmp/sheesh   < testdata/fixtures/levels.log  # same, via env
LEVEL=debug /tmp/sheesh --level error < testdata/fixtures/levels.log  # flag wins
/tmp/sheesh --level loud  < testdata/fixtures/levels.log  # stderr + exit 2
```

## Streaming

Does it stream, or clump?

```sh
while true; do echo "{\"ts\":\"$(date -Is)\",\"level\":\"info\",\"msg\":\"tick\"}"; sleep 1; done | /tmp/sheesh
```

Lines should appear one per second.

## The remembered profile

Tip: Do not use your real config — point `XDG_CONFIG_HOME` somewhere disposable:

```sh
export XDG_CONFIG_HOME=$(mktemp -d)

/tmp/sheesh list          # writes the example on this first run, then: operator
/tmp/sheesh use operator  # now using profile "operator"
/tmp/sheesh list          # operator (current)
/tmp/sheesh --level trace < testdata/fixtures/operator.log  # renders with it, no flags
/tmp/sheesh use typo      # stderr + exit 2, nothing recorded
```

What to look for: after `use`, the bare pipe renders exactly as
`--profile operator` did, and a `--profile` run leaves
`$XDG_CONFIG_HOME/sheesh/current` alone.

## Against the real thing

```sh
kubectl logs -f deploy/<x> | /tmp/sheesh --level debug
```

And the k9s plugin shortcut on a pod that has no profile installed: it should
show the raw stream and say so, rather than rendering it with whatever profile
was last used.
