package config

import "testing"

func TestExampleConfigLoads(t *testing.T) {
	if _, err := Load("../../examples/jauntbox.yaml"); err != nil {
		t.Fatal(err)
	}
}
