package bluedb

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
)

// Snapshot (checkpoint) file format — a full materialization of the memtable at
// a known WAL position, so recovery loads it and replays only the WAL tail after
// it, instead of the whole log. Atomically written (temp → fsync → rename → dir
// fsync), so a present snapshot is always complete; the trailing CRC guards
// bit-rot.
//
//	magic      "BSNP"        (4)
//	version    uint8         (1)
//	coveredSeq uint64        (8)   -- max committed seq captured here
//	count      uint64        (8)
//	entries    count * { klen uint32, key, vlen uint32, value }
//	crc32      uint32        (4)   -- IEEE over everything before it
const (
	snapMagic   = "BSNP"
	snapVersion = 1
)

// writeSnapshotAtomic serializes mem at coveredSeq and installs it at path
// atomically. On return the snapshot is durable and complete.
func writeSnapshotAtomic(path string, coveredSeq uint64, mem map[string][]byte) error {
	buf := make([]byte, 0, 4+1+8+8+len(mem)*32)
	buf = append(buf, snapMagic...)
	buf = append(buf, snapVersion)
	var num [8]byte
	binary.LittleEndian.PutUint64(num[:8], coveredSeq)
	buf = append(buf, num[:8]...)
	binary.LittleEndian.PutUint64(num[:8], uint64(len(mem)))
	buf = append(buf, num[:8]...)
	for k, v := range mem {
		binary.LittleEndian.PutUint32(num[:4], uint32(len(k)))
		buf = append(buf, num[:4]...)
		buf = append(buf, k...)
		binary.LittleEndian.PutUint32(num[:4], uint32(len(v)))
		buf = append(buf, num[:4]...)
		buf = append(buf, v...)
	}
	binary.LittleEndian.PutUint32(num[:4], crc32.ChecksumIEEE(buf))
	buf = append(buf, num[:4]...)

	tmp := path + ".tmp"
	// F7: 0o600 — snapshot holds the full app/session working set; not world-readable.
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(buf); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	syncDir(filepath.Dir(path))
	return nil
}

// loadSnapshot reads the snapshot at path. A missing snapshot returns a nil map
// and coveredSeq 0 (fresh DB — the WAL is the whole history). A present but
// corrupt snapshot is an error (never silently dropped — that would lose the
// checkpoint the WAL may have been truncated against).
func loadSnapshot(path string) (map[string][]byte, uint64, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, 0, nil
		}
		return nil, 0, err
	}
	if len(b) < 4+1+8+8+4 {
		return nil, 0, fmt.Errorf("bluedb: snapshot too short (%d bytes)", len(b))
	}
	if string(b[0:4]) != snapMagic {
		return nil, 0, fmt.Errorf("bluedb: bad snapshot magic")
	}
	if b[4] != snapVersion {
		return nil, 0, fmt.Errorf("bluedb: snapshot version %d unsupported", b[4])
	}
	body, crc := b[:len(b)-4], binary.LittleEndian.Uint32(b[len(b)-4:])
	if crc32.ChecksumIEEE(body) != crc {
		return nil, 0, fmt.Errorf("bluedb: snapshot CRC mismatch (corrupt)")
	}
	coveredSeq := binary.LittleEndian.Uint64(b[5:13])
	count := binary.LittleEndian.Uint64(b[13:21])
	// F4: bound count before allocating — each entry is >= 8 bytes (klen+vlen),
	// so a count larger than the body can hold is corruption (guards a giant
	// preallocation from a degenerate/crafted file even though the CRC passed).
	if count > uint64((len(body)-21)/8) {
		return nil, 0, fmt.Errorf("bluedb: snapshot count %d exceeds body", count)
	}
	mem := make(map[string][]byte, count)
	off := 21
	for i := uint64(0); i < count; i++ {
		if off+4 > len(body) {
			return nil, 0, fmt.Errorf("bluedb: snapshot truncated (key len)")
		}
		klen := int(binary.LittleEndian.Uint32(body[off : off+4]))
		off += 4
		if off+klen+4 > len(body) {
			return nil, 0, fmt.Errorf("bluedb: snapshot truncated (key)")
		}
		key := string(body[off : off+klen])
		off += klen
		vlen := int(binary.LittleEndian.Uint32(body[off : off+4]))
		off += 4
		if off+vlen > len(body) {
			return nil, 0, fmt.Errorf("bluedb: snapshot truncated (value)")
		}
		val := append([]byte(nil), body[off:off+vlen]...)
		off += vlen
		mem[key] = val
	}
	return mem, coveredSeq, nil
}
