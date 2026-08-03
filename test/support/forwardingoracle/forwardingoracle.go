// Package forwardingoracle decides whether the edge forwarded a call it should
// have rejected. It is a pure function over two counter readings so the
// decision itself -- not just a favorable run against the real stack -- can be
// exercised with a synthetic mutation that must be rejected.
package forwardingoracle

import "fmt"

// AssertNotForwarded compares the adapter's pre-authentication entrypoint
// counter before and after a call the edge should have rejected on its own.
// That counter advances for every request that reaches the adapter's HTTP
// listener, regardless of whether the adapter's own auth middleware then
// accepts or rejects it -- unlike a counter placed after authentication, which
// cannot distinguish "the edge never forwarded" from "the edge forwarded and
// the adapter's defense in depth caught it." An unchanged entrypoint counter is
// therefore proof the edge itself withheld the call.
func AssertNotForwarded(before, after uint64) error {
	if after != before {
		return fmt.Errorf("forwardingoracle: edge forwarded the call it should have rejected: "+
			"adapter entrypoint counter moved from %d to %d", before, after)
	}
	return nil
}
