package fixture

import (
	"fmt"

	"github.com/cucumber/godog"
)

func registerRealBinding(ctx *godog.ScenarioContext, effects *int) {
	ctx.Step(`^um efeito observável$`, func() error {
		*effects++
		if *effects != 1 {
			return fmt.Errorf("unexpected effects: %d", *effects)
		}
		return nil
	})
}
