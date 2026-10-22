//go:build linux

package artifactcache

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"go.etcd.io/bbolt"
)

type archiveEnrollmentHistory struct {
	SchemaVersion int    `json:"schema_version"`
	Source        string `json:"source"`
	Root          string `json:"root"`
	Namespace     string `json:"namespace"`
	MaxBytes      int64  `json:"max_bytes"`
	Identity      string `json:"identity"`
	SourcePresent bool   `json:"source_present"`
	Cold          bool   `json:"cold"`
	ImportStarted bool   `json:"import_started"`
	Ready         bool   `json:"ready"`
}

func archiveHistoryPath(source string) string {
	return filepath.Join(filepath.Dir(source), "."+filepath.Base(source)+".archive-enrollment-v1.bolt")
}
func (h archiveEnrollmentHistory) snapshot() string {
	return filepath.Join(filepath.Dir(h.Source), "."+filepath.Base(h.Source)+".archive-enrollment-"+h.Identity)
}
func storeArchiveHistory(db *bbolt.DB, h *archiveEnrollmentHistory) error {
	data, err := json.Marshal(h)
	if err != nil {
		return err
	}
	return db.Update(func(tx *bbolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists([]byte("archive-enrollment-v1"))
		if err != nil {
			return err
		}
		return b.Put([]byte("history"), data)
	})
}
func loadArchiveHistory(ctx context.Context, report *ArchiveEnrollmentReport, create bool) (*bbolt.DB, *archiveEnrollmentHistory, error) {
	db, fresh, err := openEnrollmentHistoryFile(ctx, archiveHistoryPath(report.Source), create)
	if err != nil {
		return nil, nil, err
	}
	var h *archiveEnrollmentHistory
	err = db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte("archive-enrollment-v1"))
		if b == nil {
			return nil
		}
		data := b.Get([]byte("history"))
		var row archiveEnrollmentHistory
		if len(data) > 16384 || json.Unmarshal(data, &row) != nil {
			return fmt.Errorf("archive enrollment history invalid")
		}
		canonical, e := json.Marshal(row)
		if e != nil || !bytes.Equal(data, canonical) || row.SchemaVersion != 1 || row.Source != report.Source || row.Root != report.Root || row.Namespace != report.Namespace || row.MaxBytes != report.MaxBytes || !receiptDigest(row.Identity) || !row.Cold && !row.SourcePresent {
			return fmt.Errorf("archive enrollment history identity or policy differs")
		}
		h = &row
		return nil
	})
	if err == nil && h == nil {
		if !create || !fresh {
			err = fmt.Errorf("archive enrollment history is missing")
		} else {
			h, err = beginArchiveHistory(db, report)
		}
	}
	if err != nil {
		_ = db.Close()
		return nil, nil, err
	}
	return db, h, nil
}
func beginArchiveHistory(db *bbolt.DB, r *ArchiveEnrollmentReport) (*archiveEnrollmentHistory, error) {
	if _, err := os.Lstat(filepath.Join(r.Root, r.Namespace)); !os.IsNotExist(err) {
		return nil, fmt.Errorf("archive destination exists without routing history")
	}
	cold, err := toolEnrollmentSourceIsCold(r.Source)
	if err != nil {
		return nil, err
	}
	_, err = os.Lstat(r.Source)
	present := err == nil
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if !cold && !present {
		return nil, fmt.Errorf("warm archive source disappeared")
	}
	var identity [32]byte
	if _, err = rand.Read(identity[:]); err != nil {
		return nil, err
	}
	h := &archiveEnrollmentHistory{SchemaVersion: 1, Source: r.Source, Root: r.Root, Namespace: r.Namespace, MaxBytes: r.MaxBytes, Identity: hex.EncodeToString(identity[:]), SourcePresent: present, Cold: cold}
	if err = storeArchiveHistory(db, h); err != nil {
		return nil, err
	}
	return h, nil
}
