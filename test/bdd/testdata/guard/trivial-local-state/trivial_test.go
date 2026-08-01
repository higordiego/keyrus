package fixture

import (
	"fmt"

	"github.com/cucumber/godog"
)

func registerLocalState(ctx *godog.ScenarioContext) {
	ctx.Step(`^estado somente local$`, func() error {
		local := struct{ observed bool }{}
		local.observed = true
		values := []int{1}
		if !local.observed || len(values) != 1 {
			return fmt.Errorf("unreachable local assertion")
		}
		return nil
	})
}
