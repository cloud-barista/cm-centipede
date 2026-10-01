package dbmsx

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

// =============================================================================
// Helpers — test model factories
// =============================================================================

func directLoc(dbmsType, db string) DBMSLocation {
	return DBMSLocation{
		DBMSType:   dbmsType,
		Database:   db,
		AccessType: AccessTypeDirect,
		Direct:     &DirectConfig{Host: "localhost", Port: 3306},
	}
}

func sshLoc(dbmsType, db string) DBMSLocation {
	return DBMSLocation{
		DBMSType:   dbmsType,
		Database:   db,
		AccessType: AccessTypeSSHTunnel,
		SSHTunnel: &SSHTunnelConfig{
			SSH:    &SSHConfig{Host: "ssh.example.com", Port: 22, Username: "deploy"},
			DBHost: "127.0.0.1",
			DBPort: 3306,
		},
	}
}

func mysqlDirectModel() DBMSMigrationModel {
	return DBMSMigrationModel{
		Source:      directLoc(DBMSTypeMySQL, "srcdb"),
		Destination: directLoc(DBMSTypeMySQL, "dstdb"),
		Scope:       ScopeFull,
	}
}

func mysqlSSHModel() DBMSMigrationModel {
	return DBMSMigrationModel{
		Source:      sshLoc(DBMSTypeMySQL, "srcdb"),
		Destination: sshLoc(DBMSTypeMySQL, "dstdb"),
		Scope:       ScopeFull,
	}
}

// planPipe plans model and releases whatever the plan allocated when the test
// ends. Planning a staging transfer creates the dump file up front — that is what
// makes its path unique and atomic — so a test that only inspects a plan still
// has to release it.
func planPipe(t *testing.T, model DBMSMigrationModel) *Pipeline {
	t.Helper()
	pipe, err := Plan(model)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	t.Cleanup(func() {
		if pipe.Cleanup != nil {
			pipe.Cleanup()
		}
	})
	return pipe
}

// =============================================================================
// Plan routing tests
// =============================================================================

func TestPlan_Direct_Direct_MySQL_ReturnsTwoSteps(t *testing.T) {
	pipe := planPipe(t, mysqlDirectModel())
	if len(pipe.Steps) != 2 {
		t.Errorf("expected 2 steps for direct→direct, got %d", len(pipe.Steps))
	}
}

func TestPlan_Direct_Direct_MySQL_StepNames(t *testing.T) {
	pipe := planPipe(t, mysqlDirectModel())
	if pipe.Steps[0].Name != StepDumpSource {
		t.Errorf("Steps[0].Name = %q, want %q", pipe.Steps[0].Name, StepDumpSource)
	}
	if pipe.Steps[1].Name != StepRestoreTarget {
		t.Errorf("Steps[1].Name = %q, want %q", pipe.Steps[1].Name, StepRestoreTarget)
	}
}

func TestPlan_SSHTunnel_SSHTunnel_MySQL_ReturnsOneStep(t *testing.T) {
	pipe := planPipe(t, mysqlSSHModel())
	if len(pipe.Steps) != 1 {
		t.Errorf("expected 1 step for ssh→ssh, got %d", len(pipe.Steps))
	}
}

func TestPlan_SSHTunnel_SSHTunnel_MySQL_StepName(t *testing.T) {
	pipe := planPipe(t, mysqlSSHModel())
	if pipe.Steps[0].Name != StepDirectPipe {
		t.Errorf("Steps[0].Name = %q, want %q", pipe.Steps[0].Name, StepDirectPipe)
	}
}

func TestPlan_Direct_SSHTunnel_PostgreSQL_ReturnsTwoSteps(t *testing.T) {
	model := DBMSMigrationModel{
		Source:      directLoc(DBMSTypePostgreSQL, "srcdb"),
		Destination: sshLoc(DBMSTypePostgreSQL, "dstdb"),
		Scope:       ScopeSchemaOnly,
	}
	pipe := planPipe(t, model)
	if len(pipe.Steps) != 2 {
		t.Errorf("expected 2 steps for direct→ssh, got %d", len(pipe.Steps))
	}
}

