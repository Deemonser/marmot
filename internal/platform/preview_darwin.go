//go:build darwin

package platform

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework AppKit -framework QuickLookUI
#include <AppKit/AppKit.h>
#include <QuickLook/QuickLook.h>
#include <QuickLookUI/QuickLookUI.h>
#include <stdlib.h>

@interface MarmotPreviewItem : NSObject <QLPreviewItem>
@property(nonatomic, strong) NSURL *previewItemURL;
@end

@implementation MarmotPreviewItem
@end

@interface MarmotPreviewSource : NSObject <QLPreviewPanelDataSource>
@property(nonatomic, strong) MarmotPreviewItem *item;
@end

@implementation MarmotPreviewSource
- (NSInteger)numberOfPreviewItemsInPreviewPanel:(QLPreviewPanel *)panel { return self.item == nil ? 0 : 1; }
- (id<QLPreviewItem>)previewPanel:(QLPreviewPanel *)panel previewItemAtIndex:(NSInteger)index { return self.item; }
@end

static MarmotPreviewSource *marmot_source;

static int marmot_preview(const char *path, char **message) {
    NSString *string = [NSString stringWithUTF8String:path];
    if (string == nil) { *message = strdup("invalid preview path"); return 1; }
    dispatch_async(dispatch_get_main_queue(), ^{
        QLPreviewPanel *panel = [QLPreviewPanel sharedPreviewPanel];
        marmot_source = [MarmotPreviewSource new];
        marmot_source.item = [MarmotPreviewItem new];
        marmot_source.item.previewItemURL = [NSURL fileURLWithPath:string];
        panel.dataSource = marmot_source;
        [panel reloadData];
        [panel makeKeyAndOrderFront:nil];
    });
    return 0;
}

static int marmot_reveal(const char *path, char **message) {
    NSString *string = [NSString stringWithUTF8String:path];
    if (string == nil) { *message = strdup("invalid Finder path"); return 1; }
    dispatch_async(dispatch_get_main_queue(), ^{
        NSURL *url = [NSURL fileURLWithPath:string];
        [[NSWorkspace sharedWorkspace] activateFileViewerSelectingURLs:@[url]];
    });
    return 0;
}
*/
import "C"

import (
	"fmt"
	"unsafe"
)

func (Adapter) Preview(path string) (string, error) {
	cPath := C.CString(path)
	defer C.free(unsafe.Pointer(cPath))
	var message *C.char
	if C.marmot_preview(cPath, &message) != 0 {
		defer C.free(unsafe.Pointer(message))
		return "", fmt.Errorf("Quick Look preview failed: %s", C.GoString(message))
	}
	return path, nil
}

func (Adapter) Reveal(path string) (string, error) {
	cPath := C.CString(path)
	defer C.free(unsafe.Pointer(cPath))
	var message *C.char
	if C.marmot_reveal(cPath, &message) != 0 {
		defer C.free(unsafe.Pointer(message))
		return "", fmt.Errorf("Finder reveal failed: %s", C.GoString(message))
	}
	return path, nil
}
