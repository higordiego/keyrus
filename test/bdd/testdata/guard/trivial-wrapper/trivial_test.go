package fixture

import "github.com/cucumber/godog"

func localNoop() error {
	return nil
}

func wrapperNoop() error {
	return localNoop()
}

func registerWrapperNoop(ctx *godog.ScenarioContext) {
	ctx.Step(`^wrapper sem efeito$`, wrapperNoop)
}