func TestPlan_SSHTunnel_Direct_MySQL_ReturnsTwoSteps(t *testing.T) {
	model := DBMSMigrationModel{
		Source:      sshLoc(DBMSTypeMySQL, "srcdb"),
		Destination: directLoc(DBMSTypeMySQL, "dstdb"),
		Scope:       ScopeFull,
	}
	pipe := planPipe(t, model)
	if len(pipe.Steps) != 2 {
		t.Errorf("expected 2 steps for ssh→direct, got %d", len(pipe.Steps))
	}
}

func TestPlan_MongoDB_CrossType_ReturnsError(t *testing.T) {
	model := DBMSMigrationModel{
		Source:      directLoc(DBMSTypeMongoDB, "srcdb"),
		Destination: sshLoc(DBMSTypeMongoDB, "dstdb"),
		Scope:       ScopeFull,
	}
	_, err := Plan(model)
	if err == nil {
		t.Fatal("expected validation error for MongoDB cross-type, got nil")
	}
	if !strings.Contains(err.Error(), "MongoDB") {
		t.Errorf("error should mention MongoDB: %v", err)
	}
}

func TestPlan_InvalidModel_ReturnsError(t *testing.T) {
	// Missing DBMSType.
	_, err := Plan(DBMSMigrationModel{
		Source:      DBMSLocation{Database: "db", AccessType: AccessTypeDirect, Direct: &DirectConfig{Host: "h"}},
		Destination: directLoc(DBMSTypeMySQL, "dst"),
	})
	if err == nil {
		t.Fatal("expected error for missing source.dbmsType, got nil")
	}
}

// =============================================================================
// Scope normalisation
// =============================================================================

func TestPlan_EmptyScope_NormalisedToFull(t *testing.T) {
	model := mysqlDirectModel()
	model.Scope = "" // should default to ScopeFull
	pipe := planPipe(t, model)
	if pipe.Strategy != ScopeFull {
		t.Errorf("Pipeline.Strategy = %q, want %q", pipe.Strategy, ScopeFull)
	}
}

// =============================================================================
// Step field verification
// =============================================================================

func TestPlan_DBMSTransfer_StepSources(t *testing.T) {
	model := mysqlDirectModel()
	pipe := planPipe(t, model)
	// Both steps must present the original source so RestoreExecutor derives
	// the correct staging path.
	for i, step := range pipe.Steps {
		if step.Source.Database != model.Source.Database {
			t.Errorf("Steps[%d].Source.Database = %q, want %q", i, step.Source.Database, model.Source.Database)
		}
	}
}

func TestPlan_DBMSTransfer_DumpStep_IntermediateDestination(t *testing.T) {
	model := mysqlDirectModel()
	pipe := planPipe(t, model)
	dumpStep := pipe.Steps[0]
	// Dump step's destination should be the local staging placeholder.
	if dumpStep.Destination.DBMSType != "local" {
		t.Errorf("dump step Destination.DBMSType = %q, want %q", dumpStep.Destination.DBMSType, "local")
	}
	if !strings.Contains(dumpStep.Destination.Database, DefaultStagingPath) {
		t.Errorf("dump step Destination.Database should contain staging path, got %q", dumpStep.Destination.Database)
	}
}

func TestPlan_DBMSTransfer_RestoreStep_RealDestination(t *testing.T) {
	model := mysqlDirectModel()
	pipe := planPipe(t, model)
	restoreStep := pipe.Steps[1]
	if restoreStep.Destination.Database != model.Destination.Database {
		t.Errorf("restore step Destination.Database = %q, want %q",
			restoreStep.Destination.Database, model.Destination.Database)
	}
}

