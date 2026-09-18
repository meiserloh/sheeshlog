# Testing

`go test ./...` runs the tests. `Run` takes its argv, streams and environment as
arguments, so a test is a function call.

Most tests assert inline. The rest assert against a **golden file** that can be found
under `testdata/golden/`.

## Golden files

A golden test runs the CLI over a fixture and compares the whole output —
stdout, then stderr, then the exit code — against a committed file under
`testdata/golden/`. That file *is* the expected output; there is no assertion
to read.

This suits sheesh because the thing under test is a rendered layout: columns,
padding, colour, what got suppressed. Asserting those field by field would
restate the renderer in the test and still miss the alignment. A whole-output
diff catches everything, including the changes nobody thought to assert.

The cost is that the expectation is a file, not a claim. The fixtures carry the
intent instead — one log style each, named for it — so a golden is only ever
read next to the fixture that produced it.

## Updating them

```sh
go test . -update
```

`-update` is this repository's own flag (defined in `run_test.go`), not a Go feature.
It **overwrites every golden with the current output**, which makes the suite
pass by definition.

So it is **never the fix for a failing test(!)**. It is the second step of a deliberate
change:

1. Change the code or the fixtures. Watch the goldens fail.
2. Run `go test . -update`.
3. **Read `git diff testdata/golden/`.** Every changed line, deliberately. This
   is the only review the renderer gets — an unread diff is an accepted
   regression.

Never hand-edit a golden file. A hand-edited golden asserts what someone
believed the output was, which is exactly the bug a golden test exists to catch.

> `go test ./... -update` does not work: `-update` is declared only in the root
> package, so the `internal/…` packages abort on an undefined flag. Use
> `go test . -update`.
