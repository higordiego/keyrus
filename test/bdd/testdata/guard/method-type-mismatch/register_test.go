package fixture

import (
	"fmt"

	"github.com/cucumber/godog"
	"github.com/higordiegoti/keyrus/test/bdd/testdata/guard/method-type-mismatch/external"
)

type localSteps struct {
	effects *int
}

func (steps localSteps) Colliding() error {
	*steps.effects++
	if *steps.effects != 1 {
		return fmt.Errorf("unexpected effects: %d", *steps.effects)
	}
	return nil
}

func registerImportedReceiver(ctx *godog.ScenarioContext) {
	steps := external.Steps{}
	ctx.Step(`^método de tipo importado$`, steps.Colliding)
}
