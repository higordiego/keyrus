package fixture

import "github.com/cucumber/godog"

type trivialSteps struct{}

func (trivialSteps) noop() error {
	return nil
}

func registerTrivialMethod(ctx *godog.ScenarioContext) {
	steps := trivialSteps{}
	ctx.Step(`^método vazio$`, steps.noop)
}
