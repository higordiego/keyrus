package fixture

import "github.com/cucumber/godog"

func registerTrivialThen(ctx *godog.ScenarioContext) {
	ctx.Then(`^vazio$`, func() error { return nil })
}
