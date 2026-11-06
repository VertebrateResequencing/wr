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

package jobqueue

// This file holds where a store's backup is kept: a local path, or objects
// under a key in an S3 bucket.

import (
	"path/filepath"
	"strings"

	"github.com/VertebrateResequencing/muxfys/v5"
	"github.com/VertebrateResequencing/wr/internal"
)

// newS3Objects returns the objects in the S3 bucket path (bucket and any
// directories within it) reached with the credentials of profile. Tests
// replace it with a fake.
var newS3Objects = func(profile, path string) (s3Objects, error) { //nolint:gochecknoglobals
	config, err := muxfys.S3ConfigFromEnvironment(profile, path)
	if err != nil {
		return nil, err
	}

	accessor, err := muxfys.NewS3Accessor(config)
	if err != nil {
		return nil, err
	}

	return accessor, nil
}

// s3Objects is the part of muxfys.S3Accessor that backups use, so tests can
// stand a fake in for S3 (see newS3Objects).
type s3Objects interface {
	DownloadFile(source, dest string) error
	UploadFile(source, dest, contentType string) error
	DeleteFile(path string) error
	ListEntries(dir string) ([]muxfys.RemoteAttr, error)
}

// s3BackupLocation returns the objects of the bucket an S3 dbBkFile url
// (s3://[profile@]bucket/path/file) names, and the key of the backup in it. In
// development deployment the key gets "." + deployment appended, so a
// development manager never touches a production backup.
func s3BackupLocation(dbBkFile, deployment string) (s3Objects, string, error) {
	if deployment == internal.Development {
		dbBkFile += "." + deployment
	}

	path := strings.TrimPrefix(dbBkFile, internal.S3Prefix)
	pp := strings.Split(path, "@")

	profile := "default"
	if len(pp) == s3ProfilePathParts {
		profile = pp[0]
		path = pp[1]
	}

	dir := filepath.Dir(path)

	objects, err := newS3Objects(profile, dir)
	if err != nil {
		return nil, "", err
	}

	key, err := stripBucketFromS3Path(filepath.Join(dir, filepath.Base(path)))
	if err != nil {
		return nil, "", err
	}

	return objects, key, nil
}

// backupTarget is where a store's backup is kept. Later items add the
// operations that write and read a backup (spec E3).
type backupTarget interface {
	// removeAll deletes the backup and everything the DB keeps beside it.
	removeAll() error
}

// backupTargetAt returns the backupTarget of dbBkFile, a local path or an S3
// url (see s3BackupLocation) for a manager of the given deployment.
func backupTargetAt(dbBkFile, deployment string) (backupTarget, error) {
	if !internal.InS3(dbBkFile) {
		return dirTarget{path: dbBkFile}, nil
	}

	objects, key, err := s3BackupLocation(dbBkFile, deployment)
	if err != nil {
		return nil, err
	}

	return s3Target{objects: objects, key: key}, nil
}

// s3Target is a backupTarget for the backup at key in a bucket.
type s3Target struct {
	objects s3Objects
	key     string
}

// removeAll deletes the objects of the backup at t.key, as dirTarget's does
// the files: a store backup under key + "/", an old bbolt backup at key, and
// the bbolt files E1 keeps beside it at key + ".bolt-pre-" + v. Other objects
// whose keys merely start with key are left.
func (t s3Target) removeAll() error {
	for _, prefix := range []string{t.key + "/", t.key + storeBoltPreInfix} {
		if err := t.removeUnder(prefix); err != nil {
			return err
		}
	}

	return t.objects.DeleteFile(t.key)
}

// removeUnder deletes every object whose key starts with prefix, descending
// into the common prefixes S3 lists in place of deeper keys.
func (t s3Target) removeUnder(prefix string) error {
	entries, err := t.objects.ListEntries(prefix)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		if strings.HasSuffix(entry.Name, "/") && entry.Name != prefix {
			err = t.removeUnder(entry.Name)
		} else {
			err = t.objects.DeleteFile(entry.Name)
		}

		if err != nil {
			return err
		}
	}

	return nil
}

// dirTarget is a backupTarget at a local path.
type dirTarget struct {
	path string
}

// removeAll removes the backup at t.path, file or directory, and the bbolt
// files E1 keeps beside it at path + ".bolt-pre-" + v.
func (t dirTarget) removeAll() error {
	return removeOwned(t.path)
}
