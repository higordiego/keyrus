package fixture

import "github.com/cucumber/godog"

func registerTrivialBlankAssignment(ctx *godog.ScenarioContext) {
	ctx.Step(`^atribuição vazia$`, func() error {
		_ = 1
		return nil
	})
}
