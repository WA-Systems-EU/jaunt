package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// netRecorder records iptables calls and accepts them all.
type netRecorder struct{ calls []string }

func (n *netRecorder) run(_ context.Context, bin string, args ...string) error {
	n.calls = append(n.calls, bin+" "+strings.Join(args, " "))
	return nil
}

func (n *netRecorder) has(sub string) bool {
	for _, c := range n.calls {
		if strings.Contains(c, sub) {
			return true
		}
	}
	return false
}

func TestAgentNetWithoutBoxInfo(t *testing.T) {
	n := &netRecorder{}
	if err := runAgentNet(context.Background(), n.run, filepath.Join(t.TempDir(), "box.json")); err != nil {
		t.Fatal(err)
	}
	if !n.has("-F JAUNTBOX-HOST") {
		t.Fatalf("guard not applied: %v", n.calls)
	}
	if n.has("-A JAUNTBOX-HOST") {
		t.Fatalf("host rules without box info: %v", n.calls)
	}
}

func TestAgentNetBadBoxInfo(t *testing.T) {
	p := filepath.Join(t.TempDir(), "box.json")
	if err := os.WriteFile(p, []byte("{nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	n := &netRecorder{}
	if err := runAgentNet(context.Background(), n.run, p); err == nil {
		t.Fatal("want the parse error")
	}
	if !n.has("-F JAUNTBOX-HOST") || n.has("-A JAUNTBOX-HOST") {
		t.Fatalf("guard must apply with no ports: %v", n.calls)
	}
}

func TestAgentNetUsesBoxPorts(t *testing.T) {
	p := filepath.Join(t.TempDir(), "box.json")
	if err := os.WriteFile(p, []byte(`{"agent":{"host_ports":[6379,5432]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	n := &netRecorder{}
	if err := runAgentNet(context.Background(), n.run, p); err != nil {
		t.Fatal(err)
	}
	if !n.has("-A JAUNTBOX-HOST -p tcp --dport 5432 -j ACCEPT") || !n.has("-A JAUNTBOX-HOST -p tcp --dport 6379 -j ACCEPT") {
		t.Fatalf("ports not applied: %v", n.calls)
	}
}

func TestAgentNetInvalidPortsStillGuards(t *testing.T) {
	p := filepath.Join(t.TempDir(), "box.json")
	if err := os.WriteFile(p, []byte(`{"agent":{"host_ports":[22]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	n := &netRecorder{}
	if err := runAgentNet(context.Background(), n.run, p); err == nil {
		t.Fatal("want validation error")
	}
	if !n.has("-F JAUNTBOX-HOST") {
		t.Fatalf("guard not applied: %v", n.calls)
	}
}
