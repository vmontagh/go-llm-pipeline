package logger_test

import (
"testing"

"github.com/vmontagh/go-llm-pipeline/pkg/logger"
)

func TestNewNoOp_DoesNotPanic(t *testing.T) {
t.Parallel()
l := logger.NewNoOp()
if l == nil {
t.Fatal("NewNoOp() returned nil")
}

tests := []struct {
name string
fn   func()
}{
{"Debug", func() { l.Debug("d") }},
{"Info", func() { l.Info("i") }},
{"Warn", func() { l.Warn("w") }},
{"Error", func() { l.Error("e") }},
{"Debug with attrs", func() { l.Debug("d", "k", "v") }},
}

for _, tc := range tests {
t.Run(tc.name, func(t *testing.T) {
t.Parallel()
defer func() {
if r := recover(); r != nil {
t.Errorf("unexpected panic: %v", r)
}
}()
tc.fn()
})
}
}
