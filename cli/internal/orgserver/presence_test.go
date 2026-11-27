package orgserver

import "testing"

func TestReclaimNeverTakesPresenceFromANewerConnection(t *testing.T) {
	st := testStore(t)
	if err := st.Reclaim("acme", "laptop", "s1", "old"); err != nil {
		t.Fatal(err)
	}
	if conn, _, _ := st.Holder("acme", "laptop"); conn != "old" {
		t.Fatalf("absent row not reclaimed: %q", conn)
	}
	_ = st.Connected("acme", "laptop", "s2", "new")
	_ = st.Reclaim("acme", "laptop", "s1", "old")
	if conn, _, _ := st.Holder("acme", "laptop"); conn != "new" {
		t.Fatalf("reclaim took the row from a newer connection: %q", conn)
	}
	_ = st.Disconnected("acme", "laptop", "new")
	_ = st.Reclaim("acme", "laptop", "s1", "old")
	if conn, _, _ := st.Holder("acme", "laptop"); conn != "old" {
		t.Fatalf("cleared row not reclaimed: %q", conn)
	}
}
