Feature: Ollama embeddings capture semantic similarity
  As a maintainer of the pipeline
  I want embeddings from a real model to behave correctly
  So that semantic similarity over vectorstore is trustworthy

  # @fake replays a committed cassette offline (pre-submit CI); @live-local
  # records/executes against a real local Ollama on a developer machine.
  @fake @live-local
  Scenario: Related texts score higher than unrelated ones
    Given an Ollama embedder for model "nomic-embed-text"
    When I embed the following texts:
      | id      | text                                |
      | cat     | The cat sat on the warm windowsill  |
      | kitten  | A kitten napped in the sunny window |
      | invoice | Quarterly tax invoice figures due   |
    Then the self-similarity of "cat" is approximately 1.0
    And "cat" is more similar to "kitten" than to "invoice"
