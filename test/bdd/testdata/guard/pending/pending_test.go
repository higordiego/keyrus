package fixture

import "github.com/cucumber/godog"

func registerPending(ctx *godog.ScenarioContext) {
	ctx.Step(`^pendente$`, func() error {
		return godog.ErrPending
	})
}
