#include <CoreServices/CoreServices.h>
#include <dispatch/dispatch.h>
#include <stdint.h>
#include "_cgo_export.h"

// One FSEvents stream for the process, on its own serial queue. The stream
// reports directories whose contents changed, coalesced by the system over the
// latency given; the Go side does the rest (ADR-0072).
static FSEventStreamRef marmot_stream = NULL;
static dispatch_queue_t marmot_queue = NULL;
static uintptr_t marmot_handle = 0;

static void marmot_file_events_callback(ConstFSEventStreamRef stream, void *info, size_t count, void *paths, const FSEventStreamEventFlags flags[], const FSEventStreamEventId ids[]) {
    (void)stream; (void)info; (void)ids;
    if (count == 0 || marmot_handle == 0) return;
    marmotFileEvents(marmot_handle, count, (char **)paths, (uint32_t *)flags);
}

int marmot_file_events_start(const char *root, double latency, uintptr_t handle) {
    if (marmot_stream != NULL) return 1;
    CFStringRef path = CFStringCreateWithCString(NULL, root, kCFStringEncodingUTF8);
    if (path == NULL) return 1;
    CFArrayRef paths = CFArrayCreate(NULL, (const void **)&path, 1, &kCFTypeArrayCallBacks);
    CFRelease(path);
    if (paths == NULL) return 1;
    // Directory-level events only: a per-file flag would multiply the volume
    // and the application re-lists the directory regardless. NoDefer delivers
    // the first event of a burst at once and the rest after `latency`.
    FSEventStreamRef stream = FSEventStreamCreate(NULL, &marmot_file_events_callback, NULL, paths, kFSEventStreamEventIdSinceNow, latency, kFSEventStreamCreateFlagNoDefer);
    CFRelease(paths);
    if (stream == NULL) return 1;
    if (marmot_queue == NULL) marmot_queue = dispatch_queue_create("marmot.fileevents", DISPATCH_QUEUE_SERIAL);
    marmot_handle = handle;
    FSEventStreamSetDispatchQueue(stream, marmot_queue);
    if (!FSEventStreamStart(stream)) {
        FSEventStreamInvalidate(stream);
        FSEventStreamRelease(stream);
        marmot_handle = 0;
        return 1;
    }
    marmot_stream = stream;
    return 0;
}

void marmot_file_events_stop(void) {
    if (marmot_stream == NULL) return;
    FSEventStreamRef stream = marmot_stream;
    marmot_stream = NULL;
    FSEventStreamStop(stream);
    // Invalidate on the queue itself: after this returns no callback is running
    // or scheduled, so the Go handle can be released.
    dispatch_sync(marmot_queue, ^{
        FSEventStreamInvalidate(stream);
        FSEventStreamRelease(stream);
        marmot_handle = 0;
    });
}