func TestPlan_DirectPipe_StepLocations(t *testing.T) {
	model := mysqlSSHModel()
	pipe := planPipe(t, model)
	step := pipe.Steps[0]
	if step.Source.Database != model.Source.Database {
		t.Errorf("pipe step Source.Database = %q, want %q", step.Source.Database, model.Source.Database)
	}
	if step.Destination.Database != model.Destination.Database {
		t.Errorf("pipe step Destination.Database = %q, want %q", step.Destination.Database, model.Destination.Database)
	}
}

// =============================================================================
// buildStagingPath
// =============================================================================

// planStaging plans model and returns the staging file the pipeline will use,
// which the dump step carries as its destination. The planner no longer computes
// that path from the model — the dump executor creates the file and the pipeline
// records it — so a test reads it off the plan rather than deriving it.
func planStaging(t *testing.T, model DBMSMigrationModel) (*Pipeline, string) {
	t.Helper()
	pipe := planPipe(t, model)
	return pipe, pipe.Steps[0].Destination.Database
}

func TestPlan_StagingPath_MongoDB(t *testing.T) {
	_, p := planStaging(t, DBMSMigrationModel{
		Source:      directLoc(DBMSTypeMongoDB, "mydb"),
		Destination: directLoc(DBMSTypeMongoDB, "dstdb"),
		Scope:       ScopeFull,
	})
	if !strings.HasSuffix(p, ".archive") {
		t.Errorf("MongoDB staging path should end with .archive, got %q", p)
	}
}

func TestPlan_StagingPath_MySQL(t *testing.T) {
	_, p := planStaging(t, DBMSMigrationModel{
		Source:      directLoc(DBMSTypeMySQL, "mydb"),
		Destination: directLoc(DBMSTypeMySQL, "dstdb"),
		Scope:       ScopeFull,
	})
	if !strings.HasSuffix(p, ".sql") {
		t.Errorf("MySQL staging path should end with .sql, got %q", p)
	}
}

// The dump side creates the file and the restore side is handed the path, so
// what the pipeline reports is the file that actually exists.
func TestPlan_StagingPath_IsTheFileTheDumpCreated(t *testing.T) {
	model := mysqlDirectModel()
	model.Scope = ScopeSchemaOnly

	_, staging := planStaging(t, model)
	if staging == "" {
		t.Fatal("the plan carries no staging path")
	}
	if _, err := os.Stat(staging); err != nil {
		t.Errorf("staging file %q does not exist: %v", staging, err)
	}
}

// Two plans of the same model must not share a dump file. They used to: the path
// was a formula over the database name and the scope, so a second migration
// would overwrite the first one's dump while it was still being restored.
func TestPlan_StagingPath_IsUniquePerPlan(t *testing.T) {
	model := mysqlDirectModel()

	_, first := planStaging(t, model)
	_, second := planStaging(t, model)

	if first == second {
		t.Errorf("two plans of the same model share the staging file %q", first)
	}
}

// =============================================================================
// Pipeline.Execute — step ordering with mock executors
// =============================================================================

// mockExec is a minimal Executor that records call order and can inject errors.
type mockExec struct {
	name  string
	order *[]string
	err   error
}

func (m *mockExec) Execute(_ context.Context, _, _ DBMSLocation) error {
	*m.order = append(*m.order, m.name)
	return m.err
}

func TestPipeline_Execute_StepsRunInOrder(t *testing.T) {
	var order []string
	pipe := &Pipeline{
		Steps: []Step{
			{Name: "step1", Executor: &mockExec{name: "step1", order: &order}},
			{Name: "step2", Executor: &mockExec{name: "step2", order: &order}},
		},
	}
	if err := pipe.Execute(context.Background()); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(order) != 2 || order[0] != "step1" || order[1] != "step2" {
		t.Errorf("steps ran in wrong order: %v", order)
	}
}

