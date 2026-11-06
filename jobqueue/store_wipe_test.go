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

// Tests of the development wipe (spec E3): it removes everything the DB owns,
// directories and S3 prefixes included, before an empty store is created.

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/VertebrateResequencing/muxfys/v5"
	"github.com/VertebrateResequencing/wr/internal"
	. "github.com/smartystreets/goconvey/convey"
)

const (
	// storeWipeS3Backup is the S3 dbBkFile of the S3 wipe test, whose
	// objects are reached with the credentials of storeWipeS3Profile in
	// storeWipeS3Dir, and storeWipeS3Key the key a development manager keeps
	// its backup under.
	storeWipeS3Backup  = "s3://" + storeWipeS3Profile + "@" + storeWipeS3Dir + "/db_bk"
	storeWipeS3Profile = "wrtest"
	storeWipeS3Dir     = "bucket/wr"
	storeWipeS3Key     = "wr/db_bk.development"

	// storeWipeS3ProdKey is the key a production manager keeps the backup of
	// storeWipeS3Backup under.
	storeWipeS3ProdKey = "wr/db_bk"

	// storeWipeBoltPre and storeWipeBoltPreOld end the names of bbolt files
	// E1 keeps for rollback, written by different versions.
	storeWipeBoltPre    = ".bolt-pre-dev"
	storeWipeBoltPreOld = ".bolt-pre-v0.38.0"

	// storeWipeUnlistableDirMode lets a directory's entries be created,
	// removed and opened by name, but not listed.
	storeWipeUnlistableDirMode os.FileMode = 0o300

	// storeWipeMaxListings is the number of listings after which the fake S3
	// fails every listing, so a wipe that lists without end fails the test
	// instead of overflowing the stack.
	storeWipeMaxListings = 1000
)

var (
	// errStoreWipeList is the error the fake S3 fails a listing with.
	errStoreWipeList = errors.New("fake S3 listing failure")

	// errStoreWipeDelete is the error the fake S3 fails a deletion with.
	errStoreWipeDelete = errors.New("fake S3 deletion failure")

	// errStoreWipeListLoop is the error the fake S3 fails a listing with once
	// it has listed storeWipeMaxListings times.
	errStoreWipeListLoop = errors.New("fake S3 listed too many times")

	// errStoreWipeUnreachable is the error newS3Objects fails with when the
	// fake S3 cannot be reached.
	errStoreWipeUnreachable = errors.New("fake S3 unreachable")
)

// shouldExist is a GoConvey assertion that the path actual exists.
func shouldExist(actual any, _ ...any) string {
	if _, err := os.Lstat(fmt.Sprint(actual)); err != nil {
		return fmt.Sprintf("expected %s to exist: %s", actual, err)
	}

	return ""
}

// shouldNotExist is a GoConvey assertion that the path actual does not exist.
func shouldNotExist(actual any, _ ...any) string {
	if _, err := os.Lstat(fmt.Sprint(actual)); !os.IsNotExist(err) {
		return fmt.Sprintf("expected %s not to exist, but Lstat gave error %v", actual, err)
	}

	return ""
}

// fakeS3Objects is an in-memory bucket standing in for S3. Its ListEntries
// lists as S3 does with the "/" delimiter: the objects whose keys start with
// the prefix and have no "/" after it, and once each, ending in "/", the
// common prefixes of the rest. Like minio, it lists an object whose key is the
// prefix itself, such as a "dir/" folder marker, as an object. It counts the
// times newS3Objects returned it, records the profile and path that call
// was given, fails every ListEntries with listErr when
// that is set, or once it has listed storeWipeMaxListings times, and fails
// DeleteFile of each key in deleteErrs with that key's error.
type fakeS3Objects struct {
	mu         sync.Mutex
	objects    map[string][]byte
	opens      int
	profile    string
	path       string
	listings   int
	listErr    error
	deleteErrs map[string]error
}

// withFakeS3Objects makes newS3Objects return a new fakeS3Objects holding
// empty objects at keys until the Convey block ends, and returns it.
func withFakeS3Objects(keys ...string) *fakeS3Objects {
	fake := &fakeS3Objects{objects: make(map[string][]byte)}
	fake.put(keys...)

	original := newS3Objects

	Reset(func() { newS3Objects = original })

	newS3Objects = func(profile, path string) (s3Objects, error) {
		fake.mu.Lock()
		defer fake.mu.Unlock()

		fake.opens++
		fake.profile = profile
		fake.path = path

		return fake, nil
	}

	return fake
}

