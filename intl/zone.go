// Copyright 2026 The quickjs-go Authors. All rights reserved.
// Use of the source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package intl // import "modernc.org/quickjs/intl"

import (
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	zoneMu    sync.Mutex
	zoneCache = map[string]zoneCacheEntry{}
)

type zoneCacheEntry struct {
	name string
	loc  *time.Location
	err  error
}

// utcAliases are the identifiers ECMA-402 canonicalizes to "UTC".
var utcAliases = map[string]bool{
	"UTC":           true,
	"GMT":           true,
	"ETC/UTC":       true,
	"ETC/GMT":       true,
	"ETC/GREENWICH": true,
	"ETC/UNIVERSAL": true,
	"ETC/ZULU":      true,
	"GREENWICH":     true,
	"UNIVERSAL":     true,
	"ZULU":          true,
	"UCT":           true,
	"ETC/UCT":       true,
}

// timeZone resolves an IANA time zone name, a UTC alias or an offset time zone
// identifier like "+05:30" to its canonical name and location. The error is
// non-nil for anything else, including "Local": Go accepts it as a location
// name but it is not a time zone identifier and browsers reject it. A name is
// matched regardless of case and reported as the database spells it.
//
// Resolving a name needs the zoneinfo database. Go finds it in the operating
// system's copy or, when the program imports time/tzdata, in the one embedded
// in the binary.
func timeZone(name string) (string, *time.Location, error) {
	zoneMu.Lock()

	defer zoneMu.Unlock()

	if e, ok := zoneCache[name]; ok {
		return e.name, e.loc, e.err
	}

	cname, loc, err := resolveTimeZone(name)
	zoneCache[name] = zoneCacheEntry{cname, loc, err}
	return cname, loc, err
}

func resolveTimeZone(name string) (string, *time.Location, error) {
	// ECMA-402 rejects an unusable identifier with a RangeError, and code that
	// probes a zone catches exactly that.
	invalid := rangeError("invalid time zone specified: %s", name)
	switch {
	case name == "":
		return "", nil, invalid
	case utcAliases[strings.ToUpper(name)]:
		return "UTC", time.UTC, nil
	case name[0] == '+' || name[0] == '-':
		secs, err := parseOffset(name)
		if err != nil {
			return "", nil, invalid
		}

		if secs == 0 {
			return "UTC", time.UTC, nil
		}

		cname := offsetName(secs)
		return cname, time.FixedZone(cname, secs), nil
	}

	// Browsers match IANA identifiers regardless of case and report them
	// as the database spells them. Go's lookup is a file name, which does
	// not find "us/eastern" on most systems and finds it as it is spelled
	// on macOS.
	cname, ok := zoneIDs()[strings.ToUpper(name)]
	if !ok {
		if !newZone(name) {
			return "", nil, invalid
		}

		cname = name
	}

	loc, err := time.LoadLocation(cname)
	if err != nil {
		return "", nil, invalid
	}

	return cname, loc, nil
}

// zoneIDs maps the upper case of every name in zoneNames to the name.
var zoneIDs = sync.OnceValue(func() map[string]string {
	m := map[string]string{}
	for name := range strings.SplitSeq(zoneNames, "\n") {
		m[strings.ToUpper(name)] = name
	}
	return m
})

// newZone reports whether name, which is not in zoneNames, can be a zone the
// database added since: one in a region where IANA adds zones. Such a name is
// taken as it is spelled. Go's zoneinfo directory also has files that are no
// time zone identifiers, which browsers reject: posixrules, localtime, the
// posix and right trees.
func newZone(name string) bool {
	region, _, ok := strings.Cut(name, "/")
	return ok && slices.Contains([]string{
		"Africa", "America", "Antarctica", "Arctic", "Asia", "Atlantic", "Australia", "Europe", "Indian", "Pacific",
	}, region)
}

// parseOffset returns the number of seconds represented by an offset time zone
// identifier: ±HH, ±HHMM or ±HH:MM.
func parseOffset(s string) (int, error) {
	sign := 1
	if s[0] == '-' {
		sign = -1
	}
	s = s[1:]
	var hh, mm string
	switch {
	case len(s) == 2:
		hh = s
	case len(s) == 4:
		hh, mm = s[:2], s[2:]
	case len(s) == 5 && s[2] == ':':
		hh, mm = s[:2], s[3:]
	default:
		return 0, fmt.Errorf("invalid offset %q", s)
	}
	h, err := strconv.Atoi(hh)
	if err != nil || h > 23 {
		return 0, fmt.Errorf("invalid offset hours %q", hh)
	}

	m := 0
	if mm != "" {
		if m, err = strconv.Atoi(mm); err != nil || m > 59 {
			return 0, fmt.Errorf("invalid offset minutes %q", mm)
		}
	}
	return sign * (h*3600 + m*60), nil
}

// offsetName renders secs as the canonical ±HH:MM offset time zone identifier.
func offsetName(secs int) string {
	sign := "+"
	if secs < 0 {
		sign = "-"
		secs = -secs
	}
	return fmt.Sprintf("%s%02d:%02d", sign, secs/3600, secs%3600/60)
}

// gmtOffset renders secs the way en-US displays a GMT offset time zone name:
// "GMT" for zero, "GMT+1" when short and "GMT+01:00" when not.
func gmtOffset(secs int, short bool) string {
	if secs == 0 {
		return "GMT"
	}

	sign := "+"
	if secs < 0 {
		sign = "-"
		secs = -secs
	}
	h, m := secs/3600, secs%3600/60
	if short && m == 0 {
		return fmt.Sprintf("GMT%s%d", sign, h)
	}

	if short {
		return fmt.Sprintf("GMT%s%d:%02d", sign, h, m)
	}

	return fmt.Sprintf("GMT%s%02d:%02d", sign, h, m)
}

// localTimeZone returns the host's IANA time zone name.
//
// Go does not expose it: time.Local carries the zone data but its name is the
// literal "Local" unless $TZ named it, so the zone has to be recovered from the
// same places the C library looks. When none of them answers, the current UTC
// offset is reported instead. That is an ECMA-402 offset time zone identifier,
// so it stays a valid answer for a caller that feeds it back into
// Intl.DateTimeFormat - it merely loses future DST transitions.
func localTimeZone() string {
	if s := os.Getenv("TZ"); s != "" {
		if name, _, err := timeZone(strings.TrimPrefix(s, ":")); err == nil {
			return name
		}
	}

	if s, err := os.Readlink("/etc/localtime"); err == nil {
		if _, after, ok := strings.Cut(s, "zoneinfo/"); ok {
			// Some systems link into the posix tree, which has the same
			// zones under the same names.
			after = strings.TrimPrefix(after, "posix/")
			if name, _, err := timeZone(after); err == nil {
				return name
			}
		}
	}

	if b, err := os.ReadFile("/etc/timezone"); err == nil {
		if name, _, err := timeZone(strings.TrimSpace(string(b))); err == nil {
			return name
		}
	}

	_, secs := time.Now().In(time.Local).Zone()
	if secs == 0 {
		return "UTC"
	}

	return offsetName(secs)
}
