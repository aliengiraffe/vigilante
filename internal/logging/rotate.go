package logging

import (
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// Default rotation limits applied when configuration is absent or unparseable.
const (
	DefaultMaxFileSize  int64 = 50 * 1024 * 1024
	DefaultMaxTotalSize int64 = 500 * 1024 * 1024
	DefaultMaxBackups   int   = 3
)

// Limits describes the on-disk budget for the logs directory.
//
// MaxFileSize bounds a single log file before it is rotated, MaxBackups is the
// number of rotated copies retained per file, and MaxTotalSize bounds the sum of
// every file in the logs directory. A non-positive value disables that
// particular limit.
type Limits struct {
	MaxFileSize  int64
	MaxBackups   int
	MaxTotalSize int64
}

// DefaultLimits returns the built-in rotation limits.
func DefaultLimits() Limits {
	return Limits{
		MaxFileSize:  DefaultMaxFileSize,
		MaxBackups:   DefaultMaxBackups,
		MaxTotalSize: DefaultMaxTotalSize,
	}
}

// Event kinds reported through the Configure callback.
const (
	EventRotate = "rotate"
	EventEvict  = "evict"
)

// Event describes a rotation or eviction that the writer performed. It is
// reported through the callback registered with Configure so operators can tell
// rotated logs apart from missing ones.
type Event struct {
	Kind          string // EventRotate or EventEvict
	Path          string
	ReclaimedByte int64
	Removed       []string
}

// ParseSize converts a human-readable size such as "500MB", "1 GiB", or a plain
// byte count such as "1048576" into bytes. Decimal suffixes (KB, MB, GB, TB) and
// binary suffixes (KiB, MiB, GiB, TiB) are both treated as powers of 1024, which
// matches how operators read the existing sandbox_memory_limit values.
func ParseSize(raw string) (int64, error) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return 0, errors.New("empty size")
	}
	unit := int64(1)
	lower := strings.ToLower(text)
	for _, candidate := range []struct {
		suffix string
		unit   int64
	}{
		{"tib", 1 << 40}, {"gib", 1 << 30}, {"mib", 1 << 20}, {"kib", 1 << 10},
		{"tb", 1 << 40}, {"gb", 1 << 30}, {"mb", 1 << 20}, {"kb", 1 << 10},
		{"t", 1 << 40}, {"g", 1 << 30}, {"m", 1 << 20}, {"k", 1 << 10},
		{"b", 1},
	} {
		if strings.HasSuffix(lower, candidate.suffix) {
			unit = candidate.unit
			text = strings.TrimSpace(text[:len(text)-len(candidate.suffix)])
			break
		}
	}
	if text == "" {
		return 0, fmt.Errorf("missing size value in %q", raw)
	}
	value, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid size %q", raw)
	}
	// ParseFloat accepts "NaN" and "Inf"; neither is a usable byte count.
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, fmt.Errorf("invalid size %q", raw)
	}
	if value < 0 {
		return 0, fmt.Errorf("negative size %q", raw)
	}
	scaled := value * float64(unit)
	if scaled > float64(1<<62) {
		return 0, fmt.Errorf("size %q out of range", raw)
	}
	return int64(scaled), nil
}

// FormatSize renders bytes using the same human-readable style ParseSize accepts.
func FormatSize(bytes int64) string {
	units := []struct {
		suffix string
		unit   int64
	}{
		{"TB", 1 << 40}, {"GB", 1 << 30}, {"MB", 1 << 20}, {"KB", 1 << 10},
	}
	for _, u := range units {
		if bytes >= u.unit && bytes%u.unit == 0 {
			return fmt.Sprintf("%d%s", bytes/u.unit, u.suffix)
		}
	}
	return fmt.Sprintf("%dB", bytes)
}

var (
	configMu    sync.RWMutex
	activeLimit = DefaultLimits()
	liveFunc    func() []string
	notifyFunc  func(Event)

	registryMu sync.Mutex
	registry   = map[string]*Writer{}

	notifying atomic.Bool
)

// Configure sets the process-wide rotation limits, the callback that reports the
// log files a sweep must never evict, and an optional event sink used to log
// rotations and evictions. It also retunes writers that were created earlier,
// so the daemon logger opened before configuration is loaded picks up the
// configured limits.
func Configure(limits Limits, live func() []string, notify func(Event)) {
	configMu.Lock()
	activeLimit = limits
	liveFunc = live
	notifyFunc = notify
	configMu.Unlock()

	registryMu.Lock()
	writers := make([]*Writer, 0, len(registry))
	for _, w := range registry {
		writers = append(writers, w)
	}
	registryMu.Unlock()

	for _, w := range writers {
		w.SetLimits(limits)
	}
}

