package features_test

import (
	"testing"

	"github.com/cucumber/godog"
)

// TestFeatures runs the Gherkin scenarios in this directory (make bdd). The
// scenarios drive the use cases over the in-memory doubles; the HTTP and Kafka
// edges have their own tests.
func TestFeatures(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: initializeScenario,
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"."},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("non-zero status returned, failed to run the feature tests")
	}
}
