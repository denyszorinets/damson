package agent

import (
	"errors"
	"fmt"
	"os"
)

// spool is a bounded ring of records in one file. Records are appended in
// order and freed in any order; when a new record does not fit, the oldest
// live records are evicted. The file never grows past its capacity.
//
// Offsets are logical (ever increasing); the physical position is the offset
// modulo the capacity, and a record never straddles the end of the file.
// spool is not safe for concurrent use: the log hub serializes access.
type spool struct {
	f          *os.File
	cap        int64
	head, tail int64
	q          []*spoolSlot
}

// spoolSlot is one record. owner is the log batch stored in it.
type spoolSlot struct {
	off   int64
	n     int
	dead  bool
	owner *batch
}

var (
	errSpoolRecordTooBig = errors.New("log batch larger than the spool")
	errSpoolClosed       = errors.New("log spool closed")
)

// newSpool creates the spool file in dir ("" means the system temp dir).
func newSpool(dir string, capacity int64) (*spool, error) {
	if dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("creating spool dir: %w", err)
		}
	}
	f, err := os.CreateTemp(dir, "damson-spool-*.bin")
	if err != nil {
		return nil, fmt.Errorf("creating spool file: %w", err)
	}
	return &spool{f: f, cap: capacity}, nil
}

// put stores data for owner and returns the slot plus the live slots it
// evicted to make room. An evicted slot's owner must be treated as lost.
func (s *spool) put(data []byte, owner *batch) (*spoolSlot, []*spoolSlot, error) {
	n := int64(len(data))
	if n > s.cap {
		return nil, nil, errSpoolRecordTooBig
	}
	var evicted []*spoolSlot
	for {
		start := s.tail
		if phys := start % s.cap; phys+n > s.cap {
			start += s.cap - phys // skip the unusable end of the file
		}
		if start+n-s.head <= s.cap {
			if _, err := s.f.WriteAt(data, start%s.cap); err != nil {
				return nil, evicted, fmt.Errorf("writing spool: %w", err)
			}
			sl := &spoolSlot{off: start, n: len(data), owner: owner}
			s.tail = start + n
			s.q = append(s.q, sl)
			return sl, evicted, nil
		}
		if len(s.q) == 0 {
			s.head, s.tail = 0, 0 // empty: restart at the beginning of the file
			continue
		}
		sl := s.q[0]
		s.q = s.q[1:]
		if !sl.dead {
			sl.dead = true
			evicted = append(evicted, sl)
		}
		s.advance()
	}
}

// read returns the record's bytes.
func (s *spool) read(sl *spoolSlot) ([]byte, error) {
	buf := make([]byte, sl.n)
	if _, err := s.f.ReadAt(buf, sl.off%s.cap); err != nil {
		return nil, fmt.Errorf("reading spool: %w", err)
	}
	return buf, nil
}

// release frees a record (acknowledged or forgotten).
func (s *spool) release(sl *spoolSlot) {
	if sl.dead {
		return
	}
	sl.dead = true
	s.advance()
}

// advance drops freed records from the front and moves head to the oldest
// live record, or to tail when none is left.
func (s *spool) advance() {
	for len(s.q) > 0 && s.q[0].dead {
		s.q = s.q[1:]
	}
	if len(s.q) > 0 {
		s.head = s.q[0].off
		return
	}
	s.head = s.tail
}

// used is the number of bytes between the oldest live record and the end of
// the newest one, holes included.
func (s *spool) used() int64 { return s.tail - s.head }

// close removes the file.
func (s *spool) close() {
	name := s.f.Name()
	_ = s.f.Close()
	_ = os.Remove(name)
}