// CurrentLimits returns the process-wide rotation limits.
func CurrentLimits() Limits {
	configMu.RLock()
	defer configMu.RUnlock()
	return activeLimit
}

func livePaths() []string {
	configMu.RLock()
	fn := liveFunc
	configMu.RUnlock()
	if fn == nil {
		return nil
	}
	return fn()
}

// notify reports an event, guarding against re-entry: the sink typically writes
// a daemon log line, which can itself rotate and emit another event.
func notify(ev Event) {
	configMu.RLock()
	fn := notifyFunc
	configMu.RUnlock()
	if fn == nil {
		return
	}
	if !notifying.CompareAndSwap(false, true) {
		return
	}
	defer notifying.Store(false)
	fn(ev)
}

// Writer is a rotation-aware io.WriteCloser for a single log file. It renames
// the current file aside once it exceeds the configured size and reopens the
// original path, so callers holding a long-lived Writer never keep appending to
// a rotated-away inode. Write and Close are safe for concurrent use.
type Writer struct {
	path string

	mu     sync.Mutex
	limits Limits
	file   *os.File
	size   int64
}

// OpenWriter returns the shared Writer for path, creating it on first use. All
// callers writing to the same file get the same Writer, so a rotation triggered
// by one of them is immediately visible to the others.
func OpenWriter(path string) *Writer {
	key := writerKey(path)

	registryMu.Lock()
	defer registryMu.Unlock()
	if w, ok := registry[key]; ok {
		return w
	}
	w := NewWriter(path, CurrentLimits())
	registry[key] = w
	return w
}

// NewWriter returns a standalone Writer for path with the given limits. It is
// not shared through the process registry; prefer OpenWriter for real log files.
func NewWriter(path string, limits Limits) *Writer {
	return &Writer{path: path, limits: limits}
}

func writerKey(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return path
}

// Path returns the current log file path.
func (w *Writer) Path() string { return w.path }

// SetLimits replaces the writer's rotation limits.
func (w *Writer) SetLimits(limits Limits) {
	w.mu.Lock()
	w.limits = limits
	w.mu.Unlock()
}

// Append writes p through the shared writer for path and then releases the file
// handle. One-shot callers use it so they do not hold a descriptor open for every
// log file the process ever touched, while still sharing rotation state — and the
// rotation lock — with any long-lived writer on the same path.
func Append(path string, p []byte) (int, error) {
	return OpenWriter(path).Append(p)
}

// Write appends p to the log file, rotating first when the write would push the
// file past the configured size limit. The file handle stays open for subsequent
// writes; callers holding a Writer for a bounded lifetime should Close it.
func (w *Writer) Write(p []byte) (int, error) {
	return w.write(p, true)
}

// Append writes p and then closes the file handle. A long-lived writer sharing
// this path simply reopens the current file on its next write.
func (w *Writer) Append(p []byte) (int, error) {
	return w.write(p, false)
}

func (w *Writer) write(p []byte, keepOpen bool) (int, error) {
	w.mu.Lock()
	events, err := w.prepare(len(p))
	if err != nil {
		w.mu.Unlock()
		return 0, err
	}
	n, writeErr := w.file.Write(p)
	w.size += int64(n)
	if !keepOpen {
		_ = w.file.Close()
		w.file = nil
		w.size = 0
	}
	w.mu.Unlock()

	for _, ev := range events {
		notify(ev)
	}
	return n, writeErr
}

// prepare ensures the file handle is open and rotates when needed. It must be
// called with w.mu held and returns the events to report after unlocking.
func (w *Writer) prepare(incoming int) ([]Event, error) {
	if err := w.ensureOpen(); err != nil {
		return nil, err
	}
	limit := w.limits.MaxFileSize
	if limit <= 0 || w.size == 0 || w.size+int64(incoming) <= limit {
		return nil, nil
	}
	return w.rotateLocked(), nil
}

