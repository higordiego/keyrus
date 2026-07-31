package fixture

import "github.com/cucumber/godog"

func registerTrivialWhen(ctx *godog.ScenarioContext) {
	ctx.When(`^vazio$`, func() error { return nil })
}
