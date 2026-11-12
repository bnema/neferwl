package domain

import "testing"

func TestContentBatchResetClearsRoutes(t *testing.T) {
	b := ContentBatch{Routes: 3, Items: []SurfaceContent{{ID: 1}}}
	b.Reset()
	if b.Routes != 0 || len(b.Items) != 0 || cap(b.Items) != 1 {
		t.Fatalf("reset batch: %+v", b)
	}
}
