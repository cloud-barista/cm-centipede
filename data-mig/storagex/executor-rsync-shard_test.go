package storagex

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloud-barista/cm-centipede/data-mig/core"
)

func TestSplitBySize(t *testing.T) {
	files := []FileInfo{{Path: "a", Size: 100}, {Path: "b", Size: 60}, {Path: "c", Size: 50}, {Path: "d", Size: 40}, {Path: "e", Size: 10}}
	shards := splitBySize(files, 2)
	var sizes [2]int64
	seen := 0
	for i, s := range shards {
		for _, f := range s {
			sizes[i] += f.Size
			seen++
		}
	}
	if seen != len(files) {
		t.Fatalf("dealt %d files, want %d", seen, len(files))
	}
	// Largest first, each to the lightest list: never further apart than the
	// largest single file.
	if d := sizes[0] - sizes[1]; d > 100 || d < -100 {
		t.Errorf("shard bytes %v: spread over the largest file", sizes)
	}
}

func TestShardPlan(t *testing.T) {
	ctx := func(c int) context.Context { return core.WithConcurrency(context.Background(), c) }
	for _, tc := range []struct {
		name    string
		e       RsyncExecutor
		c       int
		want    int
		noteHas string
	}{
		{"one", RsyncExecutor{}, 1, 1, ""},
		{"default", RsyncExecutor{}, 0, core.DefaultConcurrency, ""},
		{"capped", RsyncExecutor{}, 16, rsyncMaxShards, "capped from 16"},
		{"dry run", RsyncExecutor{DryRun: true}, 8, 1, "dry run"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, note := tc.e.shardPlan(ctx(tc.c), DataLocation{})
			if n != tc.want || !strings.Contains(note, tc.noteHas) || (tc.noteHas == "" && note != "") {
				t.Errorf("shardPlan = %d %q, want %d with %q", n, note, tc.want, tc.noteHas)
			}
		})
	}
}

func TestShardsFor(t *testing.T) {
	const (
		MiB = int64(1) << 20
		GiB = int64(1) << 30
	)
	for _, tc := range []struct {
		name  string
		n     int
		files int
		size  int64
		want  int
	}{
		{"54 small files", 2, 54, 2_473_790, 1},
		{"120 small files", 2, 120, 6_000_000, 2},
		{"150 small files of 8", 8, 150, 7_500_000, 3},
		{"54 files of 1 GiB", 4, 54, 54 * GiB, 4},
		{"3 files of 10 GiB", 8, 3, 30 * GiB, 3},
		{"10,000 small files", 8, 10_000, 1_000_000_000, 8},
		{"60 files, 300 MiB", 4, 60, 300 * MiB, 2},
		{"60 files, 700 MiB", 4, 60, 700 * MiB, 4},
		{"54 files, 162 MB", 2, 54, 162_000_000, 1},
		{"one 50 GiB file", 4, 1, 50 * GiB, 1},
	} {
		if got := shardsFor(tc.n, tc.files, tc.size); got != tc.want {
			t.Errorf("%s: shardsFor(%d, %d, %d) = %d, want %d", tc.name, tc.n, tc.files, tc.size, got, tc.want)
		}
	}
}

func TestDescribeFiles(t *testing.T) {
	for _, tc := range []struct {
		files int
		size  int64
		want  string
	}{
		{54, 2_473_790, "54 files, 2.4 MiB"},
		{1, 50 << 30, "1 file, 50.0 GiB"},
		{3, 512, "3 files, 512 B"},
	} {
		if got := describeFiles(tc.files, tc.size); got != tc.want {
			t.Errorf("describeFiles(%d, %d) = %q, want %q", tc.files, tc.size, got, tc.want)
		}
	}
}

func TestShardSum(t *testing.T) {
	var mu sync.Mutex
	var last core.Update
	sum := newShardSum(core.ReporterFunc(func(u core.Update) { mu.Lock(); last = u; mu.Unlock() }), 2)
	sum.shard(0).Report(core.Update{Bytes: 100, Files: -1})
	sum.shard(1).Report(core.Update{Bytes: 50, Files: -1})
	sum.shard(0).Report(core.Update{Bytes: 300, Files: -1}) // a running total replaces, not adds
	if last.Bytes != 350 {
		t.Fatalf("summed bytes = %d, want 350", last.Bytes)
	}
	off := sum.offset()
	off.Report(core.Update{Bytes: 7, Files: -1})
	if last.Bytes != 357 || last.Files != -1 {
		t.Errorf("final run counted from %d (files %d), want 357 (-1)", last.Bytes, last.Files)
	}
}

func TestNulList(t *testing.T) {
	got := string(nulList([]FileInfo{{Path: "a b/c"}, {Path: "d"}}))
	if got != "a b/c\x00d\x00" {
		t.Errorf("nulList = %q", got)
	}
}

// ============================================================================
// Integration: agent-forward between the testenv filesystem hosts
// ============================================================================
//
//	export RSYNC_TEST_HOST=172.24.78.163
//	export RSYNC_TEST_SRC_PORT=32210 RSYNC_TEST_DST_PORT=32211
//	export RSYNC_TEST_KEY=$PWD/../testenv/dockerenv/ssh_keys/id_rsa

