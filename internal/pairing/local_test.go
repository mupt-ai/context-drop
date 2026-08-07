package pairing

import (
	"sync"
	"testing"
	"time"
)

func TestLocalStoreConcurrentInstancesPreserveUpdates(t *testing.T) {
	path := t.TempDir() + "/pairing.json"
	const writers = 12
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, _, err := NewLocal(path).CreateChain(t.Context(), "machine", time.Now().UTC())
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	st, _, _, err := NewLocal(path).backend.(*localBackend).load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Machines) != writers {
		t.Fatalf("machines=%d, want %d", len(st.Machines), writers)
	}
}
