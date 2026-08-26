//go:build darwin

package scanner

/*
#cgo darwin CFLAGS: -D_DARWIN_C_SOURCE
#include <errno.h>
#include <fcntl.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>
#include <sys/attr.h>
#include <sys/stat.h>
#include <sys/types.h>
#include <time.h>
#include <unistd.h>

typedef struct {
	uint32_t objtype;
	uint32_t device;
	uint64_t file_id;
	uint32_t link_count;
	int64_t alloc_size;
	int64_t data_length;
	int64_t mod_seconds;
	int64_t mod_nanoseconds;
	uint32_t mount_status;
	uint32_t common_attrs;
	uint32_t file_attrs;
	uint32_t name_length;
	char name[1024];
} marmot_bulk_entry;

static int marmot_bulk_open(const char *path) {
	return open(path, O_RDONLY | O_DIRECTORY | O_NOFOLLOW);
}

static int marmot_bulk_close(int fd) {
	return close(fd);
}

static int marmot_bulk_open_at(int parent_fd, const char *name) {
	return openat(parent_fd, name, O_RDONLY | O_DIRECTORY | O_NOFOLLOW);
}

static int marmot_bulk_rewind(int fd) {
	return lseek(fd, 0, SEEK_SET) < 0 ? -errno : 0;
}

static void marmot_bulk_request(struct attrlist *request) {
	memset(request, 0, sizeof(*request));
	request->bitmapcount = ATTR_BIT_MAP_COUNT;
	request->commonattr = ATTR_CMN_RETURNED_ATTRS | ATTR_CMN_NAME | ATTR_CMN_DEVID |
		ATTR_CMN_OBJTYPE | ATTR_CMN_MODTIME | ATTR_CMN_FILEID;
	request->dirattr = ATTR_DIR_MOUNTSTATUS;
	request->fileattr = ATTR_FILE_LINKCOUNT | ATTR_FILE_ALLOCSIZE | ATTR_FILE_DATALENGTH;
}

static const char *marmot_bulk_entry_name(const marmot_bulk_entry *entry) {
	return entry->name;
}

static int marmot_read_u32(const char *base, size_t offset, size_t length, uint32_t *value) {
	if (offset > length || sizeof(*value) > length - offset) return 0;
	memcpy(value, base + offset, sizeof(*value));
	return 1;
}

static int marmot_read_i64(const char *base, size_t offset, size_t length, int64_t *value) {
	if (offset > length || sizeof(*value) > length - offset) return 0;
	memcpy(value, base + offset, sizeof(*value));
	return 1;
}

static int marmot_read_record(const char *record, size_t length, marmot_bulk_entry *out) {
	if (length < 24) return 0;
	const attribute_set_t *returned = (const attribute_set_t *)(record + 4);
	const uint32_t common = returned->commonattr;
	const uint32_t dir = returned->dirattr;
	const uint32_t file = returned->fileattr;
	size_t offset = 24;
	memset(out, 0, sizeof(*out));
	out->link_count = 1;
	out->common_attrs = common;
	out->file_attrs = file;

	if ((common & ATTR_CMN_NAME) == 0 || offset > length || sizeof(attrreference_t) > length - offset) return 0;
	attrreference_t name_ref;
	memcpy(&name_ref, record + offset, sizeof(name_ref));
	if (name_ref.attr_length == 0) return 0;
	const int64_t name_start = (int64_t)offset + name_ref.attr_dataoffset;
	size_t name_length = name_ref.attr_length;
	if (name_start < 0 || (uint64_t)name_start > length) return 0;
	size_t name_offset = (size_t)name_start;
	if (name_length > length - name_offset || name_length >= sizeof(out->name)) return 0;
	if (name_length > 0 && record[name_offset + name_length - 1] == '\0') name_length--;
	memcpy(out->name, record + name_offset, name_length);
	out->name[name_length] = '\0';
	out->name_length = (uint32_t)name_length;
	offset += sizeof(attrreference_t);

	if ((common & ATTR_CMN_DEVID) != 0) {
		if (!marmot_read_u32(record, offset, length, &out->device)) return 0;
		offset += sizeof(uint32_t);
	}
	if ((common & ATTR_CMN_OBJTYPE) != 0) {
		if (!marmot_read_u32(record, offset, length, &out->objtype)) return 0;
		offset += sizeof(uint32_t);
	}
	if ((common & ATTR_CMN_MODTIME) != 0) {
		struct timespec modified;
		if (offset > length || sizeof(modified) > length - offset) return 0;
		memcpy(&modified, record + offset, sizeof(modified));
		out->mod_seconds = (int64_t)modified.tv_sec;
		out->mod_nanoseconds = (int64_t)modified.tv_nsec;
		offset += sizeof(modified);
	}
	if ((common & ATTR_CMN_FILEID) != 0) {
		if (!marmot_read_i64(record, offset, length, (int64_t *)&out->file_id)) return 0;
		offset += sizeof(uint64_t);
	}

	if (out->objtype == 2) {
		if ((dir & ATTR_DIR_MOUNTSTATUS) != 0) {
			if (!marmot_read_u32(record, offset, length, &out->mount_status)) return 0;
		}
		return 1;
	}
	if ((file & ATTR_FILE_LINKCOUNT) != 0) {
		if (!marmot_read_u32(record, offset, length, &out->link_count)) return 0;
		offset += sizeof(uint32_t);
	}
	if ((file & ATTR_FILE_ALLOCSIZE) != 0) {
		if (!marmot_read_i64(record, offset, length, &out->alloc_size)) return 0;
		offset += sizeof(int64_t);
	}
	if ((file & ATTR_FILE_DATALENGTH) != 0) {
		if (!marmot_read_i64(record, offset, length, &out->data_length)) return 0;
	}
	return 1;
}

static int marmot_bulk_read(int fd, void *buffer, size_t buffer_size, marmot_bulk_entry *out, int max_entries) {
	struct attrlist request;
	marmot_bulk_request(&request);

	int count = getattrlistbulk(fd, &request, buffer, buffer_size, 0);
	if (count < 0) return -errno;
	if (count == 0) return 0;
	const char *bytes = (const char *)buffer;
	size_t offset = 0;
	int parsed = 0;
	for (int index = 0; index < count; index++) {
		uint32_t record_length = 0;
		if (!marmot_read_u32(bytes, offset, buffer_size, &record_length) || record_length < 4 || offset > buffer_size || record_length > buffer_size - offset) return -EIO;
		if (parsed >= max_entries || !marmot_read_record(bytes + offset, record_length, &out[parsed])) return -EIO;
		parsed++;
		offset += record_length;
	}
	return parsed;
}

static int marmot_bulk_too_many_entries(void) {
	return -E2BIG;
}

static int marmot_bulk_read_directory_fd(int fd, void *buffer, size_t buffer_size, marmot_bulk_entry *out, int max_entries) {
	struct attrlist request;
	marmot_bulk_request(&request);
	int parsed = 0;
	for (;;) {
		int count = getattrlistbulk(fd, &request, buffer, buffer_size, 0);
		if (count < 0) return -errno;
		if (count == 0) return parsed;
		const char *bytes = (const char *)buffer;
		size_t offset = 0;
		for (int index = 0; index < count; index++) {
			uint32_t record_length = 0;
			if (!marmot_read_u32(bytes, offset, buffer_size, &record_length) || record_length < 4 || offset > buffer_size || record_length > buffer_size - offset) return -EIO;
			if (parsed >= max_entries) {
				return marmot_bulk_too_many_entries();
			}
			if (!marmot_read_record(bytes + offset, record_length, &out[parsed])) return -EIO;
			parsed++;
			offset += record_length;
		}
	}
}

static int marmot_bulk_read_directory(const char *path, void *buffer, size_t buffer_size, marmot_bulk_entry *out, int max_entries) {
	int fd = marmot_bulk_open(path);
	if (fd < 0) return -errno;
	int result = marmot_bulk_read_directory_fd(fd, buffer, buffer_size, out, max_entries);
	marmot_bulk_close(fd);
	return result;
}
*/
import "C"