func rsyncTestHosts(t *testing.T) (src, dst *SSHConfig) {
	t.Helper()
	host := os.Getenv("RSYNC_TEST_HOST")
	if host == "" {
		t.Skip("RSYNC_TEST_HOST not set; skipping rsync integration test")
	}
	port := func(env string) int {
		p, err := strconv.Atoi(os.Getenv(env))
		if err != nil {
			t.Fatalf("%s: %v", env, err)
		}
		return p
	}
	key := os.Getenv("RSYNC_TEST_KEY")
	return &SSHConfig{Host: host, Port: port("RSYNC_TEST_SRC_PORT"), Username: "root", PrivateKeyPath: key},
		&SSHConfig{Host: host, Port: port("RSYNC_TEST_DST_PORT"), Username: "root", PrivateKeyPath: key}
}

// sshRun runs cmd on cfg's host with the system ssh client.
func sshRun(t *testing.T, cfg *SSHConfig, cmd string) string {
	t.Helper()
	out, err := exec.Command("ssh", "-i", cfg.PrivateKeyPath, "-p", strconv.Itoa(cfg.Port),
		"-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile=/dev/null", "-o", "LogLevel=ERROR",
		cfg.Username+"@"+cfg.Host, cmd).CombinedOutput()
	if err != nil {
		t.Fatalf("ssh %s: %v\n%s", cmd, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestRsyncShardsThenFinalRun(t *testing.T) {
	srcSSH, dstSSH := rsyncTestHosts(t)
	stamp := strconv.FormatInt(time.Now().UnixNano(), 36)
	srcDir, dstDir := "/tmp/shardtest-src-"+stamp, "/tmp/shardtest-dst-"+stamp

	// 400 files over 8 directories, an empty directory and a symbolic link —
	// the last two only the final run can carry — and a file the destination
	// has but the source does not, for --delete.
	sshRun(t, srcSSH, fmt.Sprintf(`set -e; mkdir -p %[1]s/empty; for d in 0 1 2 3 4 5 6 7; do mkdir -p %[1]s/d$d;
		for f in $(seq 1 50); do head -c $((f*97)) /dev/urandom > "%[1]s/d$d/f $f.bin"; done; done;
		ln -s d0 %[1]s/link`, srcDir))
	sshRun(t, dstSSH, fmt.Sprintf(`mkdir -p %[1]s && echo stale > %[1]s/stale.txt`, dstDir))
	t.Cleanup(func() {
		sshRun(t, srcSSH, "rm -rf "+srcDir)
		sshRun(t, dstSSH, "rm -rf "+dstDir)
	})

	var mu sync.Mutex
	var updates []core.Update
	ctx := core.WithReporter(core.WithConcurrency(context.Background(), 4),
		core.ReporterFunc(func(u core.Update) { mu.Lock(); updates = append(updates, u); mu.Unlock() }))

	src := DataLocation{StorageType: StorageTypeFilesystem, Path: srcDir,
		Filesystem: &FilesystemAccess{AccessType: AccessTypeSSH, SSH: srcSSH}}
	dst := DataLocation{StorageType: StorageTypeFilesystem, Path: dstDir,
		Filesystem: &FilesystemAccess{AccessType: AccessTypeSSH, SSH: dstSSH}}
	e := &RsyncExecutor{Mode: TransferModeAgentForward, DeleteExtraneous: true}
	if err := e.Execute(ctx, src, dst); err != nil {
		t.Fatal(err)
	}

	if got := sshRun(t, dstSSH, "find "+dstDir+" -type f | wc -l"); got != "400" {
		t.Errorf("files at the destination = %s, want 400", got)
	}
	if got := sshRun(t, dstSSH, "test -d "+dstDir+"/empty && test -L "+dstDir+"/link && test ! -e "+dstDir+"/stale.txt && echo ok"); got != "ok" {
		t.Errorf("final run did not settle the empty directory, the link and --delete: %q", got)
	}
	if a, b := sshRun(t, srcSSH, "cd "+srcDir+" && find . -type f -exec md5sum {} + | sort | md5sum"),
		sshRun(t, dstSSH, "cd "+dstDir+" && find . -type f -exec md5sum {} + | sort | md5sum"); a != b {
		t.Errorf("contents differ: %s vs %s", a, b)
	}

	var conc core.Update
	var maxBytes int64
	for _, u := range updates {
		if u.Concurrency > 0 {
			conc = u
		}
		maxBytes = max(maxBytes, u.Bytes)
	}
	if conc.Concurrency != 4 {
		t.Errorf("reported concurrency %d (%q), want 4", conc.Concurrency, conc.ConcurrencyNote)
	}
	if maxBytes <= 0 {
		t.Errorf("no bytes reported")
	}
	t.Logf("concurrency %d %q, bytes %d", conc.Concurrency, conc.ConcurrencyNote, maxBytes)
}
