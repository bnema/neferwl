package core

import (
	"slices"
	"testing"
	"testing/synctest"

	"github.com/stretchr/testify/mock"
)

func TestStatelessLayoutBuildsStateOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w, l := dispatchLayout(t, 3)
		// dispatchLayout allows NewState at will; this test counts it.
		l.Mock.ExpectedCalls = slices.DeleteFunc(l.Mock.ExpectedCalls, func(c *mock.Call) bool { return c.Method == "NewState" })
		l.EXPECT().NewState().Return(nil).Once()
		for range 3 {
			w.Snapshot()
		}
		l.AssertNumberOfCalls(t, "NewState", 1)

		w.ResetLayoutState()
		l.EXPECT().NewState().Return(nil).Once()
		for range 3 {
			w.Snapshot()
		}
		l.AssertNumberOfCalls(t, "NewState", 2)

	})
}
