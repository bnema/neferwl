package drm

import "testing"

func TestHDRPropertyMetadata(t *testing.T) {
	e := propEnum{value: 9}
	copy(e.name[:], "BT2020_RGB")
	if id, v := bt2020Enum(3, []propEnum{e}); id != 3 || v != 9 {
		t.Fatalf("enum %d %d", id, v)
	}
	if id, _ := bt2020Enum(3, nil); id != 0 {
		t.Fatal("missing enum accepted")
	}
	for _, tt := range []struct {
		values []uint64
		want   bool
	}{
		{nil, false}, {[]uint64{8}, false}, {[]uint64{8, 9}, false}, {[]uint64{8, 10}, true}, {[]uint64{10, 12}, true}, {[]uint64{11, 12}, false},
	} {
		if got := bpcAllows10(tt.values); got != tt.want {
			t.Fatalf("%v: %t", tt.values, got)
		}
	}
}
