package service

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"go.uber.org/zap"
)

const (
	slowTimingRetention    = time.Hour
	slowTimingBucketLimit  = 128 << 10
	slowTimingBucketLayout = "20060102T1504Z"
)

// Minute buckets bound the dedicated log to about 8 MiB. No payloads or log
// history are held in memory. Cleanup also runs when there is no new traffic.
type openAISlowTimingStore struct {
	mu          sync.Mutex
	dir         string
	stop        chan struct{}
	done        chan struct{}
	once        sync.Once
	dropped     int
	lastWarning time.Time
}

func openAISlowTimingDirectory(cfg *config.Config) string {
	if cfg != nil && strings.TrimSpace(cfg.Log.Output.FilePath) != "" {
		return filepath.Join(filepath.Dir(cfg.Log.Output.FilePath), "slow-ttft")
	}
	if dir := strings.TrimSpace(os.Getenv("DATA_DIR")); dir != "" {
		return filepath.Join(dir, "logs", "slow-ttft")
	}
	return filepath.Join(filepath.Dir(logger.DefaultContainerLogPath), "slow-ttft")
}

func newOpenAISlowTimingStore(dir string) *openAISlowTimingStore {
	s := &openAISlowTimingStore{dir: dir, stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		s.runJanitor(ticker.C)
	}()
	return s
}

func (s *openAISlowTimingStore) runJanitor(ticks <-chan time.Time) {
	defer close(s.done)
	s.cleanup(time.Now())
	for {
		select {
		case now := <-ticks:
			s.cleanup(now)
		case <-s.stop:
			return
		}
	}
}

func (s *openAISlowTimingStore) warnLocked(operation string) {
	if time.Since(s.lastWarning) < time.Minute {
		return
	}
	s.lastWarning = time.Now()
	logger.L().Warn("openai slow timing log unavailable", zap.String("operation", operation))
}

func (s *openAISlowTimingStore) Close() {
	if s != nil {
		s.once.Do(func() { close(s.stop) })
		<-s.done
	}
}

func (s *openAISlowTimingStore) cleanup(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupLocked(now)
}

func (s *openAISlowTimingStore) cleanupLocked(now time.Time) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if !os.IsNotExist(err) {
			s.warnLocked("read_directory")
		}
		return
	}
	cutoff := now.UTC().Add(-slowTimingRetention)
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !strings.HasPrefix(entry.Name(), "slow-") || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		stamp := strings.TrimSuffix(strings.TrimPrefix(entry.Name(), "slow-"), ".jsonl")
		created, err := time.Parse(slowTimingBucketLayout, stamp)
		if err == nil && (!created.After(cutoff) || created.After(now.UTC().Truncate(time.Minute))) {
			if os.Remove(filepath.Join(s.dir, entry.Name())) != nil {
				s.warnLocked("cleanup")
			}
		}
	}
}

func (s *openAISlowTimingStore) write(now time.Time, event map[string]any) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case <-s.stop:
		return
	default:
	}
	if os.MkdirAll(s.dir, 0700) != nil {
		s.warnLocked("create_directory")
		return
	}
	s.cleanupLocked(now)
	if s.dropped > 0 {
		event["dropped_since_last_record"] = s.dropped
	}
	line, err := json.Marshal(event)
	if err != nil || len(line) > 16<<10 {
		return
	}
	path := filepath.Join(s.dir, "slow-"+now.UTC().Format(slowTimingBucketLayout)+".jsonl")
	info, statErr := os.Lstat(path)
	flags := os.O_WRONLY | os.O_APPEND
	if os.IsNotExist(statErr) {
		flags |= os.O_CREATE | os.O_EXCL
	} else if statErr != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		s.warnLocked("unsafe_file")
		return
	} else if info.Size()+int64(len(line)+1) > slowTimingBucketLimit {
		s.dropped++
		return
	}
	f, err := os.OpenFile(path, flags, 0600)
	if err != nil {
		s.warnLocked("open_file")
		return
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !opened.Mode().IsRegular() || opened.Mode().Perm()&0077 != 0 || (info != nil && !os.SameFile(info, opened)) || opened.Size()+int64(len(line)+1) > slowTimingBucketLimit {
		s.warnLocked("file_changed")
		return
	}
	if _, err := f.Write(append(line, '\n')); err == nil {
		s.dropped = 0
	} else {
		s.warnLocked("write_file")
	}
}
