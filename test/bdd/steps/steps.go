// Package steps is the single registration point for implemented business
// bindings. T01 intentionally registers none.
package steps

import "github.com/cucumber/godog"

// Initialize registers only real bindings for tags listed in the manifest.
func Initialize(_ *godog.ScenarioContext) {}
