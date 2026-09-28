/*******************************************************************************
 * Copyright (c) 2026 Genome Research Ltd.
 *
 * Author: Sendu Bala <sb10@sanger.ac.uk>
 *
 * Permission is hereby granted, free of charge, to any person obtaining
 * a copy of this software and associated documentation files (the
 * "Software"), to deal in the Software without restriction, including
 * without limitation the rights to use, copy, modify, merge, publish,
 * distribute, sublicense, and/or sell copies of the Software, and to
 * permit persons to whom the Software is furnished to do so, subject to
 * the following conditions:
 *
 * The above copyright notice and this permission notice shall be included
 * in all copies or substantial portions of the Software.
 *
 * THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND,
 * EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF
 * MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT.
 * IN NO EVENT SHALL THE AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY
 * CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER IN AN ACTION OF CONTRACT,
 * TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION WITH THE
 * SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.
 ******************************************************************************/

// Package testcerts gives tests a manager's TLS files without paying for new
// RSA keys every time. internal.GenerateCerts makes two 2048-bit keys, which
// takes about 150ms, and test fixtures that start a fresh manager dir call it
// hundreds of times per run. Write generates one set per domain per process and
// copies it to wherever each test wants it.
//
// Tests that check certificate generation, or that need a CA that differs from
// another server's, must keep calling internal.GenerateCerts themselves.
package testcerts

import (
	crand "crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"github.com/VertebrateResequencing/wr/internal"
)

const numFiles = 3

// errExists is returned by Write when a file it would write already exists.
var errExists = errors.New("testcerts: file already exists")

// certSet is the contents and modes of one generated CA, server cert and key,
// in that order.
type certSet struct {
	data  [numFiles][]byte
	modes [numFiles]fs.FileMode
}

//nolint:gochecknoglobals // The per-process cache is the point of the package.
var cache = struct {
	sync.Mutex
	sets map[string]*certSet
}{sets: make(map[string]*certSet)}

// Write puts a CA certificate, a server certificate signed by it for domain,
// and the server's key at the given paths, as internal.GenerateCerts would with
// the default key sizes. Like GenerateCerts, it fails if any of the files
// already exist. Every call for the same domain in a process writes the same
// set.
func Write(caFile, certFile, keyFile, domain string) error {
	set, err := setFor(domain)
	if err != nil {
		return err
	}

	paths := [numFiles]string{caFile, certFile, keyFile}

	for _, path := range paths {
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("%w: %s", errExists, path)
		}
	}

	for i, path := range paths {
		if err := writeNew(path, set.data[i], set.modes[i]); err != nil {
			return err
		}
	}

	return nil
}

func setFor(domain string) (*certSet, error) {
	cache.Lock()
	defer cache.Unlock()

	if set, ok := cache.sets[domain]; ok {
		return set, nil
	}

	set, err := generate(domain)
	if err != nil {
		return nil, err
	}

	cache.sets[domain] = set

	return set, nil
}

func generate(domain string) (*certSet, error) {
	dir, err := os.MkdirTemp("", "wr_testcerts")
	if err != nil {
		return nil, err
	}

	defer os.RemoveAll(dir)

	paths := [numFiles]string{
		filepath.Join(dir, "ca.pem"),
		filepath.Join(dir, "cert.pem"),
		filepath.Join(dir, "key.pem"),
	}

	if err = internal.GenerateCerts(paths[0], paths[1], paths[2], domain, internal.DefaultBitsForRootRSAKey,
		internal.DefualtBitsForServerRSAKey, crand.Reader, internal.DefaultCertFileFlags); err != nil {
		return nil, err
	}

	set := &certSet{}

	for i, path := range paths {
		info, errs := os.Stat(path)
		if errs != nil {
			return nil, errs
		}

		set.modes[i] = info.Mode().Perm()

		if set.data[i], errs = os.ReadFile(path); errs != nil {
			return nil, errs
		}
	}

	return set, nil
}

func writeNew(path string, data []byte, mode fs.FileMode) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}

	if _, err = file.Write(data); err != nil {
		_ = file.Close()

		return err
	}

	return file.Close()
}
