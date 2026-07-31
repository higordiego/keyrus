package fixture

import (
	"context"

	"github.com/cucumber/godog"
)

func registerTrivialBackground(ctx *godog.ScenarioContext) {
	ctx.Step(`^contexto novo sem efeito$`, func() (context.Context, error) {
		return context.Background(), nil
	})
}
