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

package internal

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const dbUpgradeStatusPerm = 0o600

// DBUpgradeStatus records a manager database upgrade phase that can be shown to
// the user while the manager is not yet ready to accept connections.
type DBUpgradeStatus struct {
	State     string    `json:"state"`
	Detail    string    `json:"detail"`
	Processed int       `json:"processed,omitempty"`
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"started_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ReadDBUpgradeStatus reads the database upgrade status sidecar.
func ReadDBUpgradeStatus(dbFile string) (DBUpgradeStatus, os.FileInfo, error) {
	path := DBUpgradeStatusPath(dbFile)

	info, err := os.Stat(path)
	if err != nil {
		return DBUpgradeStatus{}, nil, err
	}

	payload, err := os.ReadFile(path)
	if err != nil {
		return DBUpgradeStatus{}, nil, err
	}

	var status DBUpgradeStatus
	if err = json.Unmarshal(payload, &status); err != nil {
		return DBUpgradeStatus{}, nil, fmt.Errorf("parse database upgrade status: %w", err)
	}

	return status, info, nil
}

// WriteDBUpgradeStatus atomically writes the database upgrade status sidecar.
func WriteDBUpgradeStatus(dbFile string, status DBUpgradeStatus) error {
	now := time.Now()
	if status.StartedAt.IsZero() {
		status.StartedAt = now
	}

	status.UpdatedAt = now
	status.PID = os.Getpid()

	payload, err := json.MarshalIndent(status, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal database upgrade status: %w", err)
	}

	payload = append(payload, '\n')

	return writeDBUpgradeStatusFile(DBUpgradeStatusPath(dbFile), payload)
}

func writeDBUpgradeStatusFile(path string, payload []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp.")
	if err != nil {
		return fmt.Errorf("create temporary database upgrade status: %w", err)
	}

	tmpName := tmp.Name()
	defer func() {
		_ = os.Remove(tmpName)
	}()

	if err = writeDBUpgradeStatusTemp(tmp, payload); err != nil {
		return err
	}

	if err = os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("publish database upgrade status: %w", err)
	}

	return nil
}

func writeDBUpgradeStatusTemp(tmp *os.File, payload []byte) error {
	if _, err := tmp.Write(payload); err != nil {
		_ = tmp.Close()

		return fmt.Errorf("write temporary database upgrade status: %w", err)
	}

	if err := tmp.Chmod(dbUpgradeStatusPerm); err != nil {
		_ = tmp.Close()

		return fmt.Errorf("chmod temporary database upgrade status: %w", err)
	}

	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary database upgrade status: %w", err)
	}

	return nil
}

// DBUpgradeStatusPath returns the sidecar status path for a manager DB file.
func DBUpgradeStatusPath(dbFile string) string {
	return dbFile + ".upgrade"
}

// RemoveDBUpgradeStatus removes the database upgrade status sidecar.
func RemoveDBUpgradeStatus(dbFile string) error {
	err := os.Remove(DBUpgradeStatusPath(dbFile))
	if err == nil || os.IsNotExist(err) {
		return nil
	}

	return err
}