import (
	"errors"
	"fmt"
	"sync"
	"time"
	"unsafe"
)

const (
	bulkBufferSize            = 256 * 1024
	bulkEntryLimit            = 8192
	bulkCommonDevid    uint32 = 0x00000002
	bulkCommonModtime  uint32 = 0x00000400
	bulkCommonFileID   uint32 = 0x02000000
	bulkFileAllocSize  uint32 = 0x00000004
	bulkFileDataLength uint32 = 0x00000200
)

type bulkReaderState struct {
	buffer  []byte
	entries []C.marmot_bulk_entry
}

var bulkReaderPool sync.Pool

func acquireBulkReader() *bulkReaderState {
	state := bulkReaderPool.Get()
	if state == nil {
		return &bulkReaderState{buffer: make([]byte, bulkBufferSize), entries: make([]C.marmot_bulk_entry, bulkEntryLimit)}
	}
	return state.(*bulkReaderState)
}

func readDirectoryBulk(path string) ([]directoryEntry, error) {
	pathCString := C.CString(path)
	defer C.free(unsafe.Pointer(pathCString))

	reader := acquireBulkReader()
	defer bulkReaderPool.Put(reader)

	count := C.marmot_bulk_read_directory(pathCString, unsafe.Pointer(&reader.buffer[0]), C.size_t(len(reader.buffer)), &reader.entries[0], C.int(len(reader.entries)))
	if int(count) == int(C.marmot_bulk_too_many_entries()) {
		return readDirectoryBulkBatched(pathCString, reader)
	}
	if count < 0 {
		return nil, fmt.Errorf("getattrlistbulk: errno %d", -int(count))
	}
	return decodeBulkEntries(reader.entries, int(count))
}

