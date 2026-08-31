//go:build darwin

package platform

/*
#include <sys/attr.h>
#include <sys/vnode.h>
#include <unistd.h>
#include <string.h>
#include <stdlib.h>

// ATTR_CMNEXT_PRIVATESIZE is documented as the number of bytes not shared with
// any other file. On APFS that makes it the reclaimable size: a clone reports
// almost none of what du attributes to it, because the blocks belong to both.
struct marmot_sizes {
	long long alloc;
	long long logical;
	long long private_size;
	int have_private;
};

static int marmot_read_sizes(const char *path, struct marmot_sizes *out) {
	struct attrlist request;
	memset(&request, 0, sizeof(request));
	request.bitmapcount = ATTR_BIT_MAP_COUNT;
	request.commonattr = ATTR_CMN_RETURNED_ATTRS;
	request.fileattr = ATTR_FILE_ALLOCSIZE | ATTR_FILE_DATALENGTH;
	request.forkattr = ATTR_CMNEXT_PRIVATESIZE;

	char buffer[512];
	if (getattrlist(path, &request, buffer, sizeof(buffer), FSOPT_ATTR_CMN_EXTENDED) != 0) return -1;

	char *cursor = buffer + sizeof(uint32_t);
	attribute_set_t returned;
	memcpy(&returned, cursor, sizeof(returned));
	cursor += sizeof(returned);

	if (returned.fileattr & ATTR_FILE_ALLOCSIZE) { memcpy(&out->alloc, cursor, 8); cursor += 8; }
	if (returned.fileattr & ATTR_FILE_DATALENGTH) { memcpy(&out->logical, cursor, 8); cursor += 8; }
	if (returned.forkattr & ATTR_CMNEXT_PRIVATESIZE) {
		memcpy(&out->private_size, cursor, 8);
		out->have_private = 1;
	}
	return 0;
}
*/
import "C"

import (
	"errors"
	"unsafe"
)

// FileSizes is what one file costs, three ways.
type FileSizes struct {
	// Allocated is what lstat and du report: blocks attributed to this file,
	// including blocks it shares with a clone.
	Allocated int64
	Logical   int64
	// Private is the part shared with nothing else -- what deleting this file
	// alone would actually give back. Equal to Allocated for an ordinary file.
	Private int64
	// HasPrivate is false where the filesystem does not report it, in which case
	// Private is meaningless and Allocated is the only answer available.
	HasPrivate bool
}

// ReadFileSizes asks the filesystem for all three at once.
func ReadFileSizes(path string) (FileSizes, error) {
	var sizes C.struct_marmot_sizes
	cPath := C.CString(path)
	defer C.free(unsafe.Pointer(cPath))
	if C.marmot_read_sizes(cPath, &sizes) != 0 {
		return FileSizes{}, errors.New("getattrlist 失败: " + path)
	}
	return FileSizes{
		Allocated:  int64(sizes.alloc),
		Logical:    int64(sizes.logical),
		Private:    int64(sizes.private_size),
		HasPrivate: sizes.have_private != 0,
	}, nil
}
