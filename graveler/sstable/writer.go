package sstable

import (
	"context"
	"encoding/hex"
	"fmt"
	"hash"
	"strconv"

	"github.com/treeverse/lakefs/graveler/committed"

	"github.com/cockroachdb/pebble/sstable"
	"github.com/treeverse/lakefs/pyramid"
)

type DiskWriter struct {
	ctx    context.Context
	w      *sstable.Writer
	tierFS pyramid.FS
	first  committed.Key
	last   committed.Key
	count  int
	hash   hash.Hash
	fh     pyramid.StoredFile
	closed bool
}

func NewDiskWriter(ctx context.Context, tierFS pyramid.FS, ns committed.Namespace, hash hash.Hash) (*DiskWriter, error) {
	fh, err := tierFS.Create(ctx, string(ns))
	if err != nil {
		return nil, fmt.Errorf("opening file: %w", err)
	}

	writer := sstable.NewWriter(fh, sstable.WriterOptions{
		Compression: sstable.SnappyCompression,
	})

	return &DiskWriter{
		ctx:    ctx,
		w:      writer,
		fh:     fh,
		tierFS: tierFS,
		hash:   hash,
	}, nil
}

func (dw *DiskWriter) GetFS() pyramid.FS {
	return dw.tierFS
}

func (dw *DiskWriter) GetStoredFile() pyramid.StoredFile {
	return dw.fh
}

func (dw *DiskWriter) WriteRecord(record committed.Record) error {
	if err := dw.w.Set(record.Key, record.Value); err != nil {
		return fmt.Errorf("setting key and value: %w", err)
	}

	// updating stats
	if dw.count == 0 {
		dw.first = record.Key
	}
	dw.last = record.Key
	dw.count++

	if err := dw.writeHashWithLen(record.Key); err != nil {
		return err
	}
	return dw.writeHashWithLen(record.Value)
}

func (dw *DiskWriter) GetApproximateSize() uint64 {
	return dw.w.EstimatedSize()
}

func (dw *DiskWriter) writeHashWithLen(buf []byte) error {
	if _, err := dw.hash.Write([]byte(strconv.Itoa(len(buf)))); err != nil {
		return err
	}
	if _, err := dw.hash.Write(buf); err != nil {
		return err
	}
	if _, err := dw.hash.Write([]byte("|")); err != nil {
		return err
	}
	return nil
}

func (dw *DiskWriter) Abort() error {
	if dw.closed {
		return nil
	}

	if err := dw.w.Close(); err != nil {
		return fmt.Errorf("sstable file close: %w", err)
	}

	if err := dw.fh.Abort(dw.ctx); err != nil {
		return fmt.Errorf("sstable file abort: %w", err)
	}
	return nil
}

func (dw *DiskWriter) Close() (*committed.WriteResult, error) {
	tableHash := dw.hash.Sum(nil)
	sstableID := hex.EncodeToString(tableHash)

	if err := dw.w.Close(); err != nil {
		return nil, fmt.Errorf("sstable close (%s): %w", sstableID, err)
	}

	if err := dw.fh.Store(dw.ctx, sstableID); err != nil {
		return nil, fmt.Errorf("sstable store (%s): %w", sstableID, err)
	}

	dw.closed = true

	return &committed.WriteResult{
		RangeID:                 committed.ID(sstableID),
		First:                   dw.first,
		Last:                    dw.last,
		Count:                   dw.count,
		EstimatedRangeSizeBytes: dw.w.EstimatedSize(),
	}, nil
}
