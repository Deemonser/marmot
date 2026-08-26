package memtree

import (
	"fmt"
	"math"
	"time"
)

// record is one node, packed (ADR-0056 §1). 56 bytes, and deliberately without
// two things the obvious layout would carry:
//
//   - no node ID: the ID *is* the index into the table. The scanner numbers nodes
//     from 1 with no gaps, so an ID field would be 8 bytes of pure redundancy —
//     21 MiB on a 2.8M-node volume. insert enforces the no-gaps property instead
//     of assuming it.
//   - no path: paths average 122 bytes, which is 325 MiB of the same prefixes
//     over and over. A path is rebuilt by walking parents when something actually
//     needs one.
//
// kind/volume/basis/confidence are 1-byte codes: their real cardinality on a full
// volume is 3/1/2/1 (R-057 §3.2).
type record struct {
	parentID       int64
	ownedAllocated int64
	logicalSize    int64
	device         int64
	inode          int64
	modifiedUnix   int64
	nameOffset     uint32
	nameLength     uint16
	kind           uint8
	volume         uint8
	basis          uint8
	confidence     uint8
	flags          uint8
	_              [1]byte
}

const (
	flagHasChildren uint8 = 1 << 0
)

// codeTable interns the handful of distinct strings the scanner repeats on every
// node. It is bounded: an unbounded intern table would be a memory leak dressed
// up as an optimisation, so a scanner that starts producing unique values per
// node fails loudly instead of quietly eating the heap.
type codeTable struct {
	values []string
	index  map[string]uint8
}

const maxCodes = 255

func newCodeTable() *codeTable {
	return &codeTable{index: make(map[string]uint8)}
}

func (t *codeTable) code(value string) (uint8, error) {
	if code, ok := t.index[value]; ok {
		return code, nil
	}
	if len(t.values) >= maxCodes {
		return 0, fmt.Errorf("%w: more than %d distinct values for an interned field", ErrInvalidRequest, maxCodes)
	}
	code := uint8(len(t.values))
	t.values = append(t.values, value)
	t.index[value] = code
	return code, nil
}

func (t *codeTable) value(code uint8) string {
	if int(code) >= len(t.values) {
		return ""
	}
	return t.values[code]
}

// recordPageShift sets the node table's page size. ADR-0057 §3 replaces the
// single growing array with pages: doubling a 178 MiB array copies about twice
// its final size over a scan (511.99 MB measured), and the trim that removed
// the resulting slack copied it once more (225.55 MB). Pages are never copied,
// and the slack they leave is at most one page.
//
// This is a memory budget, not a tuning detail — TestRecordPageIsBudgeted pins
// the byte size, same reason TestRecordStaysPacked pins the record.
const (
	recordPageShift = 12
	recordsPerPage  = 1 << recordPageShift
	recordPageMask  = recordsPerPage - 1
)

// recordTable is the node table: recordTable.at(id) is the node with that ID,
// and index 0 is unused so a zero ID stays invalid.
type recordTable struct {
	pages  [][]record
	length int64
}

func (t *recordTable) len() int64 { return t.length }

// at returns a pointer: both the insert path and the roll-up write in place.
// Callers must have grown the table past id first.
func (t *recordTable) at(id int64) *record {
	return &t.pages[id>>recordPageShift][id&recordPageMask]
}

// grow makes every index below length addressable. It only ever appends pages,
// so nothing already written is copied.
func (t *recordTable) grow(length int64) {
	for int64(len(t.pages))*recordsPerPage < length {
		t.pages = append(t.pages, make([]record, recordsPerPage))
	}
	if length > t.length {
		t.length = length
	}
}

// arenaPageSize is the name arena's page size. A name is at most MaxUint16
// bytes, so one always fits in a fresh page, which is what lets an offset stay
// decodable as (page, offset within page).
const arenaPageSize = 1 << 20

// arena holds every name. Offsets are uint32 and lengths uint16, so both bounds
// are checked on the way in rather than assumed: measured totals are 55.7 MiB
// and 178 bytes, far from the limits, but a silent wrap here would corrupt every
// name after it.
//
// Paged for the same reason as recordTable (ADR-0057 §3): the previous doubling
// array cost 160.43 MB of allocation to build 55.7 MiB of names, plus its share
// of the trim.
type arena struct {
	pages [][]byte
	// next is the write position in the global offset space, which spans pages
	// at a fixed stride so an offset decodes without a lookup table.
	next uint32
}

func (a *arena) put(value string) (uint32, uint16, error) {
	if len(value) > math.MaxUint16 {
		return 0, 0, fmt.Errorf("%w: name of %d bytes exceeds %d", ErrInvalidRequest, len(value), math.MaxUint16)
	}
	pageIndex := int64(a.next / arenaPageSize)
	within := int64(a.next % arenaPageSize)
	if within+int64(len(value)) > arenaPageSize {
		// A name never straddles a page. The skipped tail is at most one name.
		pageIndex++
		within = 0
	}
	offset := pageIndex*arenaPageSize + within
	if offset+int64(len(value)) > math.MaxUint32 {
		return 0, 0, fmt.Errorf("%w: name arena exceeds 4 GiB", ErrInvalidRequest)
	}
	for int64(len(a.pages)) <= pageIndex {
		a.pages = append(a.pages, make([]byte, arenaPageSize))
	}
	copy(a.pages[pageIndex][within:], value)
	a.next = uint32(offset) + uint32(len(value))
	return uint32(offset), uint16(len(value)), nil
}

func (a *arena) get(offset uint32, length uint16) string {
	pageIndex := int(offset / arenaPageSize)
	within := int(offset % arenaPageSize)
	if pageIndex >= len(a.pages) || within+int(length) > arenaPageSize {
		return ""
	}
	return string(a.pages[pageIndex][within : within+int(length)])
}

func modifiedTime(unix int64) time.Time {
	if unix == 0 {
		return time.Time{}
	}
	return time.Unix(0, unix)
}

// trimRoot returns the part of target below root.
func trimRoot(root, target string) (string, bool) {
	if len(target) <= len(root) || target[:len(root)] != root {
		return "", false
	}
	rest := target[len(root):]
	if root != "/" && rest[0] != '/' {
		return "", false
	}
	return rest, true
}

func splitSegments(value string) []string {
	segments := make([]string, 0, 8)
	start := 0
	for index := 0; index <= len(value); index++ {
		if index == len(value) || value[index] == '/' {
			if index > start {
				segments = append(segments, value[start:index])
			}
			start = index + 1
		}
	}
	return segments
}
