package fixture

import (
	"fmt"

	"github.com/cucumber/godog"
)

func handler() error {
	return fmt.Errorf("top-level handler must not be inspected for a shadowing closure")
}

func registerShadowedHandler(ctx *godog.ScenarioContext) {
	handler := func() error { return nil }
	ctx.Step(`^closure sombreada$`, handler)
}
