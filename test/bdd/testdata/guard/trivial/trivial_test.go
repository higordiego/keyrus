package fixture

import "github.com/cucumber/godog"

func registerTrivial(ctx *godog.ScenarioContext) {
	ctx.Step(`^vazio$`, func() error {
		return nil
	})
}
