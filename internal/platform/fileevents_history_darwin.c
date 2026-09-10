#include <CoreServices/CoreServices.h>
#include <dispatch/dispatch.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>
#include "_cgo_export.h"

// A second, independent FSEvents stream that starts from a recorded event ID
// instead of "now". macOS keeps a per-volume journal of directory-level changes
// that survives reboots; a stream created with sinceWhen = <old ID> replays it
// and marks the end of the replay with kFSEventStreamEventFlagHistoryDone. This
// is the building block a persisted scan cache would be revalidated with
// (R-078); today it only serves the probe that measures whether the replay is
// cheap enough to be worth building on.
//
// Separate from the live watcher in fileevents_darwin.c on purpose: that one is
// a process-wide singleton with its own lifecycle, and a probe must be able to
// open and close several history streams while it runs.
typedef struct {
    FSEventStreamRef stream;
    dispatch_queue_t queue;
    uintptr_t handle;
} marmot_history_stream;

static void marmot_history_callback(ConstFSEventStreamRef stream, void *info, size_t count, void *paths, const FSEventStreamEventFlags flags[], const FSEventStreamEventId ids[]) {
    (void)stream;
    marmot_history_stream *history = (marmot_history_stream *)info;
    if (history == NULL || history->handle == 0 || count == 0) return;
    marmotFileEventHistory(history->handle, count, (char **)paths, (uint32_t *)flags, (uint64_t *)ids);
}

void *marmot_file_event_history_start(const char *root, uint64_t since, double latency, uintptr_t handle) {
    CFStringRef path = CFStringCreateWithCString(NULL, root, kCFStringEncodingUTF8);
    if (path == NULL) return NULL;
    CFArrayRef paths = CFArrayCreate(NULL, (const void **)&path, 1, &kCFTypeArrayCallBacks);
    CFRelease(path);
    if (paths == NULL) return NULL;

    marmot_history_stream *history = (marmot_history_stream *)calloc(1, sizeof(marmot_history_stream));
    if (history == NULL) {
        CFRelease(paths);
        return NULL;
    }
    history->handle = handle;

    FSEventStreamContext context;
    memset(&context, 0, sizeof(context));
    context.info = history;
    // Same shape as the live stream: directory-level events, NoDefer so the
    // first event of a burst arrives at once. Latency is a parameter because
    // whether it paces history delivery too is one of the things R-078 measures.
    FSEventStreamRef stream = FSEventStreamCreate(NULL, &marmot_history_callback, &context, paths, (FSEventStreamEventId)since, (CFTimeInterval)latency, kFSEventStreamCreateFlagNoDefer);
    CFRelease(paths);
    if (stream == NULL) {
        free(history);
        return NULL;
    }
    history->queue = dispatch_queue_create("marmot.fileevents.history", DISPATCH_QUEUE_SERIAL);
    history->stream = stream;
    FSEventStreamSetDispatchQueue(stream, history->queue);
    if (!FSEventStreamStart(stream)) {
        FSEventStreamInvalidate(stream);
        FSEventStreamRelease(stream);
        dispatch_release(history->queue);
        free(history);
        return NULL;
    }
    return history;
}

void marmot_file_event_history_stop(void *opaque) {
    marmot_history_stream *history = (marmot_history_stream *)opaque;
    if (history == NULL) return;
    FSEventStreamStop(history->stream);
    // Invalidate on the stream's own queue: once this returns no callback is
    // running or scheduled, so the Go handle can be released by the caller.
    dispatch_sync(history->queue, ^{
        FSEventStreamInvalidate(history->stream);
        FSEventStreamRelease(history->stream);
        history->handle = 0;
    });
    dispatch_release(history->queue);
    free(history);
}

uint64_t marmot_file_event_current_id(void) {
    return (uint64_t)FSEventsGetCurrentEventId();
}

// posix_time is seconds since 1970: the header documents the CFAbsoluteTime
// parameter as "a posix style time_t" despite the type's usual epoch.
uint64_t marmot_file_event_id_before(int32_t device, double posix_time) {
    return (uint64_t)FSEventsGetLastEventIdForDeviceBeforeTime((dev_t)device, (CFAbsoluteTime)posix_time);
}

// The journal's identity for a device. A recorded event ID is only meaningful
// against the journal that issued it; if this UUID changes, the journal was
// reset and every stored ID is void.
int marmot_file_event_device_uuid(int32_t device, char *out, size_t capacity) {
    CFUUIDRef uuid = FSEventsCopyUUIDForDevice((dev_t)device);
    if (uuid == NULL) return 1;
    CFStringRef text = CFUUIDCreateString(NULL, uuid);
    CFRelease(uuid);
    if (text == NULL) return 1;
    int ok = CFStringGetCString(text, out, (CFIndex)capacity, kCFStringEncodingUTF8) ? 0 : 1;
    CFRelease(text);
    return ok;
}
