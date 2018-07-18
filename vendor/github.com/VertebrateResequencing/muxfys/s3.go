// Copyright © 2017, 2018 Genome Research Limited
// Author: Sendu Bala <sb10@sanger.ac.uk>.
// The target parsing code in this file is based on code in
// https://github.com/minio/minfs Copyright 2016 Minio, Inc.
// licensed under the Apache License, Version 2.0 (the "License"), stating:
// "You may not use this file except in compliance with the License. You may
// obtain a copy of the License at http://www.apache.org/licenses/LICENSE-2.0"
//
//  This file is part of muxfys.
//
//  muxfys is free software: you can redistribute it and/or modify
//  it under the terms of the GNU Lesser General Public License as published by
//  the Free Software Foundation, either version 3 of the License, or
//  (at your option) any later version.
//
//  muxfys is distributed in the hope that it will be useful,
//  but WITHOUT ANY WARRANTY; without even the implied warranty of
//  MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
//  GNU Lesser General Public License for more details.
//
//  You should have received a copy of the GNU Lesser General Public License
//  along with muxfys. If not, see <http://www.gnu.org/licenses/>.

package muxfys

// This file contains an implementation of RemoteAccessor for S3-like object
// stores.

import (
	"bufio"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/go-ini/ini"
	"github.com/minio/minio-go"
	"github.com/mitchellh/go-homedir"
)

const (
	defaultS3Domain = "s3.amazonaws.com"
)

// S3Config struct lets you provide details of the S3 bucket you wish to mount.
// If you have Amazon's s3cmd or other tools configured to work using config
// files and/or environment variables, you can make one of these with the
// S3ConfigFromEnvironment() method.
type S3Config struct {
	// The full URL of your bucket and possible sub-path, eg.
	// https://cog.domain.com/bucket/subpath. For performance reasons, you
	// should specify the deepest subpath that holds all your files.
	Target string

	// Region is optional if you need to use a specific region.
	Region string

	// AccessKey and SecretKey are your access credentials, and could be empty
	// strings for access to a public bucket.
	AccessKey string
	SecretKey string
}

