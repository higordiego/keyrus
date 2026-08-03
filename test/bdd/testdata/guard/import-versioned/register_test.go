package fixture

import (
	"fmt"

	"github.com/cucumber/godog"
	"github.com/higordiegoti/keyrus/test/bdd/testdata/guard/import-versioned/versioned/v2"
)

type localVersionedCollision struct {
	effects *int
}

func (steps localVersionedCollision) Colliding() error {
	*steps.effects++
	if *steps.effects != 1 {
		return fmt.Errorf("unexpected effects: %d", *steps.effects)
	}
	return nil
}

func registerVersionedImport(ctx *godog.ScenarioContext) {
	ctx.Step(`^selector importado sem alias$`, external.Colliding)
}
