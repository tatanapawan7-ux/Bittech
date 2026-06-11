package engine

import (
	"bufio"
	"fmt"
	"os"
	"sync"
)

// FileJournal is an append-only, fsync-per-append command log: one JSON record
// per line. It provides the durability contract the engine needs for local
// development and single-node deployments; a Kafka/Redpanda-backed Journal
// replaces it for clustered production without touching the engine.
type FileJournal struct {
	mu   sync.Mutex
	f    *os.File
	next uint64 // next sequence number to assign
}

// OpenFileJournal opens (or creates) the journal at path. Sequence numbering
// resumes after any existing records.
func OpenFileJournal(path string) (*FileJournal, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	j := &FileJournal{f: f, next: 1}
	// Count existing records to resume the sequence.
	if err := j.Replay(func(seq uint64, _ Command) error {
		j.next = seq + 1
		return nil
	}); err != nil {
		f.Close()
		return nil, err
	}
	return j, nil
}

// Append durably writes one command (fsync before returning) and assigns the
// next sequence number.
func (j *FileJournal) Append(cmd Command) (uint64, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	b, err := MarshalCommand(cmd)
	if err != nil {
		return 0, err
	}
	if _, err := j.f.Write(append(b, '\n')); err != nil {
		return 0, err
	}
	if err := j.f.Sync(); err != nil {
		return 0, err
	}
	seq := j.next
	j.next++
	return seq, nil
}

// Replay streams every record in order, assigning sequence numbers by position.
func (j *FileJournal) Replay(fn func(seq uint64, cmd Command) error) error {
	if _, err := j.f.Seek(0, 0); err != nil {
		return err
	}
	sc := bufio.NewScanner(j.f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	var seq uint64
	for sc.Scan() {
		seq++
		cmd, err := UnmarshalCommand(sc.Bytes())
		if err != nil {
			return fmt.Errorf("journal: corrupt record %d: %w", seq, err)
		}
		if err := fn(seq, cmd); err != nil {
			return err
		}
	}
	return sc.Err()
}

// Close releases the underlying file.
func (j *FileJournal) Close() error { return j.f.Close() }

// MemJournal is an in-memory Journal for tests.
type MemJournal struct {
	mu   sync.Mutex
	cmds []Command
}

// Append records the command in memory.
func (j *MemJournal) Append(cmd Command) (uint64, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.cmds = append(j.cmds, cmd)
	return uint64(len(j.cmds)), nil
}

// Replay iterates the recorded commands in order.
func (j *MemJournal) Replay(fn func(seq uint64, cmd Command) error) error {
	j.mu.Lock()
	cmds := append([]Command(nil), j.cmds...)
	j.mu.Unlock()
	for i, c := range cmds {
		if err := fn(uint64(i+1), c); err != nil {
			return err
		}
	}
	return nil
}
