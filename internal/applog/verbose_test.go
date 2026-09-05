package applog

import (
	"bytes"
	"log"
	"strings"
	"testing"
)

func TestDebugfGatedByVerbose(t *testing.T) {
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev); SetVerbose(false) })

	SetVerbose(false)
	Debugf("hidden %d", 1)
	if buf.Len() != 0 {
		t.Fatalf("wrote while off: %q", buf.String())
	}

	SetVerbose(true)
	if !Verbose() {
		t.Fatal("Verbose() false after SetVerbose(true)")
	}
	Debugf("shown %d", 2)
	if got := buf.String(); !strings.Contains(got, "debug: shown 2") {
		t.Fatalf("missing debug line, got %q", got)
	}
}
