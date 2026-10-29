/*
 * SPDX-FileCopyrightText: © 2017-2025 Istari Digital, Inc.
 * SPDX-License-Identifier: Apache-2.0
 */

package badger

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// mountTmpfs mounts a tmpfs of size bytes on a new directory. It needs root, and skips the test
// without it.
func mountTmpfs(t *testing.T, size int) string {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("mounting a small filesystem needs root")
	}
	dir := t.TempDir()
	require.NoError(t, unix.Mount("tmpfs", dir, "tmpfs", 0, fmt.Sprintf("size=%d", size)))
	t.Cleanup(func() { require.NoError(t, unix.Unmount(dir, unix.MNT_DETACH)) })
	return dir
}

// fillDisk takes the free space left in dir with a file, and returns its path.
func fillDisk(t *testing.T, dir string) string {
	t.Helper()
	var st unix.Statfs_t
	require.NoError(t, unix.Statfs(dir, &st))
	path := filepath.Join(dir, "filler")
	f, err := os.Create(path)
	require.NoError(t, err)
	defer func() { require.NoError(t, f.Close()) }()
	require.NoError(t, unix.Fallocate(int(f.Fd()), 0, 0, int64(st.Bavail)*st.Bsize))
	return path
}

// requireKeys checks that db holds every key in keys with its value.
func requireKeys(t *testing.T, db *DB, keys map[string][]byte) {
	t.Helper()
	require.NoError(t, db.View(func(txn *Txn) error {
		for k, v := range keys {
			item, err := txn.Get([]byte(k))
			if err != nil {
				return fmt.Errorf("%s: %w", k, err)
			}
			got, err := item.ValueCopy(nil)
			if err != nil {
				return fmt.Errorf("%s: %w", k, err)
			}
			if !bytes.Equal(got, v) {
				return fmt.Errorf("%s: wrong value", k)
			}
		}
		return nil
	}))
}

// A compaction whose table cannot be created, as on a full disk, fails and keeps its input.
func TestCompactionOnFullDisk(t *testing.T) {
	dir := mountTmpfs(t, 16<<20)
	opt := getTestOptions(dir).
		WithMemTableSize(1 << 20).
		WithValueLogFileSize(1 << 20).
		WithBaseTableSize(8 << 20).
		WithNumLevelZeroTables(100).
		WithNumLevelZeroTablesStall(200).
		WithValueThreshold(64 << 10)
	db, err := Open(opt)
	require.NoError(t, err)
	acked := make(map[string][]byte)
	for i := 0; len(db.Tables()) < 2; i++ {
		require.Less(t, i, 4096, "two memtables were not flushed")
		key, val := fmt.Sprintf("key%06d", i), make([]byte, 4<<10)
		_, _ = rand.Read(val)
		require.NoError(t, db.Update(func(txn *Txn) error { return txn.Set([]byte(key), val) }))
		acked[key] = val
	}

	db.stopCompactions()
	filler := fillDisk(t, dir)
	require.ErrorIs(t, db.lc.doCompact(0, compactionPriority{level: 0, score: 1}), syscall.ENOSPC)
	requireKeys(t, db, acked)
	db.startCompactions()
	require.NoError(t, os.Remove(filler))
	require.NoError(t, db.Close())
}
