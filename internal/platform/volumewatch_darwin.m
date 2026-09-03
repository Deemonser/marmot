#import <AppKit/AppKit.h>
#include "_cgo_export.h"

// The workspace posts these on the main thread when a volume mounts, unmounts
// or is renamed. The observers only forward to Go; what to re-read and when is
// the application layer's business. Three observers, one per name: the
// notification centre has no "any of these" form.
static id marmot_volume_observers[3];

void marmot_volume_watch_start(void) {
    @autoreleasepool {
        NSNotificationCenter *center = [[NSWorkspace sharedWorkspace] notificationCenter];
        NSArray<NSNotificationName> *names = @[NSWorkspaceDidMountNotification, NSWorkspaceDidUnmountNotification, NSWorkspaceDidRenameVolumeNotification];
        for (NSUInteger index = 0; index < names.count; index++) {
            if (marmot_volume_observers[index] != nil) continue;
            marmot_volume_observers[index] = [center addObserverForName:names[index] object:nil queue:nil usingBlock:^(NSNotification *notification) {
                marmotVolumesChanged();
            }];
        }
    }
}

void marmot_volume_watch_stop(void) {
    @autoreleasepool {
        NSNotificationCenter *center = [[NSWorkspace sharedWorkspace] notificationCenter];
        for (NSUInteger index = 0; index < 3; index++) {
            if (marmot_volume_observers[index] == nil) continue;
            [center removeObserver:marmot_volume_observers[index]];
            marmot_volume_observers[index] = nil;
        }
    }
}
