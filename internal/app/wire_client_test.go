package app

import (
	"testing"

	"github.com/bnema/wlturbo"
)

// bindTestClient uses the generated real client proxy so context, version,
// event signatures and child lifetimes follow the dependency's contracts.
func bindTestClient(t *testing.T, c *wlturbo.Display, iface string, version uint32, p wlturbo.Proxy) {
	t.Helper()
	if _, err := c.Registry().BindNegotiated(iface, version, p); err != nil {
		t.Fatal(err)
	}
}