func (w *Writer) ensureOpen() error {
	if w.file != nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(w.path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	size := int64(0)
	if info, statErr := f.Stat(); statErr == nil {
		size = info.Size()
	}
	w.file = f
	w.size = size
	return nil
}

// rotateLocked renames the current file aside, prunes surplus backups, reopens
// the original path, and sweeps the directory back within budget. Every step is
// best-effort: logging must never be the reason a session or the daemon fails.
func (w *Writer) rotateLocked() []Event {
	_ = w.file.Close()
	w.file = nil
	w.size = 0

	backups := w.limits.MaxBackups
	if backups < 0 {
		backups = 0
	}
	removed := []string{}
	if backups == 0 {
		if err := os.Remove(w.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			// Keep the oversized file rather than losing the handle entirely.
			_ = w.ensureOpen()
			return nil
		}
		removed = append(removed, w.path)
	} else {
		_ = os.Remove(backupPath(w.path, backups))
		for i := backups - 1; i >= 1; i-- {
			from := backupPath(w.path, i)
			if _, err := os.Stat(from); err != nil {
				continue
			}
			_ = os.Rename(from, backupPath(w.path, i+1))
		}
		if err := os.Rename(w.path, backupPath(w.path, 1)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			_ = w.ensureOpen()
			return nil
		}
	}

	// Reopen immediately so the next write lands in the fresh file even if the
	// caller never writes again through this writer.
	_ = w.ensureOpen()

	events := []Event{{Kind: EventRotate, Path: w.path, Removed: removed}}
	if result, err := Sweep(filepath.Dir(w.path), w.limits, livePaths()); err == nil && len(result.Removed) > 0 {
		events = append(events, Event{
			Kind:          EventEvict,
			Path:          filepath.Dir(w.path),
			ReclaimedByte: result.Reclaimed,
			Removed:       result.Removed,
		})
	}
	return events
}

// Close releases the underlying file handle. The Writer stays usable: a later
// Write reopens the current file, so closing a session's writer never silences
// other callers sharing it.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	w.size = 0
	return err
}

// backupPath returns the name of the index-th rotated copy of path.
func backupPath(path string, index int) string {
	return fmt.Sprintf("%s.%d", path, index)
}

// backupIndex reports the rotation index encoded in name, or 0 when name is not
// a rotated backup. Rotated names always carry a numeric suffix, so they can
// never be mistaken for a live "<repo>-issue-<n>.log" session log.
func backupIndex(name string) int {
	dot := strings.LastIndex(name, ".")
	if dot <= 0 || dot == len(name)-1 {
		return 0
	}
	index, err := strconv.Atoi(name[dot+1:])
	if err != nil || index <= 0 {
		return 0
	}
	return index
}

// SweepResult reports what a directory sweep reclaimed.
type SweepResult struct {
	TotalBefore int64
	TotalAfter  int64
	Reclaimed   int64
	Removed     []string
}

type sweepCandidate struct {
	path    string
	size    int64
	modTime int64
	backup  int
}

// Sweep removes log files from dir until its total size is at or below
// limits.MaxTotalSize. Rotated backups are evicted first, oldest rotation
// generation first, followed by the least recently modified non-live files.
// Paths listed in live — the current daemon log, the access log, and the session
// logs of running sessions — are never removed. A missing or empty directory is
// a no-op.
func Sweep(dir string, limits Limits, live []string) (SweepResult, error) {
	var result SweepResult
	if limits.MaxTotalSize <= 0 {
		return result, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return result, nil
		}
		return result, err
	}

	protected := make(map[string]struct{}, len(live))
	for _, path := range live {
		if strings.TrimSpace(path) == "" {
			continue
		}
		protected[writerKey(path)] = struct{}{}
	}

	candidates := make([]sweepCandidate, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		result.TotalBefore += info.Size()
		if _, ok := protected[writerKey(path)]; ok {
			continue
		}
		candidates = append(candidates, sweepCandidate{
			path:    path,
			size:    info.Size(),
			modTime: info.ModTime().UnixNano(),
			backup:  backupIndex(entry.Name()),
		})
	}
	result.TotalAfter = result.TotalBefore
	if result.TotalBefore <= limits.MaxTotalSize {
		return result, nil
	}

	// Highest rotation generation first (oldest data), then live-name files by
	// least recent modification. Path breaks ties so eviction is deterministic.
	sort.Slice(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if (a.backup > 0) != (b.backup > 0) {
			return a.backup > 0
		}
		if a.backup != b.backup {
			return a.backup > b.backup
		}
		if a.modTime != b.modTime {
			return a.modTime < b.modTime
		}
		return a.path < b.path
	})

	for _, candidate := range candidates {
		if result.TotalAfter <= limits.MaxTotalSize {
			break
		}
		if err := os.Remove(candidate.path); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				result.TotalAfter -= candidate.size
			}
			continue
		}
		result.TotalAfter -= candidate.size
		result.Reclaimed += candidate.size
		result.Removed = append(result.Removed, candidate.path)
	}
	return result, nil
}

// hasOpenFile reports whether the writer currently holds a file handle. It exists
// for tests that assert one-shot appends do not leak descriptors.
func (w *Writer) hasOpenFile() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.file != nil
}
