package fixture

import "github.com/cucumber/godog"

func registerRealMethod(ctx *godog.ScenarioContext, effects *int) {
	steps := observableSteps{effects: effects}
	ctx.Step(`^método observável$`, steps.apply)
}
