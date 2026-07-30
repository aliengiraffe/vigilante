package logging

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestParseSize(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"1", 1},
		{"1024", 1024},
		{"512B", 512},
		{"1KB", 1024},
		{"1kb", 1024},
		{"1KiB", 1024},
		{"500MB", 500 * 1024 * 1024},
		{" 500 MB ", 500 * 1024 * 1024},
		{"1GB", 1024 * 1024 * 1024},
		{"1GiB", 1024 * 1024 * 1024},
		{"1.5MB", 1024 * 1024 * 3 / 2},
		{"2G", 2 * 1024 * 1024 * 1024},
	}
	for _, tc := range cases {
		got, err := ParseSize(tc.in)
		if err != nil {
			t.Fatalf("ParseSize(%q) returned error: %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("ParseSize(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}

	for _, bad := range []string{"", "   ", "abc", "MB", "-5MB", "12XB", "1..2MB", "NaN", "nAn", "Inf", "+Inf", "-Inf"} {
		if got, err := ParseSize(bad); err == nil {
			t.Fatalf("ParseSize(%q) = %d, want error", bad, got)
		}
	}
}

func FuzzParseSize(f *testing.F) {
	for _, seed := range []string{"500MB", "1024", "1.5GiB", "", "-1", "MB", "999999999999999999999TB"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		size, err := ParseSize(raw)
		if err != nil {
			return
		}
		if size < 0 {
			t.Fatalf("ParseSize(%q) returned negative size %d", raw, size)
		}
	})
}

func TestFormatSize(t *testing.T) {
	cases := map[int64]string{
		500 * 1024 * 1024: "500MB",
		1024:              "1KB",
		1536:              "1536B",
		0:                 "0B",
	}
	for in, want := range cases {
		if got := FormatSize(in); got != want {
			t.Fatalf("FormatSize(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestWriterRotatesAtMaxFileSize(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vigilante.log")
	w := NewWriter(path, Limits{MaxFileSize: 64, MaxBackups: 2})
	t.Cleanup(func() { _ = w.Close() })

	line := strings.Repeat("a", 40) + "\n"
	for range 4 {
		if _, err := w.Write([]byte(line)); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat current: %v", err)
	}
	if info.Size() > 64 {
		t.Fatalf("current file size = %d, want <= 64", info.Size())
	}
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatalf("expected rotated backup: %v", err)
	}
}

func TestWriterEnforcesBackupCount(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vigilante.log")
	w := NewWriter(path, Limits{MaxFileSize: 16, MaxBackups: 2})
	t.Cleanup(func() { _ = w.Close() })

	for i := range 12 {
		if _, err := fmt.Fprintf(w, "line %02d padding\n", i); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatalf("expected .1 backup: %v", err)
	}
	if _, err := os.Stat(path + ".2"); err != nil {
		t.Fatalf("expected .2 backup: %v", err)
	}
	if _, err := os.Stat(path + ".3"); !os.IsNotExist(err) {
		t.Fatalf("expected .3 backup to be pruned, got err=%v", err)
	}
}

func TestWriterKeepsWritingAfterRotation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vigilante.log")
	w := NewWriter(path, Limits{MaxFileSize: 32, MaxBackups: 3})
	t.Cleanup(func() { _ = w.Close() })

	if _, err := w.Write([]byte(strings.Repeat("x", 40) + "\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := w.Write([]byte("after rotation\n")); err != nil {
		t.Fatalf("write after rotation: %v", err)
	}

	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read current: %v", err)
	}
	if !strings.Contains(string(current), "after rotation") {
		t.Fatalf("post-rotation line missing from current file, got %q", current)
	}
}

func TestWriterStreamingWritesFollowRotation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.log")
	streaming := NewWriter(path, Limits{MaxFileSize: 24, MaxBackups: 2})
	t.Cleanup(func() { _ = streaming.Close() })

	// The streaming writer holds the handle; a short-lived appender writes
	// through the same shared writer and must land in the same current file.
	if _, err := streaming.Write([]byte("stream line\n")); err != nil {
		t.Fatalf("stream write: %v", err)
	}
	if _, err := streaming.Write([]byte("rotate me now\n")); err != nil {
		t.Fatalf("stream write: %v", err)
	}
	if _, err := streaming.Write([]byte("post rotate\n")); err != nil {
		t.Fatalf("stream write: %v", err)
	}

	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read current: %v", err)
	}
	if !strings.Contains(string(current), "post rotate") {
		t.Fatalf("current file missing latest line: %q", current)
	}
}

