// Package generate produces local dummy files at MB granularity using gofakeit,
// which the bucket and filesystem targets then upload/transfer.
//
// Size unit: each config value is a count of megabytes (MiB). Allocate turns
// those into a file plan: N MiB is N files of ~1 MiB, unless a file limit makes
// the files fewer and larger (see Allocate). Binary formats (png/gif/zip)
// approximate the target size.
package generate

import (
	"archive/zip"
	"bufio"
	"encoding/json"
	"fmt"
	"image"
	"image/color/palette"
	"image/gif"
	"image/png"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"

	"github.com/brianvoe/gofakeit/v6"
)

// MiB is the per-file target size and the meaning of one config unit.
const MiB = 1 << 20

// Options mirrors config.json "dummy": per-format size in MB (0 = skip).
type Options struct {
	SizeSQL  int
	SizeCSV  int
	SizeJSON int
	SizeXML  int
	SizeTXT  int
	SizePNG  int
	SizeGIF  int
	SizeZIP  int

	// MaxFiles caps the number of files; 0 means no limit.
	MaxFiles int
}

// File is a generated file: absolute path plus its slash-relative path under the dir.
type File struct {
	Abs  string
	Rel  string
	Size int64
}

type writer func(path string, target int64, seed int64) error

// writers maps a format to the function that writes one file of it.
var writers = map[string]writer{
	"csv": writeCSV, "txt": writeTXT, "sql": writeSQL, "json": writeJSON,
	"xml": writeXML, "png": writePNG, "gif": writeGIF, "zip": writeZIP,
}

// Generate writes the files a planned into a fresh temp directory and returns
// the dir and a stably-sorted file list. The caller is responsible for removing
// the dir. The temp directory is created under root, which is made if it does
// not exist yet; an empty root means the system temp directory. onFile, when
// set, is called with the size of each file once it is written, so the caller
// can report progress; it is called from several goroutines at once.
//
// Files are written workers at a time (0 means one per CPU). Each file depends
// only on its own seed, so the result is the same whatever the count; the first
// failure stops the files not yet started.
func Generate(a Allocation, root string, workers int, onFile func(size int64)) (string, []File, error) {
	if root != "" {
		if err := os.MkdirAll(root, 0o755); err != nil {
			return "", nil, fmt.Errorf("create temp root: %w", err)
		}
	}
	dir, err := os.MkdirTemp(root, "gendata-")
	if err != nil {
		return "", nil, err
	}
	if workers < 1 {
		workers = runtime.NumCPU()
	}

	type job struct {
		format, path string
		size, seed   int64
	}
	jobs := make(chan job)
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
	)
	failed := func() bool {
		mu.Lock()
		defer mu.Unlock()
		return firstErr != nil
	}
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func() {
			defer wg.Done()
			for j := range jobs {
				if failed() {
					continue // drain: the run is already lost
				}
				if err := writers[j.format](j.path, j.size, j.seed); err != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = fmt.Errorf("generate %s: %w", j.format, err)
					}
					mu.Unlock()
					continue
				}
				if onFile != nil {
					if fi, err := os.Stat(j.path); err == nil {
						onFile(fi.Size())
					}
				}
			}
		}()
	}

feed:
	for _, f := range a.Formats {
		typeDir := filepath.Join(dir, f.Format)
		if err := os.MkdirAll(typeDir, 0o755); err != nil {
			mu.Lock()
			if firstErr == nil {
				firstErr = err
			}
			mu.Unlock()
			break
		}
		for i := 0; i < f.Files; i++ {
			if failed() {
				break feed
			}
			jobs <- job{
				format: f.Format,
				path:   filepath.Join(typeDir, fmt.Sprintf("%s_%d.%s", f.Format, i, f.Format)),
				size:   f.FileBytes(i),
				seed:   int64(i),
			}
		}
	}
	close(jobs)
	wg.Wait()
	if firstErr != nil {
		_ = os.RemoveAll(dir)
		return "", nil, firstErr
	}

	files, err := listFiles(dir)
	if err != nil {
		_ = os.RemoveAll(dir)
		return "", nil, err
	}
	return dir, files, nil
}

// --- text formats (gofakeit) -------------------------------------------------
//
// Each file draws from a Faker of its own, seeded with the file's seed, rather
// than from gofakeit's package-level source: that one is global, so files written
// at the same time would interleave their draws and no file would be the same
// twice. A Faker per file keeps every file reproducible however many are
// generated at once.

func writeCSV(path string, target, seed int64) error {
	fk := newFaker(seed)
	return textFile(path, func(w *bufio.Writer) (int, error) {
		return w.WriteString("id,first_name,last_name,email,city,age\n")
	}, func(w *bufio.Writer, i int) (int, error) {
		return w.WriteString(fmt.Sprintf("%d,%s,%s,%s,%s,%d\n",
			i, fk.FirstName(), fk.LastName(), fk.Email(),
			fk.City(), fk.Number(18, 80)))
	}, nil, target)
}

func writeSQL(path string, target, seed int64) error {
	fk := newFaker(seed)
	header := "CREATE TABLE IF NOT EXISTS gendata_people (\n" +
		"  id INT PRIMARY KEY, first_name VARCHAR(100), last_name VARCHAR(100),\n" +
		"  email VARCHAR(200), city VARCHAR(100), age INT\n);\n"
	return textFile(path, func(w *bufio.Writer) (int, error) {
		return w.WriteString(header)
	}, func(w *bufio.Writer, i int) (int, error) {
		return w.WriteString(fmt.Sprintf(
			"INSERT INTO gendata_people (id,first_name,last_name,email,city,age) VALUES (%d,'%s','%s','%s','%s',%d);\n",
			i, fk.FirstName(), fk.LastName(), fk.Email(), fk.City(), fk.Number(18, 80)))
	}, nil, target)
}