// openCount returns the number of times newS3Objects returned f.
func (f *fakeS3Objects) openCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.opens
}

// openedWith returns the profile and path newS3Objects was last given when it
// returned f.
func (f *fakeS3Objects) openedWith() (string, string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.profile, f.path
}

// failListing makes every later ListEntries fail with err, or, if err is nil,
// succeed again.
func (f *fakeS3Objects) failListing(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.listErr = err
}

// failDeleting makes every later DeleteFile of each of keys fail with err,
// leaving the object.
func (f *fakeS3Objects) failDeleting(err error, keys ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.deleteErrs == nil {
		f.deleteErrs = make(map[string]error)
	}

	for _, key := range keys {
		f.deleteErrs[key] = err
	}
}

// put stores an empty object at each of keys.
func (f *fakeS3Objects) put(keys ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	for _, key := range keys {
		f.objects[key] = nil
	}
}

// keys returns the keys of the objects held, sorted.
func (f *fakeS3Objects) keys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	return slices.Sorted(maps.Keys(f.objects))
}

// DownloadFile writes the object at source to dest.
func (f *fakeS3Objects) DownloadFile(source, dest string) error {
	f.mu.Lock()
	data, ok := f.objects[source]
	f.mu.Unlock()

	if !ok {
		return fmt.Errorf("fake S3: no object %s: %w", source, os.ErrNotExist)
	}

	return os.WriteFile(dest, data, dbFilePermission)
}

