// Package integtest is the test-only godog BDD integration suite for the
// Ollama embeddings feature. It runs the Gherkin specs under features/ in two
// lanes selected by build tag: the default build is @fake, which replays
// pkg/vcrtest cassettes fully offline (safe for pre-submit CI), while -tags=live
// is @live-local, which records/executes against a real local Ollama on a
// developer machine.
package integtest

import (
	"flag"
	"testing"

	"github.com/cucumber/godog"
)

// opts holds the godog suite options. Tags default to the current lane
// (see config_fake_test.go / config_live_test.go) and can be overridden on the
// command line with -godog.tags.
var opts = godog.Options{
	Format: "pretty",
	Paths:  []string{"features"},
	Tags:   defaultGodogTags,
}

func init() {
	godog.BindFlags("godog.", flag.CommandLine, &opts)
}

// TestFeatures runs the Gherkin features under features/ through godog. The
// active lane is chosen by build tag: the default build replays cassettes
// offline (@fake), while -tags=live records/executes against a real Ollama
// (@live-local). The outer *testing.T is threaded into the scenario
// initializer so step definitions can build cassette-backed HTTP clients with
// pkg/vcrtest.
func TestFeatures(t *testing.T) {
	if !isFakeMode && testing.Short() {
		t.Skip("skipping live BDD lane in short mode")
	}
	flag.Parse()
	opts.TestingT = t

	suite := godog.TestSuite{
		Name:                "ollama-embeddings",
		ScenarioInitializer: func(sc *godog.ScenarioContext) { initializeScenario(t, sc) },
		Options:             &opts,
	}
	if suite.Run() != 0 {
		t.Fatal("godog suite reported failures")
	}
}
