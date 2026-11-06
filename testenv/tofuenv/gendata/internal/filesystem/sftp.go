// Package filesystem transfers generated dummy files to a VM's filesystem over
// SSH/SFTP, sharing the same run identifier and folder layout as the bucket target.
package filesystem

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"sync"
	"time"

	"github.com/cloud-barista/cm-centipede/testenv/tofuenv/gendata/internal/generate"
	"github.com/cloud-barista/cm-centipede/testenv/tofuenv/gendata/internal/layout"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// ErrDataExists indicates the VM base path already contains data.
var ErrDataExists = errors.New("filesystem base path already contains data")

// Params configures a VM SFTP transfer run.
type Params struct {
	Host           string
	Port           int
	User           string
	PrivateKeyPath string
	BasePath       string // e.g. /home/ubuntu/testdata
	Concurrency    int
	// Connections is how many SSH connections the Concurrency workers are spread
	// over, round robin. 0 or 1 means one.
	Connections int

	// OnFile, when set, is called with the size of each file once it is
	// transferred, so the caller can report progress. Called from the workers.
	OnFile func(size int64)
}

func dial(p Params) (*ssh.Client, *sftp.Client, error) {
	key, err := os.ReadFile(p.PrivateKeyPath)
	if err != nil {
		return nil, nil, fmt.Errorf("read private key %s: %w", p.PrivateKeyPath, err)
	}
	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		return nil, nil, fmt.Errorf("parse private key: %w", err)
	}
	port := p.Port
	if port == 0 {
		port = 22
	}
	cfg := &ssh.ClientConfig{
		User:            p.User,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // POC: ephemeral, freshly prepared VM
		Timeout:         15 * time.Second,
	}
	addr := fmt.Sprintf("%s:%d", p.Host, port)
	sc, err := ssh.Dial("tcp", addr, cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("ssh dial %s: %w", addr, err)
	}
	// Concurrent writes let one file go out as many in-flight requests instead of
	// one 32 KiB write at a time waiting for its reply - the difference between
	// being bound by the round trip and by the bandwidth. ReadFrom only uses it
	// when it can tell the source's size, which an *os.File tells it.
	fc, err := sftp.NewClient(sc, sftp.UseConcurrentWrites(true))
	if err != nil {
		_ = sc.Close()
		return nil, nil, fmt.Errorf("sftp client: %w", err)
	}
	return sc, fc, nil
}

// CheckExisting returns ErrDataExists if BasePath exists and is non-empty.
func CheckExisting(_ context.Context, p Params) error {
	sc, fc, err := dial(p)
	if err != nil {
		return err
	}
	defer sc.Close()
	defer fc.Close()

	fi, err := fc.Stat(p.BasePath)
	if err != nil {
		return nil // absent path counts as empty
	}
	if !fi.IsDir() {
		return fmt.Errorf("%w: %s exists and is not a directory", ErrDataExists, p.BasePath)
	}
	entries, err := fc.ReadDir(p.BasePath)
	if err != nil {
		return fmt.Errorf("read dir %s: %w", p.BasePath, err)
	}
	if len(entries) > 0 {
		return fmt.Errorf("%w: %s", ErrDataExists, p.BasePath)
	}
	return nil
}

// Transfer uploads files to <BasePath>/<leaf>/<rel>. Returns the count transferred.
//
// The workers are spread over p.Connections SSH connections, because one TCP
// connection is a ceiling of its own over a long path, however many requests
// share it. Each directory is created once: the set of directories is small
// next to the number of files, and a MkdirAll per file is a stat round trip
// per file, taken under a lock.
func Transfer(_ context.Context, p Params, files []generate.File, plan layout.Plan) (int, error) {
	conns := p.Connections
	if conns < 1 {
		conns = 1
	}
	conc := p.Concurrency
	if conc < 1 {
		conc = 1
	}
	if conns > conc {
		conns = conc // a connection without a worker would only cost a login
	}
	clients := make([]*sftp.Client, 0, conns)
	defer func() {
		for _, fc := range clients {
			_ = fc.Close()
		}
	}()
	var sshClients []*ssh.Client
	defer func() {
		for _, sc := range sshClients {
			_ = sc.Close()
		}
	}()
	for i := 0; i < conns; i++ {
		sc, fc, err := dial(p)
		if err != nil {
			return 0, err
		}
		sshClients = append(sshClients, sc)
		clients = append(clients, fc)
	}

	type job struct {
		idx  int
		file generate.File
	}
	jobs := make(chan job)

	var (
		dirMu    sync.Mutex
		madeDirs = map[string]bool{}
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
		count    int
	)
	// ensureDir creates a directory the first time any worker needs it. The
	// lock is held across MkdirAll so two workers never race on the same tree;
	// once a directory is known, it costs nothing.
	ensureDir := func(fc *sftp.Client, dir string) error {
		dirMu.Lock()
		defer dirMu.Unlock()
		if madeDirs[dir] {
			return nil
		}
		if err := fc.MkdirAll(dir); err != nil {
			return err
		}
		madeDirs[dir] = true
		return nil
	}
	worker := func(fc *sftp.Client) {
		defer wg.Done()
		for j := range jobs {
			remote := path.Join(p.BasePath, plan.Rel(j.idx, j.file.Rel))
			err := ensureDir(fc, path.Dir(remote))
			if err == nil {
				err = copyFile(fc, j.file.Abs, remote)
			}
			if err == nil && p.OnFile != nil {
				p.OnFile(j.file.Size)
			}
			mu.Lock()
			if err != nil {
				if firstErr == nil {
					firstErr = fmt.Errorf("transfer %s: %w", remote, err)
				}
			} else {
				count++
			}
			mu.Unlock()
		}
	}
	wg.Add(conc)
	for i := 0; i < conc; i++ {
		go worker(clients[i%conns])
	}
	for i, f := range files {
		jobs <- job{idx: i, file: f}
	}
	close(jobs)
	wg.Wait()
	return count, firstErr
}

