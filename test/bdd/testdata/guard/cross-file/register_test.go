package fixture

import "github.com/cucumber/godog"

func registerCrossFile(ctx *godog.ScenarioContext) {
	ctx.Step(`^cross-file vazio$`, noopAcrossFiles)
}
