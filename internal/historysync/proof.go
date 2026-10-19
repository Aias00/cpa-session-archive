package historysync

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"time"
)

const proofLifetime = 24 * time.Hour

// A settled ctime protects against same-tick rewrites. Filesystems without
// inode/device/change stamps deliberately retain full-prefix verification.
func fileSignature(info os.FileInfo) (string, time.Time) {
	if info == nil {
		return "", time.Time{}
	}
	v := reflect.ValueOf(info.Sys())
	if v.Kind() == reflect.Pointer && !v.IsNil() {
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return "", time.Time{}
	}
	number := func(name string) (uint64, bool) {
		f := v.FieldByName(name)
		if !f.IsValid() {
			return 0, false
		}
		if f.CanUint() {
			return f.Uint(), true
		}
		if f.CanInt() {
			return uint64(f.Int()), true
		}
		return 0, false
	}
	dev, ok := number("Dev")
	if !ok {
		return "", time.Time{}
	}
	ino, ok := number("Ino")
	if !ok {
		return "", time.Time{}
	}
	for _, key := range []string{"Ctimespec", "Ctim"} {
		c := v.FieldByName(key)
		if c.IsValid() && c.Kind() == reflect.Struct {
			sec, nsec := c.FieldByName("Sec"), c.FieldByName("Nsec")
			if sec.CanInt() && nsec.CanInt() {
				stamp := time.Unix(sec.Int(), nsec.Int())
				return fmt.Sprintf("%d:%d:%d:%d:%d", dev, ino, info.Size(), info.ModTime().UnixNano(), stamp.UnixNano()), stamp
			}
		}
	}
	return "", time.Time{}
}
func nowOf(cfg Config) time.Time {
	if cfg.Now != nil {
		return cfg.Now()
	}
	return time.Now()
}
func skipProof(ctx context.Context, cfg Config, key string, cp checkpoint, info os.FileInfo) bool {
	if cfg.VerifyAll || cp.Digest == "" {
		return false
	}
	signature, changed := fileSignature(info)
	now := nowOf(cfg)
	if signature == "" || !changed.Before(now.Add(-2*time.Second)) {
		return false
	}
	var saved, digest string
	var offset, verified int64
	err := cfg.Store.DB.QueryRowContext(ctx, "select signature,offset,prefix_sha256,verified_ns from native_file_proofs where source_key=?", key).Scan(&saved, &offset, &digest, &verified)
	return err == nil && saved == signature && offset == cp.Offset && digest == cp.Digest && now.Sub(time.Unix(0, verified)) >= 0 && now.Sub(time.Unix(0, verified)) < proofLifetime
}
func saveProof(ctx context.Context, cfg Config, key, path string, before os.FileInfo) error {
	after, err := os.Stat(path)
	if err != nil {
		return nil
	}
	a, _ := fileSignature(before)
	b, changed := fileSignature(after)
	now := nowOf(cfg)
	if a == "" || a != b || !changed.Before(now.Add(-2*time.Second)) {
		return nil
	}
	cp, err := loadCheckpoint(ctx, cfg.Store, key)
	if err != nil {
		return err
	}
	if cp.Digest == "" {
		return nil
	}
	_, err = cfg.Store.DB.ExecContext(ctx, "insert into native_file_proofs(source_key,signature,offset,prefix_sha256,verified_ns) values(?,?,?,?,?) on conflict(source_key) do update set signature=excluded.signature,offset=excluded.offset,prefix_sha256=excluded.prefix_sha256,verified_ns=excluded.verified_ns", key, b, cp.Offset, cp.Digest, now.UnixNano())
	return err
}
