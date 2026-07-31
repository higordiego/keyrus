package fixture

import "github.com/cucumber/godog"

func registerTrivialErrorConversion(ctx *godog.ScenarioContext) {
	ctx.Step(`^conversão vazia$`, func() error {
		return error(nil)
	})
}
