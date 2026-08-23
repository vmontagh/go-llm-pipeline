## `integ-test/` — BDD integration lanes

This package holds the [godog](https://github.com/cucumber/godog) BDD
integration suite. Scenarios live in `features/*.feature`; the active lane is
chosen by a build tag plus Gherkin tags. The suite is **test-only** — godog is
never imported by the shipped library, only by these tests.

| Lane         | Tag           | Build / runtime                                            | VCR mode                     | Where it runs     |
| ------------ | ------------- | --------------------------------------------------------- | ---------------------------- | ----------------- |
| Fake         | `@fake`       | `make test-bdd` (`go test ./integ-test/...`)              | replay committed cassettes   | pre-submit CI     |
| Live-local   | `@live-local` | `make test-bdd-live` (`go test -tags=live ...`)           | record against real Ollama   | developer machine |

### How the lanes work

Both lanes drive the **same** scenarios in-process. The step definitions build
an `ollama.Embedder` whose `*http.Client` is backed by a named YAML cassette via
`pkg/vcrtest`:

- **Fake** (default build): `pkg/vcrtest` replays
  `testdata/cassettes/<scenario>.yaml` with no network access, so `make test`
  is deterministic and offline. The embedder points at an unreachable
  placeholder host; the recorded interaction still matches because the default
  matcher tolerates a changed host.
- **Live-local** (`-tags=live`): the embedder points at a real Ollama
  (`OLLAMA_HOST`, default `http://localhost:11434`). `make test-bdd-live` first
  deletes the scenario cassettes so `VCR_MODE=record` (record-once) genuinely
  records live interactions rather than replaying an existing cassette. The
  recorded vectors are what the fake lane then replays, so the two lanes assert
  identical behaviour, and the target fails fast if Ollama is unreachable.

The outer `*testing.T` from `TestFeatures` is threaded into the scenario
initializer so steps can call the testing.T-aware `vcrtest.Client`.

### Cassettes

Cassettes are committed under `testdata/cassettes/`, one file per scenario
(named from the scenario title). Each holds a single `/api/embed` interaction —
the default matcher ignores request bodies, so a scenario that needs multiple
distinct calls should embed them in one batch (as the embeddings feature does).

To refresh a cassette after changing a scenario, re-record against a live
Ollama:

```sh
make test-bdd-live            # uses http://localhost:11434
OLLAMA_HOST=http://host:11434 make test-bdd-live
```

`make test-bdd-live` removes the scenario cassettes before recording, because
record-once leaves an existing cassette untouched; recording therefore always
hits the live model and fails fast if Ollama is unreachable. Commit the
refreshed cassettes afterwards so the fake lane can replay them. Recorded
requests are scrubbed of secrets before they touch disk; the Ollama embedding
endpoint carries none.

### First feature — Ollama embeddings

`features/embeddings.feature` embeds two related strings and one unrelated
string in a single batch, indexes the vectors in `pkg/vectorstore`, and asserts
via cosine similarity that a text's self-similarity is ≈ 1 (within float
tolerance) and that the related text outscores the unrelated one. This checks
real embedding correctness end-to-end — something unit tests with fake vectors
cannot.
