//go:build darwin

package platform

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework AppKit
#include <AppKit/AppKit.h>
#include <stdlib.h>
#include <string.h>

// marmot_volume_icon renders the workspace icon for path into a square PNG of
// the given pixel size. NSWorkspace hands back the same NSImage Finder draws,
// so a volume with a custom icon, an external drive, a network share and the
// boot volume each come out looking the way the user already knows them.
// Drawing into an offscreen bitmap is permitted off the main thread, so this
// does not touch the main queue: the caller may itself be on it.
static int marmot_volume_icon(const char *path, int pixels, unsigned char **out, int *length, char **message) {
    @autoreleasepool {
        NSString *string = [NSString stringWithUTF8String:path];
        if (string == nil) { *message = strdup("invalid volume path"); return 1; }
        NSImage *icon = [[NSWorkspace sharedWorkspace] iconForFile:string];
        if (icon == nil) { *message = strdup("no icon for volume"); return 1; }
        NSBitmapImageRep *rep = [[NSBitmapImageRep alloc]
            initWithBitmapDataPlanes:NULL pixelsWide:pixels pixelsHigh:pixels bitsPerSample:8
            samplesPerPixel:4 hasAlpha:YES isPlanar:NO colorSpaceName:NSCalibratedRGBColorSpace
            bytesPerRow:0 bitsPerPixel:0];
        if (rep == nil) { *message = strdup("bitmap allocation failed"); return 1; }
        [NSGraphicsContext saveGraphicsState];
        NSGraphicsContext *context = [NSGraphicsContext graphicsContextWithBitmapImageRep:rep];
        [NSGraphicsContext setCurrentContext:context];
        [icon drawInRect:NSMakeRect(0, 0, pixels, pixels) fromRect:NSZeroRect
            operation:NSCompositingOperationCopy fraction:1.0 respectFlipped:YES
            hints:@{NSImageHintInterpolation: @(NSImageInterpolationHigh)}];
        [context flushGraphics];
        [NSGraphicsContext restoreGraphicsState];
        NSData *png = [rep representationUsingType:NSBitmapImageFileTypePNG properties:@{}];
        if (png == nil || png.length == 0) { *message = strdup("png encoding failed"); return 1; }
        *out = malloc(png.length);
        if (*out == NULL) { *message = strdup("out of memory"); return 1; }
        memcpy(*out, png.bytes, png.length);
        *length = (int)png.length;
        return 0;
    }
}
*/
import "C"

import (
	"fmt"
	"unsafe"
)

// VolumeIcon implements ports.VolumeIcons with the system's own icon for the
// volume mounted at path.
func (Adapter) VolumeIcon(path string, pixels int) ([]byte, error) {
	if pixels <= 0 || pixels > 1024 {
		return nil, fmt.Errorf("volume icon size out of range: %d", pixels)
	}
	cPath := C.CString(path)
	defer C.free(unsafe.Pointer(cPath))
	var out *C.uchar
	var length C.int
	var message *C.char
	if C.marmot_volume_icon(cPath, C.int(pixels), &out, &length, &message) != 0 {
		defer C.free(unsafe.Pointer(message))
		return nil, fmt.Errorf("volume icon failed: %s", C.GoString(message))
	}
	defer C.free(unsafe.Pointer(out))
	return C.GoBytes(unsafe.Pointer(out), length), nil
}
