package store

import (
	"fmt"
	"testing"
)

// Regression test: AddDevice must return the id of the device with the
// given IP on BOTH the insert path and the upsert-update path, even
// when other inserts happen in between. The previous implementation
// read res.LastInsertId() after the upsert; on the update branch that
// value is whatever the pooled connection last inserted anywhere, so
// re-imports attached interfaces, monitors and map markers to wrong
// devices, nondeterministically.
func TestAddDeviceUpsertReturnsOwnID(t *testing.T) {
	st := testStore(t)
	first, err := st.AddDevice(Device{Name: "alpha", IP: "10.9.0.1", Type: "Router"})
	if err != nil {
		t.Fatalf("add alpha: %v", err)
	}
	second, err := st.AddDevice(Device{Name: "beta", IP: "10.9.0.2", Type: "Switch"})
	if err != nil {
		t.Fatalf("add beta: %v", err)
	}
	if first == second {
		t.Fatalf("distinct devices share id %d", first)
	}
	for i := 0; i < 30; i++ {
		// Interleave unrelated inserts so any pooled connection's last
		// insert rowid points at a non-device row.
		if _, err := st.AddGroup(fmt.Sprintf("noise-%d", i), nil); err != nil {
			t.Fatalf("noise group: %v", err)
		}
		again, err := st.AddDevice(Device{Name: "alpha-renamed", IP: "10.9.0.1", Type: "Router"})
		if err != nil {
			t.Fatalf("re-add alpha: %v", err)
		}
		if again != first {
			t.Fatalf("iteration %d: upsert of 10.9.0.1 returned id %d, want its own id %d", i, again, first)
		}
		againB, err := st.AddDevice(Device{Name: "beta", IP: "10.9.0.2", Type: "Switch"})
		if err != nil {
			t.Fatalf("re-add beta: %v", err)
		}
		if againB != second {
			t.Fatalf("iteration %d: upsert of 10.9.0.2 returned id %d, want %d", i, againB, second)
		}
	}
	devs, err := st.ListDevices()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(devs) != 2 {
		t.Fatalf("stored devices = %d, want 2 (upserts must not duplicate)", len(devs))
	}
}
