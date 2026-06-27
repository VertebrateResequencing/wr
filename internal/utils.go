/*******************************************************************************
 * Copyright (c) 2016-2021, 2024, 2026 Genome Research Ltd.
 *
 * Author: Sendu Bala <sb10@sanger.ac.uk>
 * Author: Ashwini Chhipa <ac55@sanger.ac.uk>
 * Author: Rosie Kern
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

// this file has general utility functions

import (
	"context"
	"crypto/md5" // #nosec not used for security purposes
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/VertebrateResequencing/wr/clog"
	infoblox "github.com/fanatic/go-infoblox"
	"github.com/shirou/gopsutil/v4/mem"
)

// ZeroCoreMultiplier is the multipler of actual cores we use for the maximum of
// zero core jobs.
const ZeroCoreMultiplier = 2

// for the RandomString implementation.
const (
	randBytes   = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
	randIdxBits = 6                  // 6 bits to represent a rand index
	randIdxMask = 1<<randIdxBits - 1 // All 1-bits, as many as letterIdxBits
	randIdxMax  = 63 / randIdxBits   // # of letter indices fitting in 63 bits
)

// errors returned by InfobloxSetDomainIP.
var (
	errInfobloxLocalhost = errors.New("can't set domain IP when domain is configured as localhost")
	errInfobloxNoHost    = errors.New("INFOBLOX_HOST env var not set")
	errInfobloxNoUser    = errors.New("INFOBLOX_USER env var not set")
	errInfobloxNoPass    = errors.New("INFOBLOX_PASS env var not set")
)

//nolint:gochecknoglobals // process-wide caches of the current user's name and id
var (
	CachedUsername string
	userid         int
)

// SortMapKeysByIntValue sorts the keys of a map[string]int by its values,
// reversed if you supply true as the second arg.
func SortMapKeysByIntValue(imap map[string]int, reverse bool) []string {
	// from http://stackoverflow.com/a/18695428/675083 *** should also try the
	// idiomatic way to see if that's better in any way
	valToKeys := make(map[int][]string, len(imap))
	for key, val := range imap {
		valToKeys[val] = append(valToKeys[val], key)
	}

	vals := make([]int, 0, len(valToKeys))
	for val := range valToKeys {
		vals = append(vals, val)
	}

	if reverse {
		sort.Sort(sort.Reverse(sort.IntSlice(vals)))
	} else {
		sort.Sort(sort.IntSlice(vals))
	}

	sortedKeys := make([]string, 0, len(vals))
	for _, val := range vals {
		sort.Sort(sort.Reverse(sort.StringSlice(valToKeys[val])))
		sortedKeys = append(sortedKeys, valToKeys[val]...)
	}

	return sortedKeys
}

// SortMapKeysByMapIntValue sorts the keys of a map[string]map[string]int by
// a the values found at a given sub value, reversed if you supply true as the
// second arg.
func SortMapKeysByMapIntValue(imap map[string]map[string]int, criterion string, reverse bool) []string {
	criterionValueToKeys := make(map[int][]string, len(imap))
	for key, submap := range imap {
		val := submap[criterion]
		criterionValueToKeys[val] = append(criterionValueToKeys[val], key)
	}

	criterionValues := make([]int, 0, len(criterionValueToKeys))
	for val := range criterionValueToKeys {
		criterionValues = append(criterionValues, val)
	}

	if reverse {
		sort.Sort(sort.Reverse(sort.IntSlice(criterionValues)))
	} else {
		sort.Sort(sort.IntSlice(criterionValues))
	}

	sortedKeys := make([]string, 0, len(criterionValues))
	for _, val := range criterionValues {
		sortedKeys = append(sortedKeys, criterionValueToKeys[val]...)
	}

	return sortedKeys
}

// DedupSortStrings removes duplicates and then sorts the given strings,
// returning a new slice.
func DedupSortStrings(s []string) []string {
	seen := make(map[string]struct{}, len(s))
	i := 0

	for _, v := range s {
		if _, exists := seen[v]; exists {
			continue
		}

		seen[v] = struct{}{}
		s[i] = v
		i++
	}

	dedup := s[:i]
	sort.Strings(dedup)

	return dedup
}

// Username returns the username of the current user. This avoids problems
// with static compilation as it avoids the use of os/user. It will only work
// on linux-like systems where 'id -u -n' works.
func Username() (string, error) {
	if CachedUsername == "" {
		var err error

		CachedUsername, err = parseIDCmd("-u", "-n")
		if err != nil {
			return "", err
		}
	}

	return CachedUsername, nil
}

// Userid returns the user id of the current user. This avoids problems
// with static compilation as it avoids the use of os/user. It will only work
// on linux-like systems where 'id -u' works.
func Userid() (int, error) {
	if userid != 0 {
		return userid, nil
	}

	uidStr, err := parseIDCmd("-u")
	if err != nil {
		return 0, err
	}

	userid, err = strconv.Atoi(uidStr)
	if err != nil {
		return 0, err
	}

	return userid, nil
}

// parseIDCmd parses the output of the unix 'id' command.
func parseIDCmd(idopts ...string) (string, error) {
	idcmd := exec.CommandContext(context.Background(), "/usr/bin/id", idopts...) // #nosec

	idout, err := idcmd.Output()
	if err != nil {
		return "", err
	}

	return strings.TrimSuffix(string(idout), "\n"), err
}

// TildaToHome converts a path beginning with ~/ to the absolute path based in
// the current home directory. If that cannot be determined, path is returned
// unaltered.
func TildaToHome(path string) string {
	home, herr := os.UserHomeDir()
	if herr == nil && home != "" && strings.HasPrefix(path, "~/") {
		path = strings.TrimLeft(path, "~/")
		path = filepath.Join(home, path)
	}

	return path
}

// ProcMeminfoMBs uses gopsutil (amd64 freebsd, linux, windows, darwin, openbds
// only!) to find the total number of MBs of memory physically installed on the
// current system.
func ProcMeminfoMBs() (int, error) {
	v, err := mem.VirtualMemory()
	if err != nil {
		return 0, err
	}

	// convert bytes to MB
	//nolint:gosec // total memory in MB always fits in an int on supported platforms
	return int((v.Total / 1024) / 1024), err
}

// LogClose is for use to Close() an object during a defer when you don't care
// if the Close() returns an error, but do want non-EOF errors logged. Extra
// args are passed as additional context for the logger.
func LogClose(ctx context.Context, obj io.Closer, msg string, extra ...any) {
	err := obj.Close()
	if err != nil && err.Error() != "EOF" && !errors.Is(err, io.EOF) {
		extra = append(extra, "err", err)
		clog.Warn(ctx, "failed to close "+msg, extra...)
	}
}

// LogPanic is for use in a go routines, deferred at the start of them, to
// figure out what is causing runtime panics. If the die bool is true, the
// program exits, otherwise it continues, after logging the error message and
// stack trace. Desc string should be used to describe briefly what the
// goroutine you call this in does.
func LogPanic(ctx context.Context, desc string, die bool) {
	if err := recover(); err != nil {
		clog.Crit(ctx, desc+" panic", "err", err)

		if die {
			os.Exit(1)
		}
	}
}

// Which returns the full path to the executable with the given name that is
// found first in the set of $PATH directories, ignoring any path that is
// actually a symlink to ourselves.
func Which(exeName string) string {
	self, err := os.Executable()
	if err != nil {
		self = ""
	}

	self, err = filepath.EvalSymlinks(self)
	if err != nil {
		self = ""
	}

	for dir := range strings.SplitSeq(os.Getenv("PATH"), string(os.PathListSeparator)) {
		if path := whichInDir(dir, exeName, self); path != "" {
			return path
		}
	}

	return ""
}

// whichInDir looks for an executable called exeName directly inside dir,
// ignoring any match that is a symlink to self. It returns the full path to the
// executable, or the empty string if not found.
func whichInDir(dir, exeName, self string) string {
	stat, err := os.Stat(dir)
	if err != nil || !stat.IsDir() {
		return ""
	}

	exes, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}

	for _, exe := range exes {
		if exe.Name() != exeName {
			continue
		}

		if path := resolveExecutable(filepath.Join(dir, exe.Name()), self); path != "" {
			return path
		}
	}

	return ""
}

// resolveExecutable returns path if it is an executable file that is not a
// symlink to self, otherwise it returns the empty string.
func resolveExecutable(path, self string) string {
	// check that it's not a symlink to ourselves
	path, err := filepath.EvalSymlinks(path)
	if err != nil || path == self {
		return ""
	}

	// check it's executable
	stat, err := os.Stat(path)
	if err == nil && (runtime.GOOS == "windows" || stat.Mode()&0o111 != 0) {
		return path
	}

	return ""
}

// WaitForFile waits as long as timeout for the given file to exist. If the file
// has a timestamp from before the given after, however, waits until the file
// is touched to have a timestamp after after. When it exists with the right
// timestamp, returns true. Otherwise false.
func WaitForFile(file string, after time.Time, timeout time.Duration) bool {
	limit := time.After(timeout)
	ticker := time.NewTicker(50 * time.Millisecond)

	for {
		select {
		case <-ticker.C:
			info, err := os.Stat(file)
			if err == nil && info.ModTime().After(after) {
				ticker.Stop()

				return true
			}

			continue
		case <-limit:
			ticker.Stop()

			return false
		}
	}
}

// InfobloxSetDomainIP uses infoblox to set the IP of a domain. Returns an error
// if INFOBLOX_HOST, INFOBLOX_USER or INFOBLOX_PASS env vars are not set.
func InfobloxSetDomainIP(domain, ip string) error {
	if domain == "localhost" {
		return errInfobloxLocalhost
	}

	// turn off logging built in to go-infoblox
	log.SetFlags(0)
	log.SetOutput(io.Discard)

	host, user, password, err := infobloxCredentials()
	if err != nil {
		return err
	}

	// create infoblox client
	ib := infoblox.NewClient("https://"+host+"/", user, password, true, false)

	// check if it's already set
	objs, err := ib.FindRecordA(domain)
	if err != nil {
		return fmt.Errorf("finding A records failed: %w", err)
	}

	if infobloxAlreadySet(objs, ip) {
		return nil
	}

	if err := infobloxDeleteRecords(ib, objs); err != nil {
		return err
	}

	if err := infobloxCreateRecord(ib, domain, ip); err != nil {
		return err
	}

	// wait a while for things to "really" work
	<-time.After(500 * time.Millisecond)

	return nil
}

// infobloxAlreadySet returns true if there is exactly one existing A record and
// it already points at ip.
func infobloxAlreadySet(objs []infoblox.RecordAObject, ip string) bool {
	return len(objs) == 1 && objs[0].Ipv4Addr == ip
}

// infobloxDeleteRecords deletes any existing A records.
func infobloxDeleteRecords(ib *infoblox.Client, objs []infoblox.RecordAObject) error {
	for _, obj := range objs {
		if err := ib.NetworkObject(obj.Ref).Delete(nil); err != nil {
			return fmt.Errorf("delete of A record failed: %w", err)
		}
	}

	return nil
}

// infobloxCreateRecord adds an A record for domain pointing to ip.
func infobloxCreateRecord(ib *infoblox.Client, domain, ip string) error {
	d := url.Values{}
	d.Set("ipv4addr", ip)
	d.Set("name", domain)

	if _, err := ib.RecordA().Create(d, nil, nil); err != nil {
		return fmt.Errorf("create of A record failed: %w", err)
	}

	return nil
}

// infobloxCredentials reads the infoblox host, user and password from the
// environment, returning an error if any of them are not set.
func infobloxCredentials() (host, user, password string, err error) {
	host = os.Getenv("INFOBLOX_HOST")
	if host == "" {
		return "", "", "", errInfobloxNoHost
	}

	user = os.Getenv("INFOBLOX_USER")
	if user == "" {
		return "", "", "", errInfobloxNoUser
	}

	password = os.Getenv("INFOBLOX_PASS")
	if password == "" {
		return "", "", "", errInfobloxNoPass
	}

	return host, user, password, nil
}

// FileMD5 calculates the MD5 hash checksum of a file, returned as HEX encoded.
func FileMD5(ctx context.Context, path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}

	defer LogClose(ctx, file, "fileMD5", "path", path)

	h := md5.New() // #nosec not used for security purposes

	if _, err := io.Copy(h, file); err != nil {
		return "", err
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

// RandomString generates a random string of length 8 characters.
func RandomString() string {
	// based on http://stackoverflow.com/a/31832326/675083
	b := make([]byte, 8)

	src := rand.NewSource(time.Now().UnixNano())
	for i, cache, remain := 7, src.Int63(), randIdxMax; i >= 0; {
		if remain == 0 {
			cache, remain = src.Int63(), randIdxMax
		}

		if idx := int(cache & randIdxMask); idx < len(randBytes) {
			b[i] = randBytes[idx]
			i--
		}

		cache >>= randIdxBits
		remain--
	}

	return string(b)
}

// CurrentIP returns the IP address of the machine we're running on right now.
// The cidr argument can be an empty string, but if set to the CIDR of the
// machine's primary network, it helps us be sure of getting the correct IP
// address (for when there are multiple network interfaces on the machine).
func CurrentIP(cidr string) (string, error) {
	var ipNet *net.IPNet

	if cidr != "" {
		_, ipn, err := net.ParseCIDR(cidr)
		if err == nil {
			ipNet = ipn
		}
		// *** ignoring error since I don't want to change the return value of
		// this method...
	}

	ctx := context.Background()

	var dialer net.Dialer

	conn, err := dialer.DialContext(ctx, "udp", "8.8.8.8:80") // doesn't actually connect, dest doesn't need to exist
	if err != nil {
		// fall-back on the old method we had...
		return currentIPViaRoute(ctx, ipNet), nil
	}

	defer func() {
		err = conn.Close()
	}()

	udpAddr, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok {
		return currentIPFallback(ipNet)
	}

	ip := udpAddr.IP

	// paranoid confirmation this ip is in our CIDR
	if ipNet == nil {
		return ip.String(), err
	}

	if ipNet.Contains(ip) {
		return ip.String(), err
	}

	return currentIPFallback(ipNet)
}

// currentIPViaRoute figures out our IP address using the system routing table,
// falling back on currentIPFallback() if that doesn't yield an IP in ipNet.
func currentIPViaRoute(ctx context.Context, ipNet *net.IPNet) string {
	// first just hope http://stackoverflow.com/a/25851186/675083 gives us a
	// cross-linux&MacOS solution that works reliably...
	const routeCmd = "ip -4 route get 8.8.8.8 | head -1 | cut -d' ' -f8 | tr -d '\\n'"

	out, err := exec.CommandContext(ctx, "sh", "-c", routeCmd).Output() // #nosec

	var ip string
	if err != nil {
		ip = ipInCIDR(string(out), ipNet)
	}

	// if the above fails, fall back on manually going through all our
	// network interfaces
	if ip == "" {
		//nolint:errcheck // a fallback error just leaves ip empty, as in the original
		ip, _ = currentIPFallback(ipNet)
	}

	return ip
}

// ipInCIDR returns ip unchanged if it is empty, if ipNet is nil, or if ip is
// contained in ipNet; otherwise it returns the empty string.
func ipInCIDR(ip string, ipNet *net.IPNet) string {
	if ip == "" || ipNet == nil {
		return ip
	}

	pip := net.ParseIP(ip)
	if pip != nil && !ipNet.Contains(pip) {
		return ""
	}

	return ip
}

// currentIPFallback is an older fallback method for figuring out our IP
// address by going through all our network interfaces.
func currentIPFallback(ipNet *net.IPNet) (string, error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "", err
	}

	var ip string

	for _, address := range addrs {
		if matched, ok := matchingInterfaceIP(address, ipNet); ok {
			ip = matched

			break
		}
	}

	return ip, nil
}

// matchingInterfaceIP returns the string form of address's IPv4 address and
// true if address is a non-loopback IPv4 *net.IPNet that is in ipNet (or ipNet
// is nil). Otherwise it returns an empty string and false.
func matchingInterfaceIP(address net.Addr, ipNet *net.IPNet) (string, bool) {
	thisIPNet, ok := address.(*net.IPNet)
	if !ok || thisIPNet.IP.IsLoopback() || thisIPNet.IP.To4() == nil {
		return "", false
	}

	if ipNet == nil || ipNet.Contains(thisIPNet.IP) {
		return thisIPNet.IP.String(), true
	}

	return "", false
}

// PathToContent takes the path to a file and returns its contents as a string.
// If path begins with a tilda, TildaToHome() is used to first convert the path
// to an absolute path, in order to find the file.
func PathToContent(path string) (string, error) {
	absPath := TildaToHome(path)

	contents, err := os.ReadFile(absPath)
	if err != nil {
		return "", fmt.Errorf("path [%s] could not be read: %w", absPath, err)
	}

	return string(contents), nil
}
