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

// Terminal.app opens a directory URL as a document: a new window, shelled into
// that directory. This is what `open -a Terminal <dir>` does underneath, minus
// open(1) -- the path travels as an NSURL, never through a shell (ADR-0015,
// ADR-0069 §5). Only the system Terminal is addressed; when it is missing the
// call fails rather than falling back to anything that would spawn a process.
static int marmot_open_terminal(const char *path, char **message) {
    NSString *string = [NSString stringWithUTF8String:path];
    if (string == nil) { *message = strdup("invalid terminal path"); return 1; }
    NSURL *terminal = [[NSWorkspace sharedWorkspace] URLForApplicationWithBundleIdentifier:@"com.apple.Terminal"];
    if (terminal == nil) { *message = strdup("Terminal.app was not found"); return 1; }
    dispatch_async(dispatch_get_main_queue(), ^{
        NSURL *url = [NSURL fileURLWithPath:string isDirectory:YES];
        NSWorkspaceOpenConfiguration *config = [NSWorkspaceOpenConfiguration configuration];
        [[NSWorkspace sharedWorkspace] openURLs:@[url]
                          withApplicationAtURL:terminal
                                 configuration:config
                             completionHandler:nil];
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

func (Adapter) OpenTerminal(path string) (string, error) {
	cPath := C.CString(path)
	defer C.free(unsafe.Pointer(cPath))
	var message *C.char
	if C.marmot_open_terminal(cPath, &message) != 0 {
		defer C.free(unsafe.Pointer(message))
		return "", fmt.Errorf("Terminal open failed: %s", C.GoString(message))
	}
	return path, nil
}
