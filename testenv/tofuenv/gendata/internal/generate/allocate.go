package generate

import (
	"fmt"
	"sort"
	"strings"
)

// ImageMaxMB caps one png or gif file. Both encoders build the whole image in
// memory before writing it, so a file's size is roughly the RAM it takes; the
// text and zip writers stream and need no cap.
const ImageMaxMB = 20

// formatOrder is the order formats are ranked in when sizes tie, and the order
// they are generated in. It matches config.DummyFormats.
var formatOrder = []string{"csv", "txt", "sql", "json", "xml", "png", "gif", "zip"}

func isImage(format string) bool { return format == "png" || format == "gif" }

// FormatAlloc is how one format is generated: Files files, Bytes in all.
type FormatAlloc struct {
	Format string `json:"format"`
	Files  int    `json:"files"`
	Bytes  int64  `json:"bytes"`
}

// FileBytes is the size of file i, the total spread as evenly as whole bytes
// allow.
func (f FormatAlloc) FileBytes(i int) int64 {
	n := f.Bytes / int64(f.Files)
	if int64(i) < f.Bytes%int64(f.Files) {
		n++
	}
	return n
}

// Allocation is the file plan for one run: which formats get how many files of
// what size, and which formats a file limit squeezed out.
type Allocation struct {
	MaxFiles int           `json:"maxFiles"`
	Formats  []FormatAlloc `json:"formats"`
	Skipped  []string      `json:"skipped,omitempty"`
}

// TotalFiles is how many files the run generates.
func (a Allocation) TotalFiles() int {
	n := 0
	for _, f := range a.Formats {
		n += f.Files
	}
	return n
}

// TotalBytes is how much the run generates.
func (a Allocation) TotalBytes() int64 {
	var n int64
	for _, f := range a.Formats {
		n += f.Bytes
	}
	return n
}

// String renders the plan for the run log: "csv=125×~260 MiB png=125×~20 MiB".
func (a Allocation) String() string {
	if len(a.Formats) == 0 {
		return "all formats off"
	}
	parts := make([]string, 0, len(a.Formats))
	for _, f := range a.Formats {
		parts = append(parts, fmt.Sprintf("%s=%d×~%.1f MiB", f.Format, f.Files,
			float64(f.Bytes)/float64(f.Files)/MiB))
	}
	return strings.Join(parts, " ")
}

// Allocate turns the per-format sizes in opts into a file plan, honouring
// opts.MaxFiles (0 = no limit) and the ImageMaxMB cap.
//
// Under the limit every MB is one ~1 MiB file, as it always was. Over it, the
// total is kept and the files grow instead:
//
//  1. More formats on than files allowed: the largest formats keep one file
//     each and the rest are skipped, their size spread over the kept ones.
//  2. png and gif first get the files that keep each under ImageMaxMB, every
//     other format one. The files left are handed out one at a time to the
//     format whose files are currently largest, which evens file sizes out -
//     the same as splitting the files in proportion to size.
//  3. When the limit cannot cover what png and gif need, every format gets its
//     proportional share instead, png and gif are cut to share × ImageMaxMB, and
//     what that cuts moves to the other formats. With no other format on, the
//     total shrinks.
func Allocate(opts Options) Allocation {
	a := Allocation{MaxFiles: opts.MaxFiles}
	sizes := map[string]int{
		"csv": opts.SizeCSV, "txt": opts.SizeTXT, "sql": opts.SizeSQL, "json": opts.SizeJSON,
		"xml": opts.SizeXML, "png": opts.SizePNG, "gif": opts.SizeGIF, "zip": opts.SizeZIP,
	}
	var on []string
	total := 0
	for _, f := range formatOrder {
		if sizes[f] > 0 {
			on = append(on, f)
			total += sizes[f]
		}
	}

	files := map[string]int{}
	if opts.MaxFiles <= 0 || total <= opts.MaxFiles {
		for _, f := range on {
			files[f] = sizes[f]
		}
		return a.build(on, sizes, files)
	}
	limit := opts.MaxFiles

	// 1. One file per format at least, so a limit below the number of formats
	//    drops the smallest; ties go by formatOrder, which sort.SliceStable keeps.
	if len(on) > limit {
		ranked := append([]string(nil), on...)
		sort.SliceStable(ranked, func(i, j int) bool { return sizes[ranked[i]] > sizes[ranked[j]] })
		keep := map[string]bool{}
		for _, f := range ranked[:limit] {
			keep[f] = true
		}
		freed := 0
		var kept []string
		for _, f := range on {
			if keep[f] {
				kept = append(kept, f)
			} else {
				a.Skipped = append(a.Skipped, f)
				freed += sizes[f]
				sizes[f] = 0
			}
		}
		spread(sizes, kept, freed)
		on = kept
	}

	// 2. What each format needs as a minimum.
	need := 0
	for _, f := range on {
		files[f] = 1
		if isImage(f) {
			files[f] = (sizes[f] + ImageMaxMB - 1) / ImageMaxMB
		}
		need += files[f]
	}

	if need <= limit {
		fill(sizes, files, on, limit-need)
		return a.build(on, sizes, files)
	}

	// 3. png and gif cannot all fit under the cap: proportional shares for all,
	//    then cut png and gif down to what their share can hold.
	for _, f := range on {
		files[f] = 1
	}
	fill(sizes, files, on, limit-len(on))
	cut := 0
	var others []string
	for _, f := range on {
		if !isImage(f) {
			others = append(others, f)
			continue
		}
		if max := files[f] * ImageMaxMB; sizes[f] > max {
			cut += sizes[f] - max
			sizes[f] = max
		}
	}
	spread(sizes, others, cut)
	return a.build(on, sizes, files)
}

// fill hands out n more files, one at a time, to the format whose files are
// largest right now, never letting a file drop below 1 MB.
func fill(sizes, files map[string]int, on []string, n int) {
	for ; n > 0; n-- {
		best := ""
		for _, f := range on {
			if files[f] >= sizes[f] {
				continue
			}
			// sizes[f]/files[f] > sizes[best]/files[best], without division.
			if best == "" || sizes[f]*files[best] > sizes[best]*files[f] {
				best = f
			}
		}
		if best == "" {
			return
		}
		files[best]++
	}
}

// spread adds mb to formats, 1 MB at a time in their order.
func spread(sizes map[string]int, formats []string, mb int) {
	if len(formats) == 0 {
		return
	}
	for i, f := range formats {
		sizes[f] += mb / len(formats)
		if i < mb%len(formats) {
			sizes[f]++
		}
	}
}

func (a Allocation) build(on []string, sizes, files map[string]int) Allocation {
	for _, f := range on {
		if files[f] > 0 && sizes[f] > 0 {
			a.Formats = append(a.Formats, FormatAlloc{Format: f, Files: files[f], Bytes: int64(sizes[f]) * MiB})
		}
	}
	return a
}
