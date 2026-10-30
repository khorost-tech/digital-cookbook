package svc

import (
	"os"
	"testing"
)

func TestName(t *testing.T) {
	os.Remove("svc.tmp") // want errcheck // noise gosec
	if Name() != "svc" {
		t.Fatal(Name())
	}
}
