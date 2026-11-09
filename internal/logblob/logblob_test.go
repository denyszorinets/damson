package logblob_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/denyszorinets/damson/internal/logblob"
	"github.com/denyszorinets/damson/internal/logblob/blobtest"
	"github.com/denyszorinets/damson/internal/store"
)

type memLogs struct {
	store.LogStore
	m map[string][]byte
}

func (m *memLogs) PutLogData(_ context.Context, k string, d []byte, _ time.Time) error {
	m.m[k] = d
	return nil
}
func (m *memLogs) GetLogData(_ context.Context, k string) ([]byte, error) {
	d, ok := m.m[k]
	if !ok {
		return nil, store.ErrNotFound
	}
	return d, nil
}
func (m *memLogs) DeleteLogDataPrefix(_ context.Context, p string) (int64, error) {
	if p == "" {
		return 0, store.ErrInvalid
	}
	var n int64
	for k := range m.m {
		if len(k) >= len(p) && k[:len(p)] == p {
			delete(m.m, k)
			n++
		}
	}
	return n, nil
}

func TestDBContract(t *testing.T) {
	blobtest.Run(t, func(*testing.T) logblob.Blob { return logblob.DB{S: &memLogs{m: map[string][]byte{}}} })
}

func TestSet(t *testing.T) {
	db := logblob.DB{S: &memLogs{m: map[string][]byte{}}}
	if _, err := logblob.NewSet("s3", map[string]logblob.Blob{"db": db}); !errors.Is(err, logblob.ErrUnknownBackend) {
		t.Fatalf("err = %v", err)
	}
	s, err := logblob.NewSet("db", map[string]logblob.Blob{"db": db})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.External()) != 0 {
		t.Fatal("db is not external")
	}
	if _, err := s.Get(context.Background(), "s3", "k"); !errors.Is(err, logblob.ErrUnknownBackend) {
		t.Fatalf("err = %v", err)
	}
}

type probedBlob struct {
	logblob.Blob
	err error
}

func (p probedBlob) Probe(context.Context) error { return p.err }

func TestSetProbeReportsAnUnreachableExternalBackend(t *testing.T) {
	db := logblob.DB{}
	ok, err := logblob.NewSet(logblob.BackendDB, map[string]logblob.Blob{logblob.BackendDB: db, "s3": probedBlob{Blob: db}})
	if err != nil {
		t.Fatal(err)
	}
	if err := ok.Probe(context.Background()); err != nil {
		t.Fatalf("healthy backends: %v", err)
	}
	bad, _ := logblob.NewSet(logblob.BackendDB, map[string]logblob.Blob{logblob.BackendDB: db, "s3": probedBlob{Blob: db, err: errors.New("unreachable")}})
	if err := bad.Probe(context.Background()); err == nil {
		t.Fatal("an unreachable s3 backend passed the probe")
	}
	if err := logblob.DBOnly(nil).Probe(context.Background()); err != nil {
		t.Fatalf("db only: %v", err)
	}
}