// CheckLarge returns ErrDataExists naming every one of files already under
// BasePath. Only those paths are looked at, not BasePath as a whole, which is
// what lets large files be added next to a dataset that is already there.
func CheckLarge(_ context.Context, p Params, files []generate.Large) error {
	sc, fc, err := dial(p)
	if err != nil {
		return err
	}
	defer sc.Close()
	defer fc.Close()

	var found []string
	for _, f := range files {
		remote := path.Join(p.BasePath, f.Rel)
		if _, err := fc.Stat(remote); err == nil {
			found = append(found, remote)
		}
	}
	if len(found) > 0 {
		return fmt.Errorf("%w: %v", ErrDataExists, found)
	}
	return nil
}

// ErrNoStatVFS indicates the SFTP server cannot report disk usage.
var ErrNoStatVFS = errors.New("sftp server does not support statvfs")

// Usage reports the size of the file system BasePath is on and how much of it
// is in use, from the nearest directory that exists - BasePath may not yet.
//
// Used is everything this login cannot write to: the space actually taken plus
// the blocks reserved for root, which a non-root account cannot use either.
// That is what the volume size alone cannot tell: a VM that already holds a
// 50 GB dataset has far less room than its size says.
func Usage(_ context.Context, p Params) (total, used uint64, err error) {
	sc, fc, err := dial(p)
	if err != nil {
		return 0, 0, err
	}
	defer sc.Close()
	defer fc.Close()

	dir := p.BasePath
	for {
		if _, serr := fc.Stat(dir); serr == nil || dir == "/" || dir == "." {
			break
		}
		dir = path.Dir(dir)
	}
	st, err := fc.StatVFS(dir)
	if err != nil {
		return 0, 0, fmt.Errorf("%w: %v", ErrNoStatVFS, err)
	}
	total = st.TotalSpace()
	return total, total - st.Frsize*st.Bavail, nil
}

// TransferLarge writes the large files to <BasePath>/<rel>, one after another
// over a single connection, and returns the count transferred. A file is a
// single stream either way, and the pipelined writes of one already keep the
// connection full. Their bytes go to onRead as they are read, so OnFile is told
// 0 when a file is done.
func TransferLarge(_ context.Context, p Params, files []generate.Large, onRead func(n int64)) (int, error) {
	sc, fc, err := dial(p)
	if err != nil {
		return 0, err
	}
	defer sc.Close()
	defer fc.Close()

	madeDirs := map[string]bool{}
	for i, f := range files {
		remote := path.Join(p.BasePath, f.Rel)
		if dir := path.Dir(remote); !madeDirs[dir] {
			if err := fc.MkdirAll(dir); err != nil {
				return i, fmt.Errorf("transfer %s: %w", remote, err)
			}
			madeDirs[dir] = true
		}
		if err := writeFile(fc, f.Reader(onRead), remote); err != nil {
			return i, fmt.Errorf("transfer %s: %w", remote, err)
		}
		if p.OnFile != nil {
			p.OnFile(0)
		}
	}
	return len(files), nil
}

func writeFile(fc *sftp.Client, src io.Reader, remote string) error {
	dst, err := fc.Create(remote)
	if err != nil {
		return err
	}
	defer dst.Close()
	_, err = dst.ReadFrom(src)
	return err
}

func copyFile(fc *sftp.Client, localAbs, remote string) error {
	src, err := os.Open(localAbs)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := fc.Create(remote)
	if err != nil {
		return err
	}
	defer dst.Close()
	if _, err := dst.ReadFrom(src); err != nil {
		return err
	}
	return nil
}
