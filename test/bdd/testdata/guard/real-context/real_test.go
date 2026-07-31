package fixture

import (
	"context"
	"fmt"

	"github.com/cucumber/godog"
)

func registerRealContext(ctx *godog.ScenarioContext, effects *int) {
	ctx.Step(`^contexto com efeito$`, func(input context.Context) (context.Context, error) {
		*effects++
		if *effects != 1 {
			return input, fmt.Errorf("unexpected effects: %d", *effects)
		}
		return input, nil
	})
}
