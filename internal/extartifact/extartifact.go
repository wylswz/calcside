// Package extartifact validates and stores immutable extension trees:
// bounded portable files with a verified h1 tree hash, atomically
// installed into a cache directory.
package extartifact

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/mod/sumdb/dirhash"
)

const (
	MaxFiles      = 256
	MaxTotalBytes = 1 << 20 // 1 MiB
)

// ValidatePath enforces portable slash-relative file names — a tree
// that could escape its root on any platform is never accepted.
func ValidatePath(name string) error {
	if name == "" || len(name) > 512 {
		return errors.New("extartifact: bad path length")
	}
	if filepath.IsAbs(name) || strings.HasPrefix(name, "/") || strings.ContainsAny(name, "\\:") {
		return fmt.Errorf("extartifact: non-portable path %q", name)
	}
	parts := strings.Split(name, "/")
	for _, p := range parts {
		if p == "" || p == "." || p == ".." {
			return fmt.Errorf("extartifact: bad path element in %q", name)
		}
		for _, c := range p {
			if c < 0x20 || c == 0x7f {
				return errors.New("extartifact: control character in path")
			}
		}
	}
	return nil
}

// Bounds checks file count and total size.
func Bounds(files map[string][]byte) error {
	if len(files) > MaxFiles {
		return fmt.Errorf("extartifact: %d files exceeds %d", len(files), MaxFiles)
	}
	var total int64
	for name, b := range files {
		if err := ValidatePath(name); err != nil {
			return err
		}
		total += int64(len(b))
	}
	if total > MaxTotalBytes {
		return fmt.Errorf("extartifact: tree %d bytes exceeds %d", total, MaxTotalBytes)
	}
	return nil
}

// TreeHash computes the go-style h1: sum over sorted file names and bytes.
func TreeHash(files map[string][]byte) (string, error) {
	if err := Bounds(files); err != nil {
		return "", err
	}
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	return dirhash.Hash1(names, func(name string) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(files[name])), nil
	})
}

// CacheKey names the immutable directory for a verified (source,sum).
func CacheKey(source, sum string) string {
	h := sha256.Sum256([]byte(source + "\x00" + sum))
	return hex.EncodeToString(h[:])
}

// Install atomically materializes files into dir := cacheDir/CacheKey(source,sum).
// A unique temp sibling is written then renamed; an existing directory is
// never removed or truncated. Only the temp created here is cleaned.
func Install(cacheDir, source, sum string, files map[string][]byte) (string, error) {
	got, err := TreeHash(files)
	if err != nil {
		return "", err
	}
	if got != sum {
		return "", fmt.Errorf("extartifact: hash mismatch %s != %s", got, sum)
	}
	key := CacheKey(source, sum)
	final := filepath.Join(cacheDir, key)
	if _, err := os.Lstat(final); err == nil {
		return final, Read(final, sum)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp(cacheDir, key+".tmp-*")
	if err != nil {
		return "", err
	}
	ok := false
	defer func() {
		if !ok {
			os.RemoveAll(tmp)
		}
	}()
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		full := filepath.Join(tmp, filepath.FromSlash(n))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(full, files[n], 0o444); err != nil {
			return "", err
		}
	}
	if err := os.Rename(tmp, final); err != nil {
		// A concurrent installer may have won with identical content.
		if Read(final, sum) == nil {
			return final, nil
		}
		return "", err
	}
	ok = true
	return final, nil
}

// Read verifies an installed cache directory still matches its sum
// before advertising it as ready.
func Read(dir, sum string) error {
	st, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return errors.New("extartifact: invalid cache directory")
	}
	files := map[string][]byte{}
	var total int64
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() || len(files) >= MaxFiles {
			return errors.New("extartifact: invalid or oversized cache tree")
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		if err := ValidatePath(filepath.ToSlash(rel)); err != nil {
			return err
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		b, err := io.ReadAll(io.LimitReader(f, MaxTotalBytes-total+1))
		closeErr := f.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		total += int64(len(b))
		if total > MaxTotalBytes {
			return errors.New("extartifact: cache tree exceeds size limit")
		}
		files[filepath.ToSlash(rel)] = b
		return nil
	})
	if err != nil {
		return err
	}
	got, err := TreeHash(files)
	if err != nil {
		return err
	}
	if got != sum {
		return fmt.Errorf("extartifact: cache dir hash %s != %s", got, sum)
	}
	return nil
}
