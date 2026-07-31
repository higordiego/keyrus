package fixture

import "fmt"

type observableSteps struct {
	effects *int
}

func (steps observableSteps) apply() error {
	*steps.effects++
	if *steps.effects != 1 {
		return fmt.Errorf("unexpected effects: %d", *steps.effects)
	}
	return nil
}
