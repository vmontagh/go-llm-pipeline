Feature: Semantic deduplication and clustering over real embeddings
  As a maintainer of the pipeline
  I want dedup and clustering to work on embeddings from a real model
  So that near-duplicate content is found and grouped with its payload intact

  # Paraphrase pairs score about 0.92 and unrelated pairs at most about 0.53
  # with nomic-embed-text, so 0.8 separates them with a wide margin.
  @fake @live-local
  Scenario: Paraphrases are flagged as duplicates and grouped by topic
    Given an Ollama embedder for model "nomic-embed-text"
    When I embed the following texts:
      | id     | text                                      |
      | cat1   | The cat sat on the warm windowsill        |
      | cat2   | A cat was sitting on the sunny windowsill |
      | tax1   | Quarterly tax invoice figures are due     |
      | tax2   | The quarterly tax invoice is due soon     |
      | rocket | The rocket launched toward orbit at dawn  |
    Then finding duplicates at threshold 0.8 reports exactly these pairs:
      | a    | b    |
      | cat1 | cat2 |
      | tax1 | tax2 |
    And clustering at threshold 0.8 yields exactly these groups:
      | members    |
      | cat1, cat2 |
      | rocket     |
      | tax1, tax2 |

  # @unix: sqlitestore refuses to open on platforms without its owner-only
  # file checks, so this scenario is skipped there.
  @fake @live-local @unix
  Scenario: Embeddings persisted to SQLite rank neighbors like the memory store
    Given an Ollama embedder for model "nomic-embed-text"
    When I embed the following texts:
      | id      | text                                |
      | cat     | The cat sat on the warm windowsill  |
      | kitten  | A kitten napped in the sunny window |
      | invoice | Quarterly tax invoice figures due   |
    And I save the embeddings to a SQLite store and reopen it
    Then the reopened store ranks neighbors of "cat" exactly like the memory store
    And the reopened store ranks neighbors of "invoice" exactly like the memory store
