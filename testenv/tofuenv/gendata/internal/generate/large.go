package generate

import (
	"encoding/binary"
	"fmt"
	"io"
	"path"
)

// LargeDir is the folder the large files go into, under the bucket prefix and
// the VM data path. It sits beside the dummy set's dir_* tree, never inside it,
// so adding large files to an existing dataset touches none of its files.
const LargeDir = "large"

// Large is one large file: where it goes, how big it is, and the seed its bytes
// come from.
//
// Unlike a dummy File it has no local copy. N files of 10 GB would need N × 10 GB
// of temp space just to be read back once, so the bytes are produced while they
// are sent instead - see Reader.
type Large struct {
	Rel  string
	Size int64
	Seed int64
}

// PlanLarge names n files of mb MiB each: large/large_<mb>mb_<i>.bin. The size
// is part of the name, so files of different sizes can be added to the same
// folder, and part of the seed, so they do not share their leading bytes either.
func PlanLarge(mb, n int) []Large {
	if mb <= 0 || n <= 0 {
		return nil
	}
	out := make([]Large, n)
	for i := range out {
		out[i] = Large{
			Rel:  path.Join(LargeDir, fmt.Sprintf("large_%dmb_%d.bin", mb, i)),
			Size: int64(mb) * MiB,
			Seed: int64(mb)<<32 | int64(i),
		}
	}
	return out
}

// LargeBytes is how much the files take in all.
func LargeBytes(files []Large) int64 {
	var n int64
	for _, f := range files {
		n += f.Size
	}
	return n
}

// Reader returns the file's content. onRead, when set, is called with every
// chunk read, so a 10 GB file can report progress before it is done.
//
// The reader has ReadAt and Size as well as Read, and both consumers depend on
// it: minio-go uploads the parts of a ReaderAt in parallel and those of a plain
// Reader one by one, and pkg/sftp only pipelines its writes when it can tell the
// size up front.
func (l Large) Reader(onRead func(n int64)) io.Reader {
	key := mix64(uint64(l.Seed) + 1)
	return &largeReader{SectionReader: io.NewSectionReader(largeData{key}, 0, l.Size), onRead: onRead}
}

type largeReader struct {
	*io.SectionReader
	onRead func(n int64)
}

func (r *largeReader) Read(p []byte) (int, error) {
	n, err := r.SectionReader.Read(p)
	r.report(n)
	return n, err
}

func (r *largeReader) ReadAt(p []byte, off int64) (int, error) {
	n, err := r.SectionReader.ReadAt(p, off)
	r.report(n)
	return n, err
}

func (r *largeReader) report(n int) {
	if r.onRead != nil && n > 0 {
		r.onRead(int64(n))
	}
}

// largeData is an endless run of pseudo-random bytes: the 8 bytes at word w are
// mix64(key + w×φ), splitmix64. Any offset can be produced without the bytes
// before it, which is what lets ReadAt serve parallel parts, and the same key
// gives the same bytes every time - to the bucket and the VM alike, so a
// migrated copy can be checked against either. Like the png and zip noise, it
// does not compress, so the size that lands is the size asked for.
type largeData struct{ key uint64 }

func (d largeData) ReadAt(p []byte, off int64) (int, error) {
	var b [8]byte
	w := uint64(off) / 8
	n := 0
	if r := int(off % 8); r != 0 {
		binary.LittleEndian.PutUint64(b[:], d.word(w))
		n = copy(p, b[r:])
		w++
	}
	for ; n+8 <= len(p); n += 8 {
		binary.LittleEndian.PutUint64(p[n:], d.word(w))
		w++
	}
	if n < len(p) {
		binary.LittleEndian.PutUint64(b[:], d.word(w))
		n += copy(p[n:], b[:])
	}
	return n, nil
}

func (d largeData) word(w uint64) uint64 {
	return mix64(d.key + w*0x9E3779B97F4A7C15)
}

// mix64 is the splitmix64 finalizer.
func mix64(z uint64) uint64 {
	z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
	z = (z ^ (z >> 27)) * 0x94D049BB133111EB
	return z ^ (z >> 31)
}
