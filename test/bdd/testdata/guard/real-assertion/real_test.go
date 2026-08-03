package fixture

import (
	"fmt"

	"github.com/cucumber/godog"
)

func registerRealAssertion(ctx *godog.ScenarioContext) {
	ctx.Step(`^asserção sobre estado observado (\d+)$`, func(observed int) error {
		if observed != 1 {
			return fmt.Errorf("unexpected observed state: %d", observed)
		}
		return nil
	})
}
