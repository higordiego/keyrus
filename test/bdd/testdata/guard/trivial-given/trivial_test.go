package fixture

import "github.com/cucumber/godog"

func registerTrivialGiven(ctx *godog.ScenarioContext) {
	ctx.Given(`^vazio$`, func() error { return nil })
}
