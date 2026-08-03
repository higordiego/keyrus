// Package bddrunner applies non-vacuous execution policy around Godog.
package bddrunner

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/cucumber/godog"
)

// Config describes an explicitly selected suite. Tags must come from the
// validated implemented-scenario manifest.
type Config struct {
	Name            string
	Paths           []string
	FeatureContents []godog.Feature
	Tags            []string
	Initialize      func(*godog.ScenarioContext)
	Randomize       int64
}

// Run fails on zero selected scenarios and on failed, undefined, pending,
// ambiguous or skipped steps. Godog strict mode alone does not reject ErrSkip,
// therefore the status observer is an intentional second guard.
func Run(config Config) error {
	if len(config.Tags) == 0 {
		return fmt.Errorf("implemented scenario selection is empty")
	}

	var mu sync.Mutex
	executed := 0
	violations := make(map[godog.StepResultStatus]int)
	initializer := func(ctx *godog.ScenarioContext) {
		if config.Initialize != nil {
			config.Initialize(ctx)
		}
		ctx.Before(func(hookContext context.Context, _ *godog.Scenario) (context.Context, error) {
			mu.Lock()
			executed++
			mu.Unlock()
			return hookContext, nil
		})
		ctx.StepContext().After(func(hookContext context.Context, _ *godog.Step, status godog.StepResultStatus, _ error) (context.Context, error) {
			switch status {
			case godog.StepFailed, godog.StepUndefined, godog.StepPending, godog.StepAmbiguous, godog.StepSkipped:
				mu.Lock()
				violations[status]++
				mu.Unlock()
			}
			return hookContext, nil
		})
	}

	name := config.Name
	if name == "" {
		name = "implemented-bdd"
	}
	// Godog v0.15.1 uses the legacy Behat filter syntax: comma is OR.
	// The words "or" and parentheses are treated as part of a literal tag.
	tagExpression := strings.Join(config.Tags, ",")
	status := godog.TestSuite{
		Name:                name,
		ScenarioInitializer: initializer,
		Options: &godog.Options{
			Format:          "progress",
			NoColors:        true,
			Strict:          true,
			Tags:            tagExpression,
			Paths:           config.Paths,
			FeatureContents: config.FeatureContents,
			Randomize:       config.Randomize,
			Output:          os.Stdout,
		},
	}.Run()

	mu.Lock()
	defer mu.Unlock()
	if executed == 0 {
		return fmt.Errorf("implemented manifest selected zero Godog scenarios")
	}
	if len(violations) > 0 {
		return fmt.Errorf("Godog rejected step statuses: %v", violations)
	}
	if status != 0 {
		return fmt.Errorf("Godog suite returned status %d", status)
	}
	return nil
}
