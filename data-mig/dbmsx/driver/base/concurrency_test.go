package base

import (
	"context"
	"strings"
	"testing"

	"github.com/cloud-barista/cm-centipede/data-mig/core"
)

func TestRestoreConnections(t *testing.T) {
	for _, tc := range []struct {
		name      string
		requested int
		free      int
		want      int
		noteHas   string
	}{
		{"default", 0, -1, core.DefaultConcurrency, ""},
		{"as asked", 8, -1, 8, ""},
		{"capped", 32, -1, MaxRestoreConnections, "at most 16"},
		{"plenty free", 8, 100, 8, ""},
		{"half the free", 8, 6, 3, "6 connections free"},
		{"none free", 8, 0, 1, "0 connections free"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := core.WithConcurrency(context.Background(), tc.requested)
			n, note := RestoreConnections(ctx, tc.free)
			if n != tc.want {
				t.Errorf("connections = %d, want %d", n, tc.want)
			}
			if tc.noteHas == "" && note != "" || !strings.Contains(note, tc.noteHas) {
				t.Errorf("note = %q, want it to hold %q", note, tc.noteHas)
			}
		})
	}
}