// UploadFile stores the content of source as the object at dest.
func (f *fakeS3Objects) UploadFile(source, dest, _ string) error {
	data, err := os.ReadFile(source)
	if err != nil {
		return err
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	f.objects[dest] = data

	return nil
}

// DeleteFile deletes the object at path; as on S3, a missing one is no error.
// It fails as failDeleting set for path.
func (f *fakeS3Objects) DeleteFile(path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if err := f.deleteErrs[path]; err != nil {
		return err
	}

	delete(f.objects, path)

	return nil
}

// ListEntries lists under prefix dir as S3 does with the "/" delimiter.
func (f *fakeS3Objects) ListEntries(dir string) ([]muxfys.RemoteAttr, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.listErr != nil {
		return nil, f.listErr
	}

	f.listings++
	if f.listings > storeWipeMaxListings {
		return nil, errStoreWipeListLoop
	}

	var entries []muxfys.RemoteAttr

	for key, data := range f.objects {
		rest, ok := strings.CutPrefix(key, dir)
		if !ok {
			continue
		}

		if before, _, found := strings.Cut(rest, "/"); found {
			common := dir + before + "/"
			if !slices.ContainsFunc(entries, func(e muxfys.RemoteAttr) bool { return e.Name == common }) {
				entries = append(entries, muxfys.RemoteAttr{Name: common})
			}

			continue
		}

		entries = append(entries, muxfys.RemoteAttr{Name: key, Size: int64(len(data))})
	}

	return entries, nil
}

func TestStoreDevWipe(t *testing.T) {
	if runnermode || servermode {
		return
	}

	ctx := context.Background()

	forEachStore(t, func(kind storeKind) {
		name := storeKindName(kind)

		Convey(fmt.Sprintf("Given a development %s store holding a job, with a backup", name), t, func() {
			cfg := storeWipeSeeded(ctx, t, kind)
			cfg.wipeDevDB = true

			Convey("an open removes the store and backup before creating an empty store", func() {
				s := openStoreWiping(ctx, cfg)

				soStoreHoldsNoJobs(s)
				So(cfg.dbBkFile, shouldNotExist)
				So(cfg.dbFile+storeLockSuffix, shouldExist)
			})

			Convey("with the leftovers of a conversion and a restore, an open removes them all without converting",
				func() {
					plantConversionLeftovers(ctx, t, cfg)
					plantRestoreLeftovers(cfg.dbFile)

					olderPres := []string{cfg.dbFile + storeWipeBoltPreOld, cfg.dbBkFile + storeWipeBoltPreOld}
					for _, path := range olderPres {
						So(os.WriteFile(path, []byte("bbolt"), dbFilePermission), ShouldBeNil)
					}

					s := openStoreWiping(ctx, cfg)

					soStoreHoldsNoJobs(s)

					for _, path := range append([]string{
						cfg.dbFile + ".converting", cfg.dbFile + storeWipeBoltPre,
						cfg.dbBkFile + storeWipeBoltPre, cfg.dbFile + ".restoring", cfg.dbFile + ".corrupt",
					}, olderPres...) {
						So(path, shouldNotExist)
					}
				})

			Convey("with its backup path in a directory that does not exist, an open succeeds", func() {
				cfg.dbBkFile = filepath.Join(t.TempDir(), "absent", testStoreBackupFile)

				soStoreHoldsNoJobs(openStoreWiping(ctx, cfg))
			})

			Convey("with an empty backup path, an open removes nothing from the working directory", func() {
				planted := plantInWorkingDir(t, ".bolt-pre-x", "..bolt-pre-x")
				cfg.dbBkFile = ""

				soStoreHoldsNoJobs(openStoreWiping(ctx, cfg))

				for _, path := range planted {
					So(path, shouldExist)
				}
			})

			Convey("with look-alikes of the bbolt files kept for rollback, and of the conversion and restore "+
				"leftovers beside the backup, an open leaves them", func() {
				paths := []string{cfg.dbFile, cfg.dbBkFile}
				lookAlikes := make([]string, 0, 3*len(paths)+3)

				for _, path := range paths {
					dir, base := filepath.Split(path)
					lookAlikes = append(lookAlikes,
						filepath.Join(dir, "x"+base+storeWipeBoltPre), path+".bolt-prefoo", path+".bolt-prev")
				}

				lookAlikes = append(lookAlikes, cfg.dbBkFile+storeConvertingSuffix,
					cfg.dbBkFile+storeRestoringSuffix, cfg.dbBkFile+storeCorruptSuffix)

				for _, path := range lookAlikes {
					So(os.WriteFile(path, nil, dbFilePermission), ShouldBeNil)
				}

				soStoreHoldsNoJobs(openStoreWiping(ctx, cfg))

				for _, path := range lookAlikes {
					So(path, shouldExist)
				}
			})

			conveyUnlessRoot("with a .converting directory it cannot empty, an open fails saying why", func() {
				converting := cfg.dbFile + storeConvertingSuffix
				So(os.Mkdir(converting, dbUnreadableDirMode), ShouldBeNil)
				So(os.WriteFile(filepath.Join(converting, "SOURCE"), nil, dbFilePermission), ShouldBeNil)

				Reset(func() { So(os.Chmod(converting, dbUnreadableDirMode), ShouldBeNil) })

				So(os.Chmod(converting, storeOpenUnwritableDirMode), ShouldBeNil)

				_, _, err := openStore(ctx, cfg)
				So(err, ShouldWrap, fs.ErrPermission)
				So(err.Error(), ShouldContainSubstring, "wipe development db "+cfg.dbFile)
			})

			conveyUnlessRoot("with its directory unlistable, beside a bbolt file kept for rollback, "+
				"an open fails saying why", func() {
				pre := cfg.dbFile + storeWipeBoltPre
				So(os.WriteFile(pre, []byte("bbolt"), dbFilePermission), ShouldBeNil)

				dir := filepath.Dir(cfg.dbFile)

				Reset(func() { So(os.Chmod(dir, dbUnreadableDirMode), ShouldBeNil) })

				So(os.Chmod(dir, storeWipeUnlistableDirMode), ShouldBeNil)

				_, _, err := openStore(ctx, cfg)
				So(err, ShouldWrap, fs.ErrPermission)
				So(err.Error(), ShouldStartWith, "wipe development db "+cfg.dbFile)
			})
		})

		for _, forceBackups := range []bool{false, true} {
			Convey(fmt.Sprintf("Given a development %s store holding a job, with a backup, not asked to wipe and "+
				"forceBackups %v", name, forceBackups), t, func() {
				cfg := storeWipeSeeded(ctx, t, kind)
				cfg.forceBackups = forceBackups

				Convey("an open keeps the store and its backup", func() {
					n, err := inspectorOf(openStoreWiping(ctx, cfg)).count(kindLive)
					So(err, ShouldBeNil)
					So(n, ShouldEqual, 1)
					So(cfg.dbBkFile, shouldExist)
				})
			})
		}

		for _, deployment := range []string{internal.Production, ""} {
			Convey(fmt.Sprintf("Given a %s store of deployment %q holding a job, with a backup", name, deployment), t,
				func() {
					cfg := storeWipeSeeded(ctx, t, kind)
					cfg.deployment = deployment
					cfg.wipeDevDB = true

					Convey("an open asking for a development wipe keeps the store and its backup", func() {
						n, err := inspectorOf(openStoreWiping(ctx, cfg)).count(kindLive)
						So(err, ShouldBeNil)
						So(n, ShouldEqual, 1)
						So(cfg.dbBkFile, shouldExist)
					})
				})
		}

		Convey(fmt.Sprintf("Given a development %s store with an S3 backup path but no forced backups", name), t,
			func() {
				cfg := storeWipeSeeded(ctx, t, kind)
				cfg.wipeDevDB = true
				cfg.dbBkFile = storeWipeS3Backup

				keys := []string{storeWipeS3Key, storeWipeS3Key + "/VERSION"}
				fake := withFakeS3Objects(keys...)

				Convey("an open wipes the store without reaching S3", func() {
					soStoreHoldsNoJobs(openStoreWiping(ctx, cfg))
					So(fake.keys(), ShouldResemble, keys)
					So(fake.openCount(), ShouldEqual, 0)
				})
			})

		Convey(fmt.Sprintf("Given a development %s store with a forced S3 backup of 6 objects", name), t, func() {
			cfg := storeWipeSeeded(ctx, t, kind)
			cfg.wipeDevDB = true
			cfg.forceBackups = true
			cfg.dbBkFile = storeWipeS3Backup

			fake := withFakeS3Objects(
				storeWipeS3Key+"/VERSION",
				storeWipeS3Key+"/00000001.ckpt",
				storeWipeS3Key+"/parts/00000001.wal.0000000000000000",
				storeWipeS3Key,
				storeWipeS3Key+storeWipeBoltPre,
				storeWipeS3Key+storeWipeBoltPreOld,
			)

			Convey("an open deletes them all, reaching the bucket with the url's profile", func() {
				s := openStoreWiping(ctx, cfg)

				soStoreHoldsNoJobs(s)
				So(fake.keys(), ShouldBeEmpty)

				profile, path := fake.openedWith()
				So(profile, ShouldEqual, storeWipeS3Profile)
				So(path, ShouldEqual, storeWipeS3Dir)
			})

			Convey("with no profile in the url, an open deletes them all, reaching the bucket with the default "+
				"profile", func() {
				cfg.dbBkFile = internal.S3Prefix + storeWipeS3Dir + "/db_bk"

				soStoreHoldsNoJobs(openStoreWiping(ctx, cfg))
				So(fake.keys(), ShouldBeEmpty)

				profile, path := fake.openedWith()
				So(profile, ShouldEqual, "default")
				So(path, ShouldEqual, storeWipeS3Dir)
			})

			Convey("with an unclean url, an open deletes them all, reaching the bucket at the cleaned path", func() {
				cfg.dbBkFile = internal.S3Prefix + storeWipeS3Profile + "@" + storeWipeS3Dir + "//db_bk"

				soStoreHoldsNoJobs(openStoreWiping(ctx, cfg))
				So(fake.keys(), ShouldBeEmpty)

				_, path := fake.openedWith()
				So(path, ShouldEqual, storeWipeS3Dir)
			})

			Convey("an open deletes no object outside the development backup's prefix", func() {
				outside := []string{
					"wr/db_bk", "wr/db_bk.development.bolt-prev", "wr/db_bk.development.tmp", "wr/db_bk.development2/VERSION",
					"wr/db_bk.developmentX", "wr/other/db_bk.development",
					storeWipeS3Key + storeRestoringSuffix + "/VERSION", storeWipeS3Key + storeConvertingSuffix,
					storeWipeS3Key + storeCorruptSuffix,
				}
				slices.Sort(outside)
				fake.put(outside...)

				openStoreWiping(ctx, cfg)

				So(fake.keys(), ShouldResemble, outside)
			})

			Convey("and a folder marker object at the backup's prefix, an open deletes them all", func() {
				fake.put(storeWipeS3Key + "/")

				soStoreHoldsNoJobs(openStoreWiping(ctx, cfg))
				So(fake.keys(), ShouldBeEmpty)
			})

			Convey("when S3 cannot be reached, an open fails with that error, and once it can opens again", func() {
				reachable := newS3Objects
				newS3Objects = func(_, _ string) (s3Objects, error) { return nil, errStoreWipeUnreachable }

				_, _, err := openStore(ctx, cfg)
				So(err, ShouldWrap, errStoreWipeUnreachable)
				So(err.Error(), ShouldStartWith, "wipe development db backup "+storeWipeS3Backup)

				newS3Objects = reachable

				soStoreHoldsNoJobs(openStoreWiping(ctx, cfg))
				So(fake.keys(), ShouldBeEmpty)
			})

			Convey("when listing the bucket fails, an open fails with that error, and once it works opens again",
				func() {
					fake.failListing(errStoreWipeList)

					_, _, err := openStore(ctx, cfg)
					So(err, ShouldWrap, errStoreWipeList)
					So(err.Error(), ShouldContainSubstring, "wipe development db backup "+storeWipeS3Backup)

					fake.failListing(nil)

					soStoreHoldsNoJobs(openStoreWiping(ctx, cfg))
					So(fake.keys(), ShouldBeEmpty)
				})

			for _, key := range []string{
				storeWipeS3Key + "/VERSION",
				storeWipeS3Key + "/parts/00000001.wal.0000000000000000",
				storeWipeS3Key,
			} {
				Convey("when deleting "+key+" fails, an open fails with that error, leaving it", func() {
					fake.failDeleting(errStoreWipeDelete, key)

					_, _, err := openStore(ctx, cfg)
					So(err, ShouldWrap, errStoreWipeDelete)
					So(err.Error(), ShouldContainSubstring, "wipe development db backup "+storeWipeS3Backup)
					So(fake.keys(), ShouldContain, key)
				})
			}
		})
	})

	Convey("A production bolt store whose db file is missing is recreated from its S3 backup at the url's key, "+
		"with no deployment suffix", t, func() {
		cfg := storeWipeSeeded(ctx, t, storeKindBolt)
		cfg.deployment = internal.Production
		cfg.wipeDevDB = true
		cfg.dbBkFile = storeWipeS3Backup

		backup, err := os.ReadFile(cfg.dbFile)
		So(err, ShouldBeNil)
		So(os.Remove(cfg.dbFile), ShouldBeNil)

		fake := withFakeS3Objects()
		fake.mu.Lock()
		fake.objects[storeWipeS3ProdKey] = backup
		fake.mu.Unlock()

		s, msg, err := openStore(ctx, cfg)
		So(err, ShouldBeNil)

		Reset(func() { So(s.close(ctx), ShouldBeNil) })

		So(msg, ShouldEqual, "recreated missing db file "+cfg.dbFile+" from s3 backup file "+storeWipeS3ProdKey)

		n, err := inspectorOf(s).count(kindLive)
		So(err, ShouldBeNil)
		So(n, ShouldEqual, 1)
	})

	Convey("A development wipe removes the db and its backup, but leaves the lock file and upgrade sidecar", t,
		func() {
			dir := t.TempDir()
			cfg := storeConfig{
				deployment: internal.Development, wipeDevDB: true,
				dbFile: filepath.Join(dir, "queue.db"), dbBkFile: filepath.Join(dir, testStoreBackupFile),
			}
			kept := []string{cfg.dbFile + storeLockSuffix, internal.DBUpgradeStatusPath(cfg.dbFile)}

			for _, path := range append([]string{cfg.dbFile, cfg.dbBkFile}, kept...) {
				So(os.WriteFile(path, nil, dbFilePermission), ShouldBeNil)
			}

			So(wipeDevStore(cfg), ShouldBeNil)
			So(cfg.dbFile, shouldNotExist)
			So(cfg.dbBkFile, shouldNotExist)

			for _, path := range kept {
				So(path, shouldExist)
			}
		})

	Convey("A development wipe of empty db and backup paths removes nothing from the working directory", t, func() {
		planted := plantInWorkingDir(t, ".bolt-pre-x", "..bolt-pre-x", ".converting", ".restoring", ".corrupt")

		err := wipeDevStore(storeConfig{deployment: internal.Development, wipeDevDB: true})
		So(err, ShouldBeNil)

		for _, path := range planted {
			So(path, shouldExist)
		}
	})
}

// storeWipeSeeded is storeLockSeeded, returning only the config, but with a
// backup for every store kind: a store that makes no backup yet gets a copy of
// its directory at the backup path, as a store-directory backup.
func storeWipeSeeded(ctx context.Context, t *testing.T, kind storeKind) storeConfig {
	t.Helper()

	_, cfg := storeLockSeeded(ctx, t, kind)

	if kind == storeKindLog {
		So(os.CopyFS(cfg.dbBkFile, os.DirFS(cfg.dbFile)), ShouldBeNil)
	}

	So(cfg.dbBkFile, shouldExist)

	return cfg
}

// openStoreWiping opens the store cfg describes, which must succeed, and
// closes it when the Convey block ends.
func openStoreWiping(ctx context.Context, cfg storeConfig) store {
	s, _, err := openStore(ctx, cfg)
	So(err, ShouldBeNil)

	Reset(func() { So(s.close(ctx), ShouldBeNil) })

	return s
}

// soStoreHoldsNoJobs asserts that s stores no live and no complete job.
func soStoreHoldsNoJobs(s store) {
	inspector := inspectorOf(s)

	for _, kind := range []recordKind{kindLive, kindComplete} {
		n, err := inspector.count(kind)
		So(err, ShouldBeNil)
		So(n, ShouldEqual, 0)
	}
}

// plantConversionLeftovers puts beside cfg.dbFile what an interrupted E1
// conversion leaves: a bbolt file holding a job at dbFile's pre name, and a
// ".converting" directory whose SOURCE names it, so a conversion resumed
// before the wipe would store that job. A pre-named file goes beside the
// backup too.
func plantConversionLeftovers(ctx context.Context, t *testing.T, cfg storeConfig) {
	t.Helper()

	boltDir := t.TempDir()
	s := openStoreIn(ctx, storeKindBolt, boltDir)

	_, err := s.storeNewJobsGuarded(ctx, []*Job{testDBJob("echo converted", storeLockRepGroup)}, false, nil)
	So(err, ShouldBeNil)
	So(s.close(ctx), ShouldBeNil)

	pre := cfg.dbFile + storeWipeBoltPre
	So(os.Rename(filepath.Join(boltDir, "queue.db"), pre), ShouldBeNil)

	converting := cfg.dbFile + ".converting"
	So(os.Mkdir(converting, dbUnreadableDirMode), ShouldBeNil)
	So(os.WriteFile(filepath.Join(converting, "SOURCE"), []byte(pre+"\n"), dbFilePermission), ShouldBeNil)
	So(os.WriteFile(cfg.dbBkFile+storeWipeBoltPre, []byte("bbolt"), dbFilePermission), ShouldBeNil)
}

// plantInWorkingDir makes a new temporary directory the working directory
// until the Convey block ends, creates a file in it at each of names, and
// returns their absolute paths.
func plantInWorkingDir(t *testing.T, names ...string) []string {
	t.Helper()

	dir := t.TempDir()

	oldWd, err := os.Getwd()
	So(err, ShouldBeNil)

	Reset(func() { So(os.Chdir(oldWd), ShouldBeNil) })

	So(os.Chdir(dir), ShouldBeNil)

	paths := make([]string, len(names))

	for i, name := range names {
		paths[i] = filepath.Join(dir, name)
		So(os.WriteFile(paths[i], nil, dbFilePermission), ShouldBeNil)
	}

	return paths
}

// conveyUnlessRoot is Convey, or SkipConvey when running as root, which
// bypasses directory modes, so a directory it cannot list cannot be made.
func conveyUnlessRoot(items ...any) {
	if os.Geteuid() == 0 {
		SkipConvey(items...)

		return
	}

	Convey(items...)
}
