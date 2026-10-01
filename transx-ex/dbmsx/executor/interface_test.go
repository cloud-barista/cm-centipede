package executor

import (
	"context"
	"testing"

	drvpkg "github.com/cloud-barista/cm-centipede/transx-ex/dbmsx/driver"
)

// mockExecutor is a test double that satisfies the Executor interface.
type mockExecutor struct {
	called bool
	err    error
}

func (m *mockExecutor) Execute(_ context.Context, source, destination drvpkg.DBMSLocation) error {
	m.called = true
	return m.err
}

// Compile-time assertion: mockExecutor implements Executor.
var _ Executor = (*mockExecutor)(nil)

func TestExecutor_MethodConstants(t *testing.T) {
	if MethodDirect != "direct" {
		t.Errorf("MethodDirect = %q, want %q", MethodDirect, "direct")
	}
	if MethodSSHTunnel != "ssh-tunnel" {
		t.Errorf("MethodSSHTunnel = %q, want %q", MethodSSHTunnel, "ssh-tunnel")
	}
}

func TestExecutor_Interface_Satisfied(t *testing.T) {
	src := testDirectMySQL()
	dst := testDirectMySQL()

	exec := &mockExecutor{}
	if err := exec.Execute(context.Background(), src, dst); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !exec.called {
		t.Error("Execute was not called")
	}
}

func TestPipeline_Execute_CallsStepsInOrder(t *testing.T) {
	order := []string{}
	makeExec := func(name string) Executor {
		return &orderedExecutor{name: name, log: &order}
	}

	p := Pipeline{
		Name:     PipelineDBMSTransfer,
		Strategy: drvpkg.ScopeFull,
		Steps: []Step{
			{Name: StepDumpSource, Executor: makeExec("dump")},
			{Name: StepRestoreTarget, Executor: makeExec("restore")},
		},
	}

	if err := p.Execute(context.Background()); err != nil {
		t.Fatalf("Pipeline.Execute(context.Background()) returned error: %v", err)
	}
	if len(order) != 2 || order[0] != "dump" || order[1] != "restore" {
		t.Errorf("unexpected execution order: %v", order)
	}
}

type orderedExecutor struct {
	name string
	log  *[]string
}

func (o *orderedExecutor) Execute(_ context.Context, _, _ drvpkg.DBMSLocation) error {
	*o.log = append(*o.log, o.name)
	return nil
}

// testDirectMySQL returns a minimal direct-access MySQL location for testing.
func testDirectMySQL() drvpkg.DBMSLocation {
	return drvpkg.DBMSLocation{
		DBMSType:   drvpkg.DBMSTypeMySQL,
		Database:   "testdb",
		AccessType: drvpkg.AccessTypeDirect,
		Direct:     &drvpkg.DirectConfig{Host: "localhost", Port: 3306, Username: "root"},
	}
}
