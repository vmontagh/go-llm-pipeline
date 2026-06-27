package logger

import (
	"testing"
)

func TestNewNoOpLogger_AndEnableDebug(t *testing.T) {
	t.Parallel()
	l := NewNoOpLogger()
	if l == nil {
		t.Fatalf("NewNoOpLogger returned nil")
	}

	tests := []struct {
		name string
		fn   func()
	}{
		{name: "EnableDebug is a no-op", fn: func() { l.EnableDebug() }},
		{name: "Debugf accepts format and args", fn: func() { l.Debugf("d %s", "x") }},
		{name: "Infof accepts format and args", fn: func() { l.Infof("i %d", 1) }},
		{name: "Warnf accepts format and args", fn: func() { l.Warnf("w %v", true) }},
		{name: "Errorf accepts format and args", fn: func() { l.Errorf("e %s", "y") }},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("unwanted panic: %v", r)
				}
			}()
			tc.fn()
		})
	}
}
