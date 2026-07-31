package fixture

import (
	"fmt"

	"github.com/cucumber/godog"
	externalsteps "github.com/higordiegoti/keyrus/test/bdd/testdata/guard/import-collision/external"
)

type localCollisionSteps struct {
	effects *int
}

func (steps localCollisionSteps) Colliding() error {
	*steps.effects++
	if *steps.effects != 1 {
		return fmt.Errorf("unexpected effects: %d", *steps.effects)
	}
	return nil
}

func registerImportedCollision(ctx *godog.ScenarioContext) {
	ctx.Step(`^selector importado$`, externalsteps.Colliding)
}