// S3ConfigFromEnvironment makes an S3Config with Target, AccessKey, SecretKey
// and possibly Region filled in for you.
//
// It determines these by looking primarily at the given profile section of
// ~/.s3cfg (s3cmd's config file). If profile is an empty string, it comes from
// $AWS_DEFAULT_PROFILE or $AWS_PROFILE or defaults to "default".
//
// If ~/.s3cfg doesn't exist or isn't fully specified, missing values will be
// taken from the file pointed to by $AWS_SHARED_CREDENTIALS_FILE, or
// ~/.aws/credentials (in the AWS CLI format) if that is not set.
//
// If this file also doesn't exist, ~/.awssecret (in the format used by s3fs) is
// used instead.
//
// AccessKey and SecretKey values will always preferably come from
// $AWS_ACCESS_KEY_ID and $AWS_SECRET_ACCESS_KEY respectively, if those are set.
//
// If no config file specified host_base, the default domain used is
// s3.amazonaws.com. Region is set by the $AWS_DEFAULT_REGION environment
// variable, or if that is not set, by checking the file pointed to by
// $AWS_CONFIG_FILE (~/.aws/config if unset).
//
// To allow the use of a single configuration file, users can create a non-
// standard file that specifies all relevant options: use_https, host_base,
// region, access_key (or aws_access_key_id) and secret_key (or
// aws_secret_access_key) (saved in any of the files except ~/.awssecret).
//
// The path argument should at least be the bucket name, but ideally should also
// specify the deepest subpath that holds all the files that need to be
// accessed. Because reading from a public s3.amazonaws.com bucket requires no
// credentials, no error is raised on failure to find any values in the
// environment when profile is supplied as an empty string.
func S3ConfigFromEnvironment(profile, path string) (*S3Config, error) {
	if path == "" {
		return nil, fmt.Errorf("S3ConfigFromEnvironment requires a path")
	}

	profileSpecified := true
	if profile == "" {
		if profile = os.Getenv("AWS_DEFAULT_PROFILE"); profile == "" {
			if profile = os.Getenv("AWS_PROFILE"); profile == "" {
				profile = "default"
				profileSpecified = false
			}
		}
	}

	s3cfg, err := homedir.Expand("~/.s3cfg")
	if err != nil {
		return nil, err
	}
	ascf, err := homedir.Expand(os.Getenv("AWS_SHARED_CREDENTIALS_FILE"))
	if err != nil {
		return nil, err
	}
	acred, err := homedir.Expand("~/.aws/credentials")
	if err != nil {
		return nil, err
	}
	aconf, err := homedir.Expand(os.Getenv("AWS_CONFIG_FILE"))
	if err != nil {
		return nil, err
	}
	acon, err := homedir.Expand("~/.aws/config")
	if err != nil {
		return nil, err
	}

	aws, err := ini.LooseLoad(s3cfg, ascf, acred, aconf, acon)
	if err != nil {
		return nil, fmt.Errorf("S3ConfigFromEnvironment() loose loading of config files failed: %s", err)
	}

	var domain, key, secret, region string
	var https bool
	section, err := aws.GetSection(profile)
	if err == nil {
		https = section.Key("use_https").MustBool(false)
		domain = section.Key("host_base").String()
		region = section.Key("region").String()
		key = section.Key("access_key").MustString(section.Key("aws_access_key_id").MustString(os.Getenv("AWS_ACCESS_KEY_ID")))
		secret = section.Key("secret_key").MustString(section.Key("aws_secret_access_key").MustString(os.Getenv("AWS_SECRET_ACCESS_KEY")))
	} else if profileSpecified {
		return nil, fmt.Errorf("S3ConfigFromEnvironment could not find config files with profile %s", profile)
	}

	if key == "" && secret == "" {
		// last resort, check ~/.awssecret
		var awsSec string
		awsSec, err = homedir.Expand("~/.awssecret")
		if err != nil {
			return nil, err
		}
		if file, erro := os.Open(awsSec); erro == nil {
			defer func() {
				err = file.Close()
			}()

			scanner := bufio.NewScanner(file)
			if scanner.Scan() {
				line := scanner.Text()
				if line != "" {
					line = strings.TrimSuffix(line, "\n")
					ks := strings.Split(line, ":")
					if len(ks) == 2 {
						key = ks[0]
						secret = ks[1]
					}
				}
			}
		}
	}

	if os.Getenv("AWS_ACCESS_KEY_ID") != "" {
		key = os.Getenv("AWS_ACCESS_KEY_ID")
	}
	if os.Getenv("AWS_SECRET_ACCESS_KEY") != "" {
		secret = os.Getenv("AWS_SECRET_ACCESS_KEY")
	}

	if domain == "" {
		domain = defaultS3Domain
	}

	scheme := "http"
	if https {
		scheme += "s"
	}
	u := &url.URL{
		Scheme: scheme,
		Host:   domain,
		Path:   path,
	}

	if os.Getenv("AWS_DEFAULT_REGION") != "" {
		region = os.Getenv("AWS_DEFAULT_REGION")
	}

	return &S3Config{
		Target:    u.String(),
		Region:    region,
		AccessKey: key,
		SecretKey: secret,
	}, err
}

// S3Accessor implements the RemoteAccessor interface by embedding minio-go.
type S3Accessor struct {
	client   *minio.Client
	bucket   string
	target   string
	host     string
	basePath string
}

// NewS3Accessor creates an S3Accessor for interacting with S3-like object
// stores.
func NewS3Accessor(config *S3Config) (*S3Accessor, error) {
	// parse the target to get secure, host, bucket and basePath
	if config.Target == "" {
		return nil, fmt.Errorf("no Target defined")
	}

	u, err := url.Parse(config.Target)
	if err != nil {
		return nil, err
	}

	var secure bool
	if strings.HasPrefix(config.Target, "https") {
		secure = true
	}

	host := u.Host
	var bucket, basePath string
	if len(u.Path) > 1 {
		parts := strings.Split(u.Path[1:], "/")
		if len(parts) >= 0 {
			bucket = parts[0]
		}
		if len(parts) >= 1 {
			basePath = path.Join(parts[1:]...)
		}
	}

	if bucket == "" {
		return nil, fmt.Errorf("no bucket could be determined from [%s]", config.Target)
	}

	a := &S3Accessor{
		target:   config.Target,
		bucket:   bucket,
		host:     host,
		basePath: basePath,
	}

	// create a client for interacting with S3 (we do this here instead of
	// as-needed inside remote because there's large overhead in creating these)
	if config.Region != "" {
		a.client, err = minio.NewWithRegion(host, config.AccessKey, config.SecretKey, secure, config.Region)
	} else {
		// *** we are temporarily forcing use of V2 signatures for full
		// compatibility with ceph and uploading 0 byte files; hopefully
		// minio-go or ceph gets bugfixed to avoid this...
		a.client, err = minio.NewV2(host, config.AccessKey, config.SecretKey, secure)
	}
	return a, err
}

