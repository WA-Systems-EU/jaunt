package agentrun

import (
	"reflect"
	"testing"
)

const tcpHeader = "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"

// 0x1538 is 5432. 0100007F is 127.0.0.1 (little-endian words).
const (
	lineLoopback4 = "   0: 0100007F:1538 00000000:0000 0A 00000000:00000000 00:00000000 00000000   999        0 11111 1 0000000000000000 100 0 0 10 0\n"
	lineWild4     = "   1: 00000000:1538 00000000:0000 0A 00000000:00000000 00:00000000 00000000   999        0 11112 1 0000000000000000 100 0 0 10 0\n"
	lineTailnet4  = "   2: 05004064:1538 00000000:0000 0A 00000000:00000000 00:00000000 00000000   999        0 11113 1 0000000000000000 100 0 0 10 0\n"
	lineEstab4    = "   3: 0100007F:1538 0100007F:C350 01 00000000:00000000 00:00000000 00000000   999        0 11114 1 0000000000000000 100 0 0 10 0\n"
	lineOther4    = "   4: 00000000:0016 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 11115 1 0000000000000000 100 0 0 10 0\n"
	lineLoop6     = "   0: 00000000000000000000000001000000:1538 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000   999        0 22221 1 0000000000000000 100 0 0 10 0\n"
	lineWild6     = "   1: 00000000000000000000000000000000:1538 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000   999        0 22222 1 0000000000000000 100 0 0 10 0\n"
)

func TestListenWarningsLoopbackOnly(t *testing.T) {
	got := ListenWarnings([]byte(tcpHeader+lineLoopback4), []byte(tcpHeader+lineLoop6), []int{5432})
	want := []string{`port 5432: listens only on 127.0.0.1,::1, which agents can't reach; publish it on all interfaces (in compose: "5432:5432", not "127.0.0.1:5432:5432")`}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

func TestListenWarningsNothingListening(t *testing.T) {
	want := []string{"port 5432: nothing listens on it yet; agents are refused until a service does"}
	for name, tcp := range map[string]string{
		"no line":     tcpHeader + lineOther4,
		"established": tcpHeader + lineEstab4,
		"empty":       "",
	} {
		if got := ListenWarnings([]byte(tcp), nil, []int{5432}); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: got %q", name, got)
		}
	}
}

func TestListenWarningsWildcardOK(t *testing.T) {
	if got := ListenWarnings([]byte(tcpHeader+lineWild4), nil, []int{5432}); len(got) != 0 {
		t.Errorf("tcp wildcard: %q", got)
	}
	if got := ListenWarnings(nil, []byte(tcpHeader+lineWild6), []int{5432}); len(got) != 0 {
		t.Errorf("tcp6 wildcard: %q", got)
	}
	// a wildcard next to a loopback listener is fine
	if got := ListenWarnings([]byte(tcpHeader+lineLoopback4+lineWild4), nil, []int{5432}); len(got) != 0 {
		t.Errorf("mixed: %q", got)
	}
}

func TestListenWarningsSpecificAddress(t *testing.T) {
	got := ListenWarnings([]byte(tcpHeader+lineTailnet4), nil, []int{5432})
	want := []string{"port 5432: listens only on 100.64.0.5; agents connect to host.jauntbox (the Docker bridge gateway), so bind it to all interfaces"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q\nwant %q", got, want)
	}
	// loopback plus a specific address is not all-loopback
	got = ListenWarnings([]byte(tcpHeader+lineLoopback4+lineTailnet4), nil, []int{5432})
	want = []string{"port 5432: listens only on 127.0.0.1,100.64.0.5; agents connect to host.jauntbox (the Docker bridge gateway), so bind it to all interfaces"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

func TestListenWarningsPortOrder(t *testing.T) {
	got := ListenWarnings([]byte(tcpHeader+lineLoopback4), nil, []int{6379, 5432})
	if len(got) != 2 || got[0][:10] != "port 5432:" || got[1][:10] != "port 6379:" {
		t.Fatalf("got %q", got)
	}
}