func TestWriterCloseThenWriteReopens(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.log")
	w := NewWriter(path, DefaultLimits())

	if _, err := w.Write([]byte("first\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := w.Write([]byte("second\n")); err != nil {
		t.Fatalf("write after close: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got := string(data); !strings.Contains(got, "first") || !strings.Contains(got, "second") {
		t.Fatalf("expected both lines, got %q", got)
	}
}

func TestOpenWriterSharesWriterPerPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "shared.log")

	a := OpenWriter(path)
	b := OpenWriter(filepath.Join(dir, ".", "shared.log"))
	t.Cleanup(func() { _ = a.Close() })

	if a != b {
		t.Fatal("OpenWriter returned different writers for the same path")
	}
}

func TestWriterConcurrentWritesRotateSafely(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vigilante.log")
	w := NewWriter(path, Limits{MaxFileSize: 128, MaxBackups: 3, MaxTotalSize: 1 << 20})
	t.Cleanup(func() { _ = w.Close() })

	payload := strings.Repeat("p", 32)
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := range 50 {
				if _, err := fmt.Fprintf(w, "goroutine=%d line=%d payload=%s\n", g, i, payload); err != nil {
					t.Errorf("write: %v", err)
					return
				}
			}
		}(g)
	}
	wg.Wait()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat current: %v", err)
	}
	if info.Size() > 128 {
		t.Fatalf("current file size = %d, want <= 128", info.Size())
	}
}

func TestSweepReducesDirectoryToBudget(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "vigilante.log")
	writeSized(t, live, 100)
	writeSized(t, live+".1", 100)
	writeSized(t, live+".2", 100)
	writeSized(t, filepath.Join(dir, "owner-repo-issue-1.log"), 100)

	result, err := Sweep(dir, Limits{MaxTotalSize: 150}, []string{live})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if result.TotalAfter > 150 {
		t.Fatalf("TotalAfter = %d, want <= 150", result.TotalAfter)
	}
	if total := dirSize(t, dir); total > 150 {
		t.Fatalf("directory size after sweep = %d, want <= 150", total)
	}
	if _, err := os.Stat(live); err != nil {
		t.Fatalf("live log must survive sweep: %v", err)
	}
	if result.Reclaimed != result.TotalBefore-result.TotalAfter {
		t.Fatalf("Reclaimed = %d, want %d", result.Reclaimed, result.TotalBefore-result.TotalAfter)
	}
}

func TestSweepEvictsRotatedBackupsBeforeCompletedSessions(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "vigilante.log")
	completed := filepath.Join(dir, "owner-repo-issue-7.log")
	writeSized(t, live, 100)
	writeSized(t, live+".1", 100)
	writeSized(t, completed, 100)

	// Make the completed-session log the oldest file, so eviction order can only
	// be explained by the rotated-backup preference.
	old := time.Now().Add(-72 * time.Hour)
	if err := os.Chtimes(completed, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	if _, err := Sweep(dir, Limits{MaxTotalSize: 250}, []string{live}); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if _, err := os.Stat(live + ".1"); !os.IsNotExist(err) {
		t.Fatalf("expected rotated backup to be evicted first, got err=%v", err)
	}
	if _, err := os.Stat(completed); err != nil {
		t.Fatalf("completed-session log evicted too early: %v", err)
	}
}

func TestSweepNeverEvictsLivePaths(t *testing.T) {
	dir := t.TempDir()
	daemonLog := filepath.Join(dir, "vigilante.log")
	accessLog := filepath.Join(dir, "access.jsonl")
	running := filepath.Join(dir, "owner-repo-issue-9.log")
	for _, path := range []string{daemonLog, accessLog, running} {
		writeSized(t, path, 500)
	}

	// Budget far below the live total: the sweep must give up rather than delete
	// a file an active writer owns.
	result, err := Sweep(dir, Limits{MaxTotalSize: 10}, []string{daemonLog, accessLog, running})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(result.Removed) != 0 {
		t.Fatalf("expected no evictions, removed %v", result.Removed)
	}
	for _, path := range []string{daemonLog, accessLog, running} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("live log %s was evicted: %v", path, err)
		}
	}
}

func TestSweepEvictsOldestRotatedGenerationFirst(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "vigilante.log")
	writeSized(t, live, 10)
	writeSized(t, live+".1", 100)
	writeSized(t, live+".2", 100)

	if _, err := Sweep(dir, Limits{MaxTotalSize: 120}, []string{live}); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if _, err := os.Stat(live + ".2"); !os.IsNotExist(err) {
		t.Fatalf("expected the oldest generation .2 to be evicted, got err=%v", err)
	}
	if _, err := os.Stat(live + ".1"); err != nil {
		t.Fatalf("newest backup .1 evicted too early: %v", err)
	}
}