// DownloadFile implements RemoteAccessor by deferring to minio.
func (a *S3Accessor) DownloadFile(source, dest string) error {
	return a.client.FGetObject(a.bucket, source, dest, minio.GetObjectOptions{})
}

// UploadFile implements RemoteAccessor by deferring to minio.
func (a *S3Accessor) UploadFile(source, dest, contentType string) error {
	_, err := a.client.FPutObject(a.bucket, dest, source, minio.PutObjectOptions{ContentType: contentType})
	return err
}

// UploadData implements RemoteAccessor by deferring to minio.
func (a *S3Accessor) UploadData(data io.Reader, dest string) error {
	//*** try and do our own buffered read to initially get the mime type?
	_, err := a.client.PutObject(a.bucket, dest, data, -1, minio.PutObjectOptions{})
	return err
}

// ListEntries implements RemoteAccessor by deferring to minio.
func (a *S3Accessor) ListEntries(dir string) ([]RemoteAttr, error) {
	doneCh := make(chan struct{})
	oiCh := a.client.ListObjects(a.bucket, dir, false, doneCh)
	var ras []RemoteAttr
	for oi := range oiCh {
		if oi.Err != nil {
			close(doneCh)
			return nil, oi.Err
		}
		ras = append(ras, RemoteAttr{
			Name:  oi.Key,
			Size:  oi.Size,
			MTime: oi.LastModified,
			MD5:   oi.ETag,
		})
	}
	return ras, nil
}

// OpenFile implements RemoteAccessor by deferring to minio.
func (a *S3Accessor) OpenFile(path string, offset int64) (io.ReadCloser, error) {
	opts := minio.GetObjectOptions{}
	if offset > 0 {
		err := opts.SetRange(offset, 0)
		if err != nil {
			return nil, err
		}
	}
	core := minio.Core{Client: a.client}
	reader, _, err := core.GetObject(a.bucket, path, opts)
	return reader, err
}

// Seek implements RemoteAccessor by deferring to minio.
func (a *S3Accessor) Seek(path string, rc io.ReadCloser, offset int64) (io.ReadCloser, error) {
	err := rc.Close()
	if err != nil {
		return nil, err
	}
	opts := minio.GetObjectOptions{}
	err = opts.SetRange(offset, 0)
	if err != nil {
		return nil, err
	}
	core := minio.Core{Client: a.client}
	reader, _, err := core.GetObject(a.bucket, path, opts)
	return reader, err
}

// CopyFile implements RemoteAccessor by deferring to minio.
func (a *S3Accessor) CopyFile(source, dest string) error {
	destInfo, _ := minio.NewDestinationInfo(a.bucket, dest, nil, nil)
	return a.client.CopyObject(destInfo, minio.NewSourceInfo(a.bucket, source, nil))
}

// DeleteFile implements RemoteAccessor by deferring to minio.
func (a *S3Accessor) DeleteFile(path string) error {
	return a.client.RemoveObject(a.bucket, path)
}

// DeleteIncompleteUpload implements RemoteAccessor by deferring to minio.
func (a *S3Accessor) DeleteIncompleteUpload(path string) error {
	return a.client.RemoveIncompleteUpload(a.bucket, path)
}

// ErrorIsNotExists implements RemoteAccessor by looking for the NoSuchKey error
// code.
func (a *S3Accessor) ErrorIsNotExists(err error) bool {
	merr, ok := err.(minio.ErrorResponse)
	return ok && merr.Code == "NoSuchKey"
}

// ErrorIsNoQuota implements RemoteAccessor by looking for the QuotaExceeded
// error code.
func (a *S3Accessor) ErrorIsNoQuota(err error) bool {
	merr, ok := err.(minio.ErrorResponse)
	return ok && merr.Code == "QuotaExceeded"
}

// Target implements RemoteAccessor by returning the initial target we were
// configured with.
func (a *S3Accessor) Target() string {
	return a.target
}

// RemotePath implements RemoteAccessor by using the initially configured base
// path.
func (a *S3Accessor) RemotePath(relPath string) string {
	return filepath.Join(a.basePath, relPath)
}

// LocalPath implements RemoteAccessor by including the initially configured
// host and bucket in the return value.
func (a *S3Accessor) LocalPath(baseDir, remotePath string) string {
	return filepath.Join(baseDir, a.host, a.bucket, remotePath)
}
