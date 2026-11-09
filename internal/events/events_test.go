package events

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestJSONLAppends(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "events.jsonl")
	s := JSONLFile(path)
	in := []Event{
		{Time: time.Now().UTC(), Kind: BoxUpRequested, Fields: map[string]string{"user": "a"}},
		{Time: time.Now().UTC(), Kind: BoxDeleted},
	}
	for _, e := range in {
		if err := s.Emit(e); err != nil {
			t.Fatal(err)
		}
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var got []Event
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var e Event
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatal(err)
		}
		got = append(got, e)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 lines, got %d", len(got))
	}
	for i := range in {
		if got[i].Kind != in[i].Kind || got[i].Fields["user"] != in[i].Fields["user"] {
			t.Errorf("event %d mismatch: %+v vs %+v", i, got[i], in[i])
		}
	}
	st, _ := os.Stat(path)
	if st.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", st.Mode().Perm())
	}
	dst, _ := os.Stat(filepath.Dir(path))
	if dst.Mode().Perm() != 0o700 {
		t.Errorf("dir mode = %v, want 0700", dst.Mode().Perm())
	}
}
