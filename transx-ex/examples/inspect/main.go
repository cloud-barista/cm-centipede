package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/cloud-barista/cm-centipede/transx-ex"
)

func main() {
	var configFile string
	var format string
	var verbose bool
	var showExcluded bool

	flag.StringVar(&configFile, "config", "", "StorageLocation configuration JSON file path (required)")
	flag.StringVar(&format, "format", "table", "Output format: table | json")
	flag.BoolVar(&verbose, "verbose", false, "Enable verbose logging")
	flag.BoolVar(&showExcluded, "excluded", true,
		"List the folders the filter removed (costs a second, unfiltered scan)")
	flag.Parse()

	if configFile == "" {
		fmt.Fprintln(os.Stderr, "Error: -config is required")
		flag.Usage()
		os.Exit(1)
	}

	raw, err := os.ReadFile(configFile)
	if err != nil {
		log.Fatalf("Error reading config: %v", err)
	}

	kind, err := configKind(raw)
	if err != nil {
		log.Fatalf("Error loading config: %v", err)
	}

	startTime := time.Now()

	switch kind {
	case "filesystem":
		loc, err := decodeConfig[transxex.StorageLocation](raw)
		if err != nil {
			log.Fatalf("Error loading config: %v", err)
		}
		if verbose {
			fmt.Printf("Inspecting filesystem: %s\n", loc.Path)
		}
		result, err := transxex.InspectFilesystem(loc)
		if err != nil {
			log.Fatalf("InspectFilesystem failed: %v", err)
		}
		// The filtered scan only returns what survived, so listing what the
		// filter removed takes a second pass with the filter dropped.
		filtering := isFiltering(loc.Filter)
		var unfiltered *transxex.FSInspectResult
		if showExcluded && filtering {
			bare := loc
			bare.Filter = nil
			if verbose {
				fmt.Println("Re-scanning without the filter to list excluded folders")
			}
			unfiltered, err = transxex.InspectFilesystem(bare)
			if err != nil {
				log.Fatalf("InspectFilesystem (unfiltered) failed: %v", err)
			}
		}
		if err := printFSResult(result, unfiltered, filtering, format); err != nil {
			log.Fatalf("Output error: %v", err)
		}
		if verbose {
			fmt.Printf("Found %d directories in %s\n", len(result.Folders), time.Since(startTime))
		}

	case "objectstorage":
		loc, err := decodeConfig[transxex.StorageLocation](raw)
		if err != nil {
			log.Fatalf("Error loading config: %v", err)
		}
		if verbose {
			fmt.Printf("Inspecting object storage: %s\n", loc.Path)
		}
		result, err := transxex.InspectObjectStorage(loc)
		if err != nil {
			log.Fatalf("InspectObjectStorage failed: %v", err)
		}
		filtering := isFiltering(loc.Filter)
		var unfiltered *transxex.OSInspectResult
		if showExcluded && filtering {
			bare := loc
			bare.Filter = nil
			if verbose {
				fmt.Println("Re-scanning without the filter to list excluded prefixes")
			}
			unfiltered, err = transxex.InspectObjectStorage(bare)
			if err != nil {
				log.Fatalf("InspectObjectStorage (unfiltered) failed: %v", err)
			}
		}
		if err := printOSResult(result, unfiltered, filtering, format); err != nil {
			log.Fatalf("Output error: %v", err)
		}
		if verbose {
			fmt.Printf("Found %d folders in %s\n", len(result.Folders), time.Since(startTime))
		}

	case "dbms":
		loc, err := decodeConfig[transxex.DBMSLocation](raw)
		if err != nil {
			log.Fatalf("Error loading config: %v", err)
		}
		if verbose {
			fmt.Printf("Inspecting database: %s/%s\n", loc.DBMSType, loc.Database)
		}
		result, err := transxex.InspectDBMS(loc)
		if err != nil {
			log.Fatalf("InspectDBMS failed: %v", err)
		}
		if err := printDBMSResult(result, format); err != nil {
			log.Fatalf("Output error: %v", err)
		}
		if verbose {
			fmt.Printf("Found %d tables in %s\n", len(result.Tables), time.Since(startTime))
		}

	default:
		log.Fatalf("Unsupported storageType: %q (use filesystem or objectstorage)", kind)
	}
}