func writeTXT(path string, target, seed int64) error {
	fk := newFaker(seed)
	return textFile(path, nil, func(w *bufio.Writer, _ int) (int, error) {
		return w.WriteString(fk.Paragraph(1, 5, 12, " ") + "\n")
	}, nil, target)
}

type jsonRec struct {
	ID    int    `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
	City  string `json:"city"`
	Age   int    `json:"age"`
}

func writeJSON(path string, target, seed int64) error {
	fk := newFaker(seed)
	first := true
	return textFile(path, func(w *bufio.Writer) (int, error) {
		return w.WriteString("[\n")
	}, func(w *bufio.Writer, i int) (int, error) {
		b, err := json.Marshal(jsonRec{i, fk.Name(), fk.Email(), fk.City(), fk.Number(18, 80)})
		if err != nil {
			return 0, err
		}
		sep := ",\n"
		if first {
			sep = ""
			first = false
		}
		return w.WriteString(sep + "  " + string(b))
	}, func(w *bufio.Writer) (int, error) {
		return w.WriteString("\n]\n")
	}, target)
}

func writeXML(path string, target, seed int64) error {
	fk := newFaker(seed)
	return textFile(path, func(w *bufio.Writer) (int, error) {
		return w.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<records>\n")
	}, func(w *bufio.Writer, i int) (int, error) {
		return w.WriteString(fmt.Sprintf(
			"  <record><id>%d</id><name>%s</name><email>%s</email><city>%s</city><age>%d</age></record>\n",
			i, fk.Name(), fk.Email(), fk.City(), fk.Number(18, 80)))
	}, func(w *bufio.Writer) (int, error) {
		return w.WriteString("</records>\n")
	}, target)
}

// newFaker returns the Faker a text file draws from. gofakeit takes seed 0 to
// mean "seed from a random source", which made the first file of every text
// format different on every run; the seed is shifted by one so every file,
// _0 included, is the same each time.
func newFaker(seed int64) *gofakeit.Faker {
	return gofakeit.New(seed + 1)
}

// textFile writes head, then row(i) until the file reaches target bytes, then foot.
func textFile(path string,
	head func(*bufio.Writer) (int, error),
	row func(*bufio.Writer, int) (int, error),
	foot func(*bufio.Writer) (int, error),
	target int64) (err error) {

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}()
	w := bufio.NewWriterSize(f, 64<<10)

	var written int64
	if head != nil {
		n, herr := head(w)
		if herr != nil {
			return herr
		}
		written += int64(n)
	}
	// Reserve a little room for the footer so we stay near target.
	limit := target
	for written < limit {
		n, rerr := row(w, int(written)) // int(written) just a varying id seed
		if rerr != nil {
			return rerr
		}
		written += int64(n)
	}
	if foot != nil {
		if _, ferr := foot(w); ferr != nil {
			return ferr
		}
	}
	return w.Flush()
}

// --- binary formats (approximate target size) --------------------------------

func writePNG(path string, target, seed int64) (err error) {
	rng := rand.New(rand.NewSource(seed))
	side := isqrt(target / 4) // ~4 bytes/pixel (RGBA), noise ≈ incompressible
	img := image.NewRGBA(image.Rect(0, 0, side, side))
	_, _ = rng.Read(img.Pix)
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}()
	// Noise does not compress, so deflating it only burns CPU - several times the
	// cost of writing the file - for a file of the same size. Stored blocks keep
	// the size where the target put it and the PNG just as valid.
	enc := png.Encoder{CompressionLevel: png.NoCompression}
	return enc.Encode(f, img)
}

func writeGIF(path string, target, seed int64) (err error) {
	rng := rand.New(rand.NewSource(seed))
	side := isqrt(target) // ~1 byte/pixel index
	img := image.NewPaletted(image.Rect(0, 0, side, side), palette.Plan9)
	_, _ = rng.Read(img.Pix) // Plan9 has 256 colors, every byte is a valid index
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}()
	return gif.Encode(f, img, nil)
}

func writeZIP(path string, target, seed int64) (err error) {
	rng := rand.New(rand.NewSource(seed))
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}()
	zw := zip.NewWriter(f)
	// Store (no compression) so the archive size ≈ target.
	e, err := zw.CreateHeader(&zip.FileHeader{Name: "data.bin", Method: zip.Store})
	if err != nil {
		return err
	}
	buf := make([]byte, 64<<10)
	var written int64
	for written < target {
		_, _ = rng.Read(buf)
		chunk := buf
		if rem := target - written; rem < int64(len(buf)) {
			chunk = buf[:rem]
		}
		n, werr := e.Write(chunk)
		if werr != nil {
			return werr
		}
		written += int64(n)
	}
	return zw.Close()
}

func isqrt(n int64) int {
	if n < 256 {
		n = 256
	}
	return int(math.Sqrt(float64(n)))
}

// --- listing -----------------------------------------------------------------

func listFiles(dir string) ([]File, error) {
	var out []File
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		out = append(out, File{Abs: p, Rel: filepath.ToSlash(rel), Size: fi.Size()})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Rel < out[j].Rel })
	return out, nil
}