func readDirectoryBulkFD(fd int) ([]directoryEntry, error) {
	reader := acquireBulkReader()
	defer bulkReaderPool.Put(reader)

	count := C.marmot_bulk_read_directory_fd(C.int(fd), unsafe.Pointer(&reader.buffer[0]), C.size_t(len(reader.buffer)), &reader.entries[0], C.int(len(reader.entries)))
	if int(count) == int(C.marmot_bulk_too_many_entries()) {
		if rewind := C.marmot_bulk_rewind(C.int(fd)); rewind < 0 {
			return nil, fmt.Errorf("rewind directory: errno %d", -int(rewind))
		}
		return readDirectoryBulkBatchedFD(C.int(fd), reader)
	}
	if count < 0 {
		return nil, fmt.Errorf("getattrlistbulk: errno %d", -int(count))
	}
	return decodeBulkEntries(reader.entries, int(count))
}

func readDirectoryBulkBatched(pathCString *C.char, reader *bulkReaderState) ([]directoryEntry, error) {
	fd := C.marmot_bulk_open(pathCString)
	if fd < 0 {
		return nil, fmt.Errorf("open directory: %w", errors.New("getattrlistbulk open failed"))
	}
	defer C.marmot_bulk_close(fd)
	return readDirectoryBulkBatchedFD(fd, reader)
}

func readDirectoryBulkBatchedFD(fd C.int, reader *bulkReaderState) ([]directoryEntry, error) {
	result := make([]directoryEntry, 0, 64)
	for {
		count := C.marmot_bulk_read(fd, unsafe.Pointer(&reader.buffer[0]), C.size_t(len(reader.buffer)), &reader.entries[0], C.int(len(reader.entries)))
		if count < 0 {
			return nil, fmt.Errorf("getattrlistbulk: errno %d", -int(count))
		}
		if count == 0 {
			return result, nil
		}
		entries, err := decodeBulkEntries(reader.entries, int(count))
		if err != nil {
			return nil, err
		}
		result = append(result, entries...)
	}
}

func openDirectoryPath(path string) (int, error) {
	pathCString := C.CString(path)
	defer C.free(unsafe.Pointer(pathCString))
	fd := C.marmot_bulk_open(pathCString)
	if fd < 0 {
		return -1, fmt.Errorf("open directory: %w", errors.New("open failed"))
	}
	return int(fd), nil
}

func openDirectoryAt(parentFD int, name string) (int, error) {
	nameCString := C.CString(name)
	defer C.free(unsafe.Pointer(nameCString))
	fd := C.marmot_bulk_open_at(C.int(parentFD), nameCString)
	if fd < 0 {
		return -1, fmt.Errorf("open child directory: %w", errors.New("openat failed"))
	}
	return int(fd), nil
}

func closeDirectoryFD(fd int) {
	if fd >= 0 {
		C.marmot_bulk_close(C.int(fd))
	}
}

func decodeBulkEntries(rawEntries []C.marmot_bulk_entry, count int) ([]directoryEntry, error) {
	result := make([]directoryEntry, 0, count)
	for index := 0; index < count; index++ {
		raw := &rawEntries[index]
		nameLength := int(raw.name_length)
		if nameLength == 0 {
			return nil, errors.New("getattrlistbulk returned an empty name")
		}
		name := string(C.GoBytes(unsafe.Pointer(C.marmot_bulk_entry_name(raw)), C.int(nameLength)))
		confidence := "exact"
		basis := "darwin_getattrlistbulk_v1"
		if uint32(raw.common_attrs)&(bulkCommonDevid|bulkCommonFileID|bulkCommonModtime) != (bulkCommonDevid | bulkCommonFileID | bulkCommonModtime) {
			confidence = "partial"
		}
		if raw.objtype != 2 && uint32(raw.file_attrs)&(bulkFileAllocSize|bulkFileDataLength) != (bulkFileAllocSize|bulkFileDataLength) {
			confidence = "partial"
		}
		logicalSize := int64(raw.data_length)
		allocatedSize := int64(raw.alloc_size)
		if logicalSize < 0 {
			logicalSize = 0
		}
		if allocatedSize < 0 {
			allocatedSize = 0
		}
		result = append(result, directoryEntry{
			name:          name,
			logicalSize:   logicalSize,
			allocatedSize: allocatedSize,
			device:        uint64(raw.device),
			inode:         uint64(raw.file_id),
			linkCount:     uint64(raw.link_count),
			modifiedAt:    time.Unix(int64(raw.mod_seconds), int64(raw.mod_nanoseconds)),
			isDirectory:   raw.objtype == 2,
			isSymlink:     raw.objtype == 5,
			mountPoint:    raw.mount_status != 0,
			confidence:    confidence,
			sizeBasis:     basis,
		})
	}
	return result, nil
}