// configKind reports which inspect function the config selects: its storageType
// ("filesystem" or "objectstorage") for a StorageLocation, or "dbms" when the
// config carries a dbmsType and is therefore a DBMSLocation.
func configKind(raw []byte) (string, error) {
	var probe struct {
		StorageType string `json:"storageType"`
		DBMSType    string `json:"dbmsType"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return "", fmt.Errorf("failed to decode JSON: %w", err)
	}
	if probe.DBMSType != "" {
		return "dbms", nil
	}
	if probe.StorageType == "" {
		return "", fmt.Errorf("config declares neither storageType nor dbmsType")
	}
	return probe.StorageType, nil
}

// isFiltering reports whether f would actually remove anything from the folder
// listing. A nil or empty filter leaves nothing to re-scan for.
func isFiltering(f *transxex.PathFilterOption) bool {
	if f == nil {
		return false
	}
	return len(f.Rules) > 0 || len(f.Include) > 0 || len(f.Exclude) > 0 || f.MaxDepth > 0
}

func decodeConfig[T any](raw []byte) (T, error) {
	var loc T
	if err := json.Unmarshal(raw, &loc); err != nil {
		return loc, fmt.Errorf("failed to decode JSON: %w", err)
	}
	return loc, nil
}

// printFSResult renders the scan r, which was filtered when filtered is set.
// When unf is non-nil it is the same scan run without the filter, and supplies
// the FILTERED block.
func printFSResult(r, unf *transxex.FSInspectResult, filtered bool, format string) error {
	switch format {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(r)
	default:
		fmt.Printf("PATH:   %s\n", r.Path)
		fmt.Printf("MOUNT:  %t\n", r.IsMount)

		// TOTAL is the original: every file under Path, filter and maxDepth
		// ignored. Requested aggregates only.
		if r.TotalSize != nil || r.FileCount != nil || len(r.ExtensionCounts) > 0 {
			fmt.Println()
			fmt.Println("TOTAL (original — whole path, filter and maxDepth ignored)")
			if r.TotalSize != nil {
				fmt.Printf("  SIZE:    %d bytes\n", *r.TotalSize)
			}
			if r.FileCount != nil {
				fmt.Printf("  FILES:   %d\n", *r.FileCount)
			}
			if len(r.ExtensionCounts) > 0 {
				fmt.Printf("  EXT:     %s\n", formatExtCounts(r.ExtensionCounts))
			}
		}

		// FILTERED: the folders present in the unfiltered scan but absent from
		// the filtered one — what the filter actually removed.
		if unf != nil {
			excluded := excludedFolders(r.Folders, unf.Folders)
			files, size := sumFSFolders(excluded)
			fmt.Println()
			fmt.Printf("FILTERED (removed by the filter — %s%s)\n",
				plural(len(excluded), "folder"), fsCountSuffix(excluded, files, size))
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			for _, e := range excluded {
				if e.FileCount == nil && e.FileSize == nil {
					fmt.Fprintf(w, "  %s\n", e.Path)
					continue
				}
				fmt.Fprintf(w, "  %s\t%s\t%s\n", e.Path, intPtr(e.FileCount), int64Ptr(e.FileSize))
			}
			if err := w.Flush(); err != nil {
				return err
			}
		}

		// RESULT: what the folder table below adds up to.
		listedFiles, listedSize := sumFSFolders(r.Folders)
		fmt.Println()
		fmt.Printf("RESULT (%s%s%s)\n", filteredNote(filtered),
			plural(len(r.Folders), "folder"), fsCountSuffix(r.Folders, listedFiles, listedSize))
		fmt.Println()

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "PATH\tMOUNT\tFILES\tFSIZE\tMOD_TIME\tPERMISSIONS")
		for _, e := range r.Folders {
			fmt.Fprintf(w, "%s\t%t\t%s\t%s\t%s\t%s\n",
				e.Path, e.IsMount, intPtr(e.FileCount), int64Ptr(e.FileSize), e.ModTime, e.Permissions)
		}
		return w.Flush()
	}
}

// excludedFolders returns the entries of all that kept does not contain.
func excludedFolders(kept, all []transxex.FSEntry) []transxex.FSEntry {
	keptPaths := make(map[string]struct{}, len(kept))
	for _, e := range kept {
		keptPaths[e.Path] = struct{}{}
	}
	var out []transxex.FSEntry
	for _, e := range all {
		if _, ok := keptPaths[e.Path]; !ok {
			out = append(out, e)
		}
	}
	return out
}

func sumFSFolders(entries []transxex.FSEntry) (files int, size int64) {
	for _, e := range entries {
		if e.FileCount != nil {
			files += *e.FileCount
		}
		if e.FileSize != nil {
			size += *e.FileSize
		}
	}
	return files, size
}

// fsCountSuffix appends the file totals to a block header, but only when the
// per-folder metrics that carry them were requested.
func fsCountSuffix(entries []transxex.FSEntry, files int, size int64) string {
	for _, e := range entries {
		if e.FileCount != nil || e.FileSize != nil {
			return fmt.Sprintf(", %s, %d bytes", plural(files, "file"), size)
		}
	}
	return ""
}

// filteredNote prefixes the RESULT header only when a filter was applied, so an
// unfiltered scan does not claim to be a filtering result.
func filteredNote(filtered bool) string {
	if filtered {
		return "after filtering — "
	}
	return ""
}

func plural(n int, unit string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, unit)
	}
	return fmt.Sprintf("%d %ss", n, unit)
}

// formatExtCounts renders an extension histogram in a stable order.
func formatExtCounts(counts map[string]int) string {
	exts := make([]string, 0, len(counts))
	for e := range counts {
		exts = append(exts, e)
	}
	sort.Strings(exts)
	parts := make([]string, len(exts))
	for i, e := range exts {
		parts[i] = fmt.Sprintf("%s=%d", e, counts[e])
	}
	return strings.Join(parts, ", ")
}

// intPtr / int64Ptr render an optional metric value, or "-" when not requested.
func intPtr(p *int) string {
	if p == nil {
		return "-"
	}
	return strconv.Itoa(*p)
}

func int64Ptr(p *int64) string {
	if p == nil {
		return "-"
	}
	return strconv.FormatInt(*p, 10)
}

// lenOrDash renders the length of an opt-in inventory, or "-" when the metric
// that fills it was not requested (nil slice).
func lenOrDash[T any](s []T) string {
	if s == nil {
		return "-"
	}
	return strconv.Itoa(len(s))
}

// printOSResult renders the scan r, which was filtered when filtered is set.
// When unf is non-nil it is the same scan run without the filter, and supplies
// the FILTERED block.
func printOSResult(r, unf *transxex.OSInspectResult, filtered bool, format string) error {
	switch format {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(r)
	default:
		fmt.Printf("PATH:   %s\n", r.Path)

		// TOTAL is the original: every object under Path, filter ignored.
		if r.TotalSize != nil || r.ObjectCount != nil || len(r.ExtensionCounts) > 0 {
			fmt.Println()
			fmt.Println("TOTAL (original — whole prefix, filter and maxDepth ignored)")
			if r.TotalSize != nil {
				fmt.Printf("  SIZE:     %d bytes\n", *r.TotalSize)
			}
			if r.ObjectCount != nil {
				fmt.Printf("  OBJECTS:  %d\n", *r.ObjectCount)
			}
			if len(r.ExtensionCounts) > 0 {
				fmt.Printf("  EXT:      %s\n", formatExtCounts(r.ExtensionCounts))
			}
		}

		if unf != nil {
			excluded := excludedPrefixes(r.Folders, unf.Folders)
			objects, size := sumOSPrefixes(excluded)
			fmt.Println()
			fmt.Printf("FILTERED (removed by the filter — %s%s)\n",
				plural(len(excluded), "prefix"), osCountSuffix(excluded, objects, size))
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			for _, e := range excluded {
				if e.ObjectCount == nil && e.Size == nil {
					fmt.Fprintf(w, "  %s\n", e.Key)
					continue
				}
				fmt.Fprintf(w, "  %s\t%s\t%s\n", e.Key, intPtr(e.ObjectCount), int64Ptr(e.Size))
			}
			if err := w.Flush(); err != nil {
				return err
			}
		}

		listedObjects, listedSize := sumOSPrefixes(r.Folders)
		fmt.Println()
		fmt.Printf("RESULT (%s%s%s)\n", filteredNote(filtered),
			plural(len(r.Folders), "prefix"), osCountSuffix(r.Folders, listedObjects, listedSize))
		fmt.Println()

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "KEY\tOBJECTS\tSIZE\tLAST_MODIFIED")
		for _, e := range r.Folders {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
				e.Key, intPtr(e.ObjectCount), int64Ptr(e.Size), e.LastModified)
		}
		return w.Flush()
	}
}

// excludedPrefixes returns the entries of all that kept does not contain.
func excludedPrefixes(kept, all []transxex.OSEntry) []transxex.OSEntry {
	keptKeys := make(map[string]struct{}, len(kept))
	for _, e := range kept {
		keptKeys[e.Key] = struct{}{}
	}
	var out []transxex.OSEntry
	for _, e := range all {
		if _, ok := keptKeys[e.Key]; !ok {
			out = append(out, e)
		}
	}
	return out
}

func sumOSPrefixes(entries []transxex.OSEntry) (objects int, size int64) {
	for _, e := range entries {
		if e.ObjectCount != nil {
			objects += *e.ObjectCount
		}
		if e.Size != nil {
			size += *e.Size
		}
	}
	return objects, size
}

func osCountSuffix(entries []transxex.OSEntry, objects int, size int64) string {
	for _, e := range entries {
		if e.ObjectCount != nil || e.Size != nil {
			return fmt.Sprintf(", %s, %d bytes", plural(objects, "object"), size)
		}
	}
	return ""
}

func printDBMSResult(r *transxex.DBMSInfo, format string) error {
	switch format {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(r)
	default:
		// Summary: server, database and the always-collected size aggregates.
		fmt.Printf("DATABASE:     %s\n", r.Database)
		fmt.Printf("SERVER:       %s\n", r.ServerVersion)
		fmt.Printf("TOTAL_SIZE:   %d bytes (data %d, index %d)\n", r.TotalSize, r.DataSize, r.IndexSize)
		fmt.Printf("TOTAL_TABLES: %d\n", r.TableCount)

		// Schema-object inventories — printed only when the metric requested
		// them; engine-specific ones stay nil elsewhere.
		inventories := []struct {
			label string
			names []string
		}{
			{"FKEYS", r.ForeignKeys},
			{"VIEWS", r.Views},
			{"MVIEWS", r.MaterializedViews},
			{"FUNCS", r.Functions},
			{"PROCS", r.Procedures},
			{"TRIGGERS", r.Triggers},
			{"EVENTS", r.Events},
			{"SEQS", r.Sequences},
			{"TYPES", r.Types},
			{"EXTS", r.Extensions},
			{"RULES", r.Rules},
			{"VALIDS", r.Validators},
		}
		for _, inv := range inventories {
			if inv.names == nil {
				continue
			}
			fmt.Printf("%-9s %s\n", inv.label+":", strings.Join(inv.names, ", "))
		}
		fmt.Println()

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tROWS\tDATA\tINDEX\tTOTAL\tENGINE\tCOLUMNS\tINDEXES")
		for _, t := range r.Tables {
			fmt.Fprintf(w, "%s\t%d\t%d\t%d\t%d\t%s\t%s\t%s\n",
				t.Name, t.RowCount, t.DataSize, t.IndexSize, t.TotalSize, t.Engine,
				lenOrDash(t.Columns), lenOrDash(t.Indexes))
		}
		return w.Flush()
	}
}
