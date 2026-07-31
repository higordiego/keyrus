package fixture

import (
	"context"

	"github.com/cucumber/godog"
)

func registerTrivialContext(ctx *godog.ScenarioContext) {
	ctx.Step(`^contexto vazio$`, func(input context.Context) (context.Context, error) {
		return input, nil
	})
}
