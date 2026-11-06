package core

import (
	"context"
	"testing"
)

func TestValidateConcurrency(t *testing.T) {
	for _, n := range []int{0, 1, DefaultConcurrency, MaxConcurrency} {
		if err := ValidateConcurrency(n); err != nil {
			t.Errorf("ValidateConcurrency(%d) = %v, want nil", n, err)
		}
	}
	for _, n := range []int{-1, MaxConcurrency + 1} {
		if err := ValidateConcurrency(n); err == nil {
			t.Errorf("ValidateConcurrency(%d) = nil, want an error", n)
		}
	}
}

func TestConcurrencyFrom(t *testing.T) {
	ctx := context.Background()
	if got := ConcurrencyFrom(ctx); got != DefaultConcurrency {
		t.Errorf("no value: got %d, want %d", got, DefaultConcurrency)
	}
	if got := ConcurrencyFrom(WithConcurrency(ctx, 0)); got != DefaultConcurrency {
		t.Errorf("zero: got %d, want %d", got, DefaultConcurrency)
	}
	if got := ConcurrencyFrom(WithConcurrency(ctx, 16)); got != 16 {
		t.Errorf("16: got %d, want 16", got)
	}
}

func TestReportConcurrency(t *testing.T) {
	var got []Update
	ctx := WithReporter(context.Background(), ReporterFunc(func(u Update) { got = append(got, u) }))
	ReportConcurrency(ctx, 8, "capped from 16: ssh MaxSessions")
	if len(got) != 1 {
		t.Fatalf("got %d updates, want 1", len(got))
	}
	u := got[0]
	if u.Concurrency != 8 || u.ConcurrencyNote != "capped from 16: ssh MaxSessions" {
		t.Errorf("got %d %q", u.Concurrency, u.ConcurrencyNote)
	}
	// The report carries no transfer figures: unknown, not zero.
	if u.Bytes != -1 || u.Files != -1 {
		t.Errorf("Bytes, Files = %d, %d, want -1, -1", u.Bytes, u.Files)
	}
	ReportConcurrency(context.Background(), 4, "") // no reporter: must not panic
}
