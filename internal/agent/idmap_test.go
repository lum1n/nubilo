package agent

import (
	"path/filepath"
	"testing"
)

func TestMapPutRebindsObjectID(t *testing.T) {
	m, err := OpenMap(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	a := Mapping{LocalID: "ek-a", Kind: kindEvent, ObjectID: "obj-1", CollectionID: "col", ContentHash: "h1", Revision: 1}
	if err := m.Put(a); err != nil {
		t.Fatal(err)
	}
	b := Mapping{LocalID: "ek-b", Kind: kindEvent, ObjectID: "obj-1", CollectionID: "col", ContentHash: "h2", Revision: 2}
	if err := m.Put(b); err != nil {
		t.Fatalf("rebind: %v", err)
	}
	got, err := m.ByObject("obj-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.LocalID != "ek-b" || got.Revision != 2 {
		t.Fatalf("%+v", got)
	}
	if _, err := m.ByLocal(kindEvent, "ek-a"); err == nil {
		t.Fatal("stale local id still mapped")
	}
	n, err := m.ForCollection("col")
	if err != nil {
		t.Fatal(err)
	}
	if len(n) != 1 {
		t.Fatalf("rows %d", len(n))
	}
}

func TestMapPutRebindJournalSpam(t *testing.T) {
	m, err := OpenMap(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	for i := 0; i < 8; i++ {
		row := Mapping{
			LocalID: "ek-" + string(rune('a'+i)), Kind: kindEvent, ObjectID: "obj-1",
			CollectionID: "col", ContentHash: "h", Revision: uint64(i + 1),
		}
		if err := m.Put(row); err != nil {
			t.Fatalf("put %d: %v", i, err)
		}
	}
	got, err := m.ByObject("obj-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.LocalID != "ek-h" {
		t.Fatalf("local %s", got.LocalID)
	}
	n, err := m.ForCollection("col")
	if err != nil {
		t.Fatal(err)
	}
	if len(n) != 1 {
		t.Fatalf("rows %d", len(n))
	}
}
