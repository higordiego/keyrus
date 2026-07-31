package fixture

import "testing"

func skipFixture(t *testing.T) {
	t.Skip("silent success is forbidden")
}