func TestPipeline_Execute_StopsOnFirstError(t *testing.T) {
	var order []string
	sentinel := errors.New("step1 failed")
	pipe := &Pipeline{
		Steps: []Step{
			{Name: "step1", Executor: &mockExec{name: "step1", order: &order, err: sentinel}},
			{Name: "step2", Executor: &mockExec{name: "step2", order: &order}},
		},
	}
	err := pipe.Execute(context.Background())
	if err == nil {
		t.Fatal("expected Execute to return error, got nil")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("expected sentinel error, got %v", err)
	}
	if len(order) != 1 || order[0] != "step1" {
		t.Errorf("step2 should not have run, order = %v", order)
	}
}

func TestPipeline_Execute_EmptyPipeline_ReturnsNil(t *testing.T) {
	pipe := &Pipeline{}
	if err := pipe.Execute(context.Background()); err != nil {
		t.Errorf("empty pipeline should return nil, got %v", err)
	}
}

// =============================================================================
// Pipeline metadata
// =============================================================================

func TestPlan_PipelineName(t *testing.T) {
	pipe := planPipe(t, mysqlDirectModel())
	if pipe.Name != PipelineDBMSTransfer {
		t.Errorf("Pipeline.Name = %q, want %q", pipe.Name, PipelineDBMSTransfer)
	}
}

func TestPlan_PipelineScope(t *testing.T) {
	model := mysqlDirectModel()
	model.Scope = ScopeSchemaOnly
	pipe := planPipe(t, model)
	if pipe.Strategy != ScopeSchemaOnly {
		t.Errorf("Pipeline.Strategy = %q, want %q", pipe.Strategy, ScopeSchemaOnly)
	}
}

// =============================================================================
// Staging cleanup
// =============================================================================

// The dump file is an intermediate between the two steps, and what it holds is
// the source database in full, in plain text on local disk. Nothing reads it
// after the restore, so the pipeline removes it — and Execute runs Cleanup with
// defer, which is what makes an aborted restore leave no partial dump behind.

func TestPlan_DBMSTransfer_RemovesStagingFile(t *testing.T) {
	pipe, staging := planStaging(t, mysqlDirectModel())
	if pipe.Cleanup == nil {
		t.Fatal("staging pipeline carries no Cleanup, so the dump file would be left on disk")
	}

	// Stand in for a dump that got part way: a partial dump is the same data with
	// less of it, and must not survive either.
	if err := os.WriteFile(staging, []byte("-- dump"), 0o600); err != nil {
		t.Fatalf("write staging file: %v", err)
	}

	pipe.Cleanup()

	if _, err := os.Stat(staging); !os.IsNotExist(err) {
		t.Errorf("staging file %q survived Cleanup (stat error: %v)", staging, err)
	}
}

func TestPlan_DBMSTransfer_ExecuteRemovesStagingFile(t *testing.T) {
	pipe, staging := planStaging(t, mysqlDirectModel())

	if err := os.WriteFile(staging, []byte("-- dump"), 0o600); err != nil {
		t.Fatalf("write staging file: %v", err)
	}

	// A cancelled context ends the pipeline before the first step, which is the
	// way a test can end one without a database on either side. Cleanup is
	// deferred, so it runs on this path exactly as it does after a real restore.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := pipe.Execute(ctx); err == nil {
		t.Fatal("Execute on a cancelled context returned no error")
	}
	if _, err := os.Stat(staging); !os.IsNotExist(err) {
		t.Errorf("staging file %q survived Execute (stat error: %v)", staging, err)
	}
}

// A direct pipe streams dump stdout into restore stdin and writes no file, so
// there is nothing for it to clean up.
func TestPlan_DirectPipe_HasNoCleanup(t *testing.T) {
	pipe := planPipe(t, mysqlSSHModel())
	if pipe.Cleanup != nil {
		t.Error("direct-pipe pipeline carries a Cleanup, which suggests it staged a file it does not need")
	}
}
