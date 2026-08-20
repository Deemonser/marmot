//go:build darwin

package platform

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Foundation
#include <Foundation/Foundation.h>
#include <stdlib.h>

static int marmot_trash(const char *path, char **result, char **message) {
    @autoreleasepool {
        NSString *string = [NSString stringWithUTF8String:path];
        NSURL *url = [NSURL fileURLWithPath:string];
        NSURL *trashed = nil;
        NSError *error = nil;
        if (![[NSFileManager defaultManager] trashItemAtURL:url resultingItemURL:&trashed error:&error]) {
            const char *text = [[error localizedDescription] UTF8String];
            *message = strdup(text == NULL ? "trash failed" : text);
            return 1;
        }
        const char *text = [[trashed path] UTF8String];
        *result = strdup(text == NULL ? "" : text);
        return 0;
    }
}
*/
import "C"

import (
	"fmt"
	"unsafe"
)

func Trash(path string) (string, error) {
	cPath := C.CString(path)
	defer C.free(unsafe.Pointer(cPath))
	var result *C.char
	var message *C.char
	if C.marmot_trash(cPath, &result, &message) != 0 {
		defer C.free(unsafe.Pointer(message))
		return "", fmt.Errorf("move to Trash: %s", C.GoString(message))
	}
	defer C.free(unsafe.Pointer(result))
	return C.GoString(result), nil
}
