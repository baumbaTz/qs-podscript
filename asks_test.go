package main

import "testing"

func TestAsksList(t *testing.T) {
	old := P.DB
	P.DB = t.TempDir() + "/t.db"
	defer func() { P.DB = old }()
	st, err := openStore()
	if err != nil {
		t.Fatal(err)
	}
	st.addAsk(ask{Server: true, SrvID: 1, ID: 5, Kind: "diarize"})
	st.addAsk(ask{ID: 5})
	st.addAsk(ask{Server: true, SrvID: 1, ID: 5}) // same one again: updated, not doubled
	st.addAsk(ask{Server: true, SrvID: 2, ID: 5}) // same number on another server: its own entry
	if a := st.asks(); len(a) != 3 || a[0].Kind != "" || !a[0].Server || a[1].Server {
		t.Fatalf("asks: %+v", a)
	}
	st.dropServerAsks(1)
	if a := st.asks(); len(a) != 2 || a[0].Server || a[1].SrvID != 2 {
		t.Fatalf("after dropping server 1: %+v", a)
	}
	st.dropAsk(ask{ID: 5})
	st.dropServerAsks(0)
	if a := st.asks(); len(a) != 0 {
		t.Fatalf("after drop: %+v", a)
	}
}