func TestSweepNoOpForMissingOrEmptyDir(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent")
	result, err := Sweep(missing, DefaultLimits(), nil)
	if err != nil {
		t.Fatalf("sweep on missing dir: %v", err)
	}
	if result.TotalBefore != 0 || len(result.Removed) != 0 {
		t.Fatalf("unexpected result for missing dir: %+v", result)
	}

	empty := t.TempDir()
	result, err = Sweep(empty, DefaultLimits(), nil)
	if err != nil {
		t.Fatalf("sweep on empty dir: %v", err)
	}
	if result.TotalBefore != 0 || len(result.Removed) != 0 {
		t.Fatalf("unexpected result for empty dir: %+v", result)
	}
}

func TestSweepDisabledWhenBudgetNotPositive(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "owner-repo-issue-1.log")
	writeSized(t, path, 100)

	result, err := Sweep(dir, Limits{MaxTotalSize: 0}, nil)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(result.Removed) != 0 {
		t.Fatalf("expected no evictions with an unlimited budget, removed %v", result.Removed)
	}
}

func TestBackupIndexDoesNotMatchSessionLogNames(t *testing.T) {
	for _, name := range []string{"vigilante.log", "access.jsonl", "owner-repo-issue-472.log", "owner-repo2-issue-3.log"} {
		if idx := backupIndex(name); idx != 0 {
			t.Fatalf("backupIndex(%q) = %d, want 0", name, idx)
		}
	}
	for name, want := range map[string]int{
		"vigilante.log.1":            1,
		"access.jsonl.12":            12,
		"owner-repo-issue-472.log.3": 3,
	} {
		if idx := backupIndex(name); idx != want {
			t.Fatalf("backupIndex(%q) = %d, want %d", name, idx, want)
		}
	}
}

func writeSized(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.WriteFile(path, []byte(strings.Repeat("z", size)), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func dirSize(t *testing.T, dir string) int64 {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	var total int64
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}
		total += info.Size()
	}
	return total
}

// Append must not leave a descriptor open, or a daemon that touches one session
// log per issue would leak a file handle per issue for its whole lifetime.
func TestAppendReleasesFileHandleButWriteKeepsIt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "owner-repo-issue-1.log")
	w := NewWriter(path, DefaultLimits())
	t.Cleanup(func() { _ = w.Close() })

	if _, err := w.Append([]byte("one shot\n")); err != nil {
		t.Fatalf("append: %v", err)
	}
	if w.hasOpenFile() {
		t.Fatal("Append left the file handle open")
	}

	if _, err := w.Write([]byte("streamed\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if !w.hasOpenFile() {
		t.Fatal("Write should keep the file handle open for the next write")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); got != "one shot\nstreamed\n" {
		t.Fatalf("unexpected contents %q", got)
	}
}

// The streaming writer and one-shot appenders must never disagree about which
// file is current, no matter which of them triggers the rotation.
func TestAppendAndWriteStayOnTheSameCurrentFileAcrossRotation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "owner-repo-issue-1.log")
	limits := Limits{MaxFileSize: 64, MaxBackups: 3, MaxTotalSize: 1 << 20}
	w := NewWriter(path, limits)
	t.Cleanup(func() { _ = w.Close() })

	for i := range 12 {
		if _, err := fmt.Fprintf(w, "streamed %02d padding\n", i); err != nil {
			t.Fatalf("write: %v", err)
		}
		if _, err := w.Append([]byte(fmt.Sprintf("appended %02d padding\n", i))); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	if _, err := fmt.Fprintf(w, "last streamed\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := w.Append([]byte("last appended\n")); err != nil {
		t.Fatalf("append: %v", err)
	}

	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(current)
	if !strings.Contains(got, "last streamed") {
		t.Fatalf("streaming write went to a stale file: %q", got)
	}
	if !strings.Contains(got, "last appended") {
		t.Fatalf("append went to a stale file: %q", got)
	}

	// Every event must land whole in exactly one file: a rotation may not split a
	// line across the current file and a backup.
	for _, name := range []string{path, path + ".1", path + ".2", path + ".3"} {
		data, err := os.ReadFile(name)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
			if line == "" {
				continue
			}
			if !strings.HasPrefix(line, "streamed ") && !strings.HasPrefix(line, "appended ") &&
				!strings.HasPrefix(line, "last ") {
				t.Fatalf("torn line %q in %s", line, name)
			}
		}
	}
}
