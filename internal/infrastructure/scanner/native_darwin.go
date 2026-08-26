//go:build darwin

package scanner

/*
#cgo darwin CFLAGS: -D_DARWIN_C_SOURCE
#include <errno.h>
#include <fcntl.h>
#include <limits.h>
#include <pthread.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/attr.h>
#include <sys/stat.h>
#include <sys/types.h>
#include <time.h>
#include <unistd.h>

#define MARMOT_NATIVE_QUEUE_CAP 4096
#define MARMOT_NATIVE_FD_CAP 2048
#define MARMOT_NATIVE_BATCH_CAP 8192
#define MARMOT_NATIVE_CHILD_CAP 4096
#define MARMOT_NATIVE_BUFFER_SIZE (256 * 1024)

typedef struct {
	uint64_t parent_id;
	uint64_t node_id;
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
	char name[NAME_MAX + 1];
} marmot_native_entry;

typedef struct {
	uint64_t node_id;
	size_t batch_index;
	uint32_t name_length;
	char name[NAME_MAX + 1];
} marmot_native_child;

typedef struct {
	uint64_t parent_id;
	int fd;
	int fd_held;
	int is_root;
	char *path;
	size_t path_length;
} marmot_native_task;

typedef struct {
	char *path;
	size_t length;
} marmot_native_boundary;

typedef struct marmot_native_state marmot_native_state;

typedef struct {
	marmot_native_task items[MARMOT_NATIVE_QUEUE_CAP];
	size_t head;
	size_t tail;
	size_t count;
	uint64_t pending;
	int stopping;
	int cancelled;
	int held_fds;
	pthread_mutex_t mutex;
	pthread_cond_t not_empty;
	pthread_cond_t not_full;
	pthread_cond_t done;
} marmot_native_queue;

struct marmot_native_state {
	uintptr_t handle;
	marmot_native_boundary *boundaries;
	size_t boundary_count;
	int worker_count;
	pthread_t *workers;
	pthread_mutex_t id_mutex;
	uint64_t next_id;
	marmot_native_queue queue;
};

extern int marmotNativeBatchCallback(uintptr_t handle, marmot_native_entry *entries, size_t count);
extern int marmotNativeIssueCallback(uintptr_t handle, char *path, char *message);
extern int marmotNativeRootDoneCallback(uintptr_t handle);

static void marmot_native_task_dispose(marmot_native_queue *queue, marmot_native_task *task) {
	if (task == NULL) return;
	if (task->fd >= 0) close(task->fd);
	if (task->fd_held) {
		pthread_mutex_lock(&queue->mutex);
		if (queue->held_fds > 0) queue->held_fds--;
		pthread_mutex_unlock(&queue->mutex);
	}
	free(task->path);
	task->fd = -1;
	task->fd_held = 0;
	task->path = NULL;
}

static void marmot_native_queue_cancel_locked(marmot_native_queue *queue) {
	queue->stopping = 1;
	queue->cancelled = 1;
	while (queue->count > 0) {
		marmot_native_task task = queue->items[queue->head];
		queue->head = (queue->head + 1) % MARMOT_NATIVE_QUEUE_CAP;
		queue->count--;
		if (queue->pending > 0) queue->pending--;
		if (task.fd >= 0) close(task.fd);
		if (task.fd_held && queue->held_fds > 0) queue->held_fds--;
		free(task.path);
	}
	pthread_cond_broadcast(&queue->not_empty);
	pthread_cond_broadcast(&queue->not_full);
	if (queue->pending == 0) pthread_cond_broadcast(&queue->done);
}

static void marmot_native_queue_cancel(marmot_native_queue *queue) {
	pthread_mutex_lock(&queue->mutex);
	marmot_native_queue_cancel_locked(queue);
	pthread_mutex_unlock(&queue->mutex);
}

static int marmot_native_queue_push(marmot_native_queue *queue, marmot_native_task task) {
	pthread_mutex_lock(&queue->mutex);
	while (queue->count == MARMOT_NATIVE_QUEUE_CAP && !queue->stopping) {
		pthread_cond_wait(&queue->not_full, &queue->mutex);
	}
	if (queue->stopping) {
		pthread_mutex_unlock(&queue->mutex);
		return 0;
	}
	queue->items[queue->tail] = task;
	queue->tail = (queue->tail + 1) % MARMOT_NATIVE_QUEUE_CAP;
	queue->count++;
	queue->pending++;
	pthread_cond_signal(&queue->not_empty);
	pthread_mutex_unlock(&queue->mutex);
	return 1;
}

static int marmot_native_queue_try_push(marmot_native_queue *queue, marmot_native_task task) {
	pthread_mutex_lock(&queue->mutex);
	if (queue->stopping) {
		pthread_mutex_unlock(&queue->mutex);
		return 0;
	}
	if (queue->count == MARMOT_NATIVE_QUEUE_CAP) {
		pthread_mutex_unlock(&queue->mutex);
		return -1;
	}
	queue->items[queue->tail] = task;
	queue->tail = (queue->tail + 1) % MARMOT_NATIVE_QUEUE_CAP;
	queue->count++;
	queue->pending++;
	pthread_cond_signal(&queue->not_empty);
	pthread_mutex_unlock(&queue->mutex);
	return 1;
}

static int marmot_native_queue_pop(marmot_native_queue *queue, marmot_native_task *task) {
	pthread_mutex_lock(&queue->mutex);
	while (queue->count == 0 && !queue->stopping) {
		pthread_cond_wait(&queue->not_empty, &queue->mutex);
	}
	if (queue->count == 0) {
		pthread_mutex_unlock(&queue->mutex);
		return 0;
	}
	*task = queue->items[queue->head];
	queue->head = (queue->head + 1) % MARMOT_NATIVE_QUEUE_CAP;
	queue->count--;
	pthread_cond_signal(&queue->not_full);
	pthread_mutex_unlock(&queue->mutex);
	return 1;
}

static void marmot_native_queue_done(marmot_native_queue *queue) {
	pthread_mutex_lock(&queue->mutex);
	if (queue->pending > 0) queue->pending--;
	if (queue->pending == 0) {
		queue->stopping = 1;
		pthread_cond_broadcast(&queue->done);
		pthread_cond_broadcast(&queue->not_empty);
		pthread_cond_broadcast(&queue->not_full);
	}
	pthread_mutex_unlock(&queue->mutex);
}

static int marmot_native_reserve_fd(marmot_native_queue *queue) {
	pthread_mutex_lock(&queue->mutex);
	if (queue->held_fds >= MARMOT_NATIVE_FD_CAP || queue->stopping) {
		pthread_mutex_unlock(&queue->mutex);
		return 0;
	}
	queue->held_fds++;
	pthread_mutex_unlock(&queue->mutex);
	return 1;
}

static void marmot_native_release_fd(marmot_native_queue *queue) {
	pthread_mutex_lock(&queue->mutex);
	if (queue->held_fds > 0) queue->held_fds--;
	pthread_mutex_unlock(&queue->mutex);
}

static uint64_t marmot_native_reserve_ids(marmot_native_state *state, size_t count) {
	pthread_mutex_lock(&state->id_mutex);
	uint64_t first = state->next_id;
	state->next_id += (uint64_t)count;
	pthread_mutex_unlock(&state->id_mutex);
	return first;
}

static void marmot_native_process_task(marmot_native_state *state, marmot_native_task *task, int counted, void *buffer, marmot_native_entry *batch, marmot_native_child *children_buffer, size_t children_capacity);

static int marmot_native_candidate_prefix_matches(const char *parent, size_t parent_length, const char *name, size_t name_length, const char *prefix, size_t prefix_length) {
	int separator = parent_length > 0 && parent[parent_length - 1] != '/';
	if (parent_length > SIZE_MAX - name_length) return 0;
	size_t candidate_length = parent_length + name_length;
	if (separator) {
		if (candidate_length == SIZE_MAX) return 0;
		candidate_length++;
	}
	if (prefix_length > candidate_length) return 0;

	if (prefix_length <= parent_length) {
		if (prefix_length > 0 && memcmp(parent, prefix, prefix_length) != 0) return 0;
	} else {
		if (parent_length > 0 && memcmp(parent, prefix, parent_length) != 0) return 0;
		size_t remaining = prefix_length - parent_length;
		if (separator) {
			if (remaining == 0 || prefix[parent_length] != '/') return 0;
			remaining--;
			if (remaining > name_length || (remaining > 0 && memcmp(name, prefix + parent_length + 1, remaining) != 0)) return 0;
		} else if (remaining > name_length || (remaining > 0 && memcmp(name, prefix + parent_length, remaining) != 0)) {
			return 0;
		}
	}
	if (prefix_length == candidate_length) return 1;

	char next;
	if (prefix_length < parent_length) {
		next = parent[prefix_length];
	} else {
		size_t offset = prefix_length - parent_length;
		if (separator) {
			if (offset == 0) {
				next = '/';
			} else {
				offset--;
				if (offset >= name_length) return 0;
				next = name[offset];
			}
		} else {
			if (offset >= name_length) return 0;
			next = name[offset];
		}
	}
	return next == '/';
}

static int marmot_native_is_boundary(marmot_native_state *state, const char *parent, size_t parent_length, const char *name, size_t name_length) {
	for (size_t index = 0; index < state->boundary_count; index++) {
		marmot_native_boundary *boundary = &state->boundaries[index];
		if (marmot_native_candidate_prefix_matches(parent, parent_length, name, name_length, boundary->path, boundary->length)) return 1;
	}
	return 0;
}

static char *marmot_native_join(const char *parent, size_t parent_length, const char *name, size_t name_length) {
	int separator = parent_length > 0 && parent[parent_length - 1] != '/';
	if (parent_length > SIZE_MAX - name_length) return NULL;
	size_t length = parent_length + name_length;
	if (separator) {
		if (length == SIZE_MAX) return NULL;
		length++;
	}
	if (length == SIZE_MAX) return NULL;
	length++;
	char *result = (char *)malloc(length);
	if (result == NULL) return NULL;
	memcpy(result, parent, parent_length);
	size_t offset = parent_length;
	if (separator) result[offset++] = '/';
	memcpy(result + offset, name, name_length);
	result[offset + name_length] = '\0';
	return result;
}

static void marmot_native_request(struct attrlist *request) {
	memset(request, 0, sizeof(*request));
	request->bitmapcount = ATTR_BIT_MAP_COUNT;
	request->commonattr = ATTR_CMN_RETURNED_ATTRS | ATTR_CMN_NAME | ATTR_CMN_DEVID |
		ATTR_CMN_OBJTYPE | ATTR_CMN_MODTIME | ATTR_CMN_FILEID;
	request->dirattr = ATTR_DIR_MOUNTSTATUS;
	request->fileattr = ATTR_FILE_LINKCOUNT | ATTR_FILE_ALLOCSIZE | ATTR_FILE_DATALENGTH;
}

static int marmot_native_read_u32(const char *base, size_t offset, size_t length, uint32_t *value) {
	if (offset > length || sizeof(*value) > length - offset) return 0;
	memcpy(value, base + offset, sizeof(*value));
	return 1;
}

static int marmot_native_read_i64(const char *base, size_t offset, size_t length, int64_t *value) {
	if (offset > length || sizeof(*value) > length - offset) return 0;
	memcpy(value, base + offset, sizeof(*value));
	return 1;
}

static int marmot_native_read_record(const char *record, size_t length, marmot_native_entry *out) {
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
	int64_t name_start = (int64_t)offset + name_ref.attr_dataoffset;
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
		if (!marmot_native_read_u32(record, offset, length, &out->device)) return 0;
		offset += sizeof(uint32_t);
	}
	if ((common & ATTR_CMN_OBJTYPE) != 0) {
		if (!marmot_native_read_u32(record, offset, length, &out->objtype)) return 0;
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
		if (!marmot_native_read_i64(record, offset, length, (int64_t *)&out->file_id)) return 0;
		offset += sizeof(uint64_t);
	}
	if (out->objtype == 2) {
		if ((dir & ATTR_DIR_MOUNTSTATUS) != 0 && !marmot_native_read_u32(record, offset, length, &out->mount_status)) return 0;
		return 1;
	}
	if ((file & ATTR_FILE_LINKCOUNT) != 0) {
		if (!marmot_native_read_u32(record, offset, length, &out->link_count)) return 0;
		offset += sizeof(uint32_t);
	}
	if ((file & ATTR_FILE_ALLOCSIZE) != 0) {
		if (!marmot_native_read_i64(record, offset, length, &out->alloc_size)) return 0;
		offset += sizeof(int64_t);
	}
	if ((file & ATTR_FILE_DATALENGTH) != 0 && !marmot_native_read_i64(record, offset, length, &out->data_length)) return 0;
	return 1;
}

static int marmot_native_emit_batch(marmot_native_state *state, marmot_native_entry *entries, size_t count) {
	if (count == 0) return 0;
	return marmotNativeBatchCallback(state->handle, entries, count) != 0;
}

static int marmot_native_emit_issue(marmot_native_state *state, const char *path, const char *message) {
	return marmotNativeIssueCallback(state->handle, (char *)path, (char *)message) != 0;
}

static int marmot_native_schedule_children(marmot_native_state *state, marmot_native_task *parent, marmot_native_child *children, size_t count, void *buffer, marmot_native_entry *batch) {
	for (size_t index = 0; index < count; index++) {
		marmot_native_child *child = &children[index];
		char *path = marmot_native_join(parent->path, parent->path_length, child->name, child->name_length);
		if (path == NULL) return 1;
		marmot_native_task task;
		memset(&task, 0, sizeof(task));
		task.parent_id = child->node_id;
		task.fd = -1;
		task.path = path;
		task.path_length = parent->path_length + child->name_length + (parent->path_length > 0 && parent->path[parent->path_length - 1] != '/');
		if (parent->fd >= 0 && marmot_native_reserve_fd(&state->queue)) {
			int child_fd = openat(parent->fd, child->name, O_RDONLY | O_DIRECTORY | O_NOFOLLOW | O_CLOEXEC);
			if (child_fd >= 0) {
				task.fd = child_fd;
				task.fd_held = 1;
			} else {
				marmot_native_release_fd(&state->queue);
			}
		}
		int queued = marmot_native_queue_try_push(&state->queue, task);
		if (queued == 0) {
			marmot_native_task_dispose(&state->queue, &task);
			return 1;
		}
		if (queued < 0) {
			marmot_native_process_task(state, &task, 0, buffer, batch, NULL, 0);
			if (state->queue.cancelled) return 1;
		}
	}
	return 0;
}

static int marmot_native_flush_batch(marmot_native_state *state, marmot_native_task *task, marmot_native_entry *batch, size_t batch_count, marmot_native_child *children, size_t child_count, void *buffer) {
	if (batch_count == 0) return 0;
	uint64_t first_id = marmot_native_reserve_ids(state, batch_count);
	for (size_t index = 0; index < batch_count; index++) {
		batch[index].parent_id = task->parent_id;
		batch[index].node_id = first_id + (uint64_t)index;
	}
	for (size_t index = 0; index < child_count; index++) {
		children[index].node_id = batch[children[index].batch_index].node_id;
	}
	if (marmot_native_emit_batch(state, batch, batch_count)) return 1;
	return marmot_native_schedule_children(state, task, children, child_count, buffer, batch);
}

static void marmot_native_process_task(marmot_native_state *state, marmot_native_task *task, int counted, void *buffer, marmot_native_entry *batch, marmot_native_child *children_buffer, size_t children_capacity) {
	int fd = task->fd;
	if (fd < 0) fd = open(task->path, O_RDONLY | O_DIRECTORY | O_NOFOLLOW | O_CLOEXEC);
	if (fd < 0) {
		char message[128];
		snprintf(message, sizeof(message), "open directory: errno %d", errno);
		if (marmot_native_emit_issue(state, task->path, message)) marmot_native_queue_cancel(&state->queue);
		if (task->is_root && !state->queue.cancelled && marmotNativeRootDoneCallback(state->handle) != 0) marmot_native_queue_cancel(&state->queue);
		marmot_native_task_dispose(&state->queue, task);
		if (counted) marmot_native_queue_done(&state->queue);
		return;
	}
	struct attrlist request;
	marmot_native_request(&request);
	size_t batch_count = 0;
	for (;;) {
		if (state->queue.cancelled) break;
		errno = 0;
		int count = getattrlistbulk(fd, &request, buffer, MARMOT_NATIVE_BUFFER_SIZE, 0);
		if (count < 0) {
			char message[128];
			snprintf(message, sizeof(message), "getattrlistbulk: errno %d", errno);
			if (marmot_native_emit_issue(state, task->path, message)) marmot_native_queue_cancel(&state->queue);
			break;
		}
		if (count == 0) break;
		const char *bytes = (const char *)buffer;
		size_t offset = 0;
		int owns_children = 0;
		marmot_native_child *children = children_buffer;
		if (children == NULL || (size_t)count > children_capacity) {
			children = (marmot_native_child *)malloc((size_t)count * sizeof(marmot_native_child));
			owns_children = 1;
		}
		if (children == NULL) {
			if (marmot_native_emit_issue(state, task->path, "native scanner child allocation failed")) marmot_native_queue_cancel(&state->queue);
			break;
		}
		size_t child_count = 0;
		for (int index = 0; index < count; index++) {
			uint32_t record_length = 0;
			if (offset > MARMOT_NATIVE_BUFFER_SIZE || !marmot_native_read_u32(bytes, offset, MARMOT_NATIVE_BUFFER_SIZE, &record_length) || record_length < 4 || record_length > MARMOT_NATIVE_BUFFER_SIZE - offset) {
				if (marmot_native_emit_issue(state, task->path, "getattrlistbulk returned an invalid record")) marmot_native_queue_cancel(&state->queue);
				break;
			}
			marmot_native_entry entry;
			memset(&entry, 0, sizeof(entry));
			if (!marmot_native_read_record(bytes + offset, record_length, &entry)) {
				if (marmot_native_emit_issue(state, task->path, "getattrlistbulk record attributes were invalid")) marmot_native_queue_cancel(&state->queue);
				break;
			}
			offset += record_length;
			if (entry.objtype == 2) {
				int boundary = entry.mount_status != 0 || marmot_native_is_boundary(state, task->path, task->path_length, entry.name, entry.name_length);
				if (boundary) continue;
			}
				if (state->queue.cancelled) break;
				if (batch_count >= MARMOT_NATIVE_BATCH_CAP) {
					if (marmot_native_flush_batch(state, task, batch, batch_count, children, child_count, buffer)) {
						marmot_native_queue_cancel(&state->queue);
						break;
					}
					batch_count = 0;
					child_count = 0;
				}
				batch[batch_count] = entry;
				size_t batch_index = batch_count++;
				if (entry.objtype == 2) {
					children[child_count].batch_index = batch_index;
					children[child_count].name_length = entry.name_length;
					memcpy(children[child_count].name, entry.name, entry.name_length + 1);
					child_count++;
				}
			}
		if (!state->queue.cancelled && batch_count > 0) {
			if (marmot_native_flush_batch(state, task, batch, batch_count, children, child_count, buffer)) marmot_native_queue_cancel(&state->queue);
			batch_count = 0;
			child_count = 0;
		}
		if (owns_children) free(children);
		if (offset == 0 || state->queue.cancelled) break;
	}
	close(fd);
	task->fd = -1;
	if (task->is_root && !state->queue.cancelled && marmotNativeRootDoneCallback(state->handle) != 0) marmot_native_queue_cancel(&state->queue);
	marmot_native_task_dispose(&state->queue, task);
	if (counted) marmot_native_queue_done(&state->queue);
}

static void *marmot_native_worker(void *opaque) {
	marmot_native_state *state = (marmot_native_state *)opaque;
	void *buffer = malloc(MARMOT_NATIVE_BUFFER_SIZE);
	marmot_native_entry *batch = (marmot_native_entry *)malloc(MARMOT_NATIVE_BATCH_CAP * sizeof(marmot_native_entry));
	marmot_native_child *children = (marmot_native_child *)malloc(MARMOT_NATIVE_CHILD_CAP * sizeof(marmot_native_child));
	if (buffer == NULL || batch == NULL || children == NULL) {
		free(buffer);
		free(batch);
		free(children);
		marmot_native_queue_cancel(&state->queue);
		return NULL;
	}
	for (;;) {
		marmot_native_task task;
		memset(&task, 0, sizeof(task));
		task.fd = -1;
		if (!marmot_native_queue_pop(&state->queue, &task)) break;
		marmot_native_process_task(state, &task, 1, buffer, batch, children, MARMOT_NATIVE_CHILD_CAP);
	}
	free(buffer);
	free(batch);
	free(children);
	return NULL;
}

static void *marmot_native_alloc_boundaries(size_t count) {
	return calloc(count, sizeof(marmot_native_boundary));
}

static void marmot_native_set_boundary(marmot_native_boundary *boundaries, size_t index, const char *value) {
	boundaries[index].path = strdup(value);
	boundaries[index].length = strlen(value);
}

static void marmot_native_free_boundaries(marmot_native_boundary *boundaries, size_t count) {
	if (boundaries == NULL) return;
	for (size_t index = 0; index < count; index++) free(boundaries[index].path);
	free(boundaries);
}

static void *marmot_native_new(uintptr_t handle, marmot_native_boundary *boundaries, size_t boundary_count, int worker_count) {
	if (worker_count < 1 || worker_count > 8) return NULL;
	marmot_native_state *state = (marmot_native_state *)calloc(1, sizeof(marmot_native_state));
	if (state == NULL) return NULL;
	state->handle = handle;
	state->boundaries = boundaries;
	state->boundary_count = boundary_count;
	state->worker_count = worker_count;
	state->next_id = 2;
	pthread_mutex_init(&state->id_mutex, NULL);
	pthread_mutex_init(&state->queue.mutex, NULL);
	pthread_cond_init(&state->queue.not_empty, NULL);
	pthread_cond_init(&state->queue.not_full, NULL);
	pthread_cond_init(&state->queue.done, NULL);
	return state;
}

static void marmot_native_free(void *opaque) {
	marmot_native_state *state = (marmot_native_state *)opaque;
	if (state == NULL) return;
	pthread_cond_destroy(&state->queue.done);
	pthread_cond_destroy(&state->queue.not_full);
	pthread_cond_destroy(&state->queue.not_empty);
	pthread_mutex_destroy(&state->queue.mutex);
	pthread_mutex_destroy(&state->id_mutex);
	free(state->workers);
	free(state);
}

static void marmot_native_cancel(void *opaque) {
	marmot_native_state *state = (marmot_native_state *)opaque;
	if (state != NULL) marmot_native_queue_cancel(&state->queue);
}

static int marmot_native_run(void *opaque, const char *root) {
	marmot_native_state *state = (marmot_native_state *)opaque;
	if (state == NULL || root == NULL) return -1;
	state->workers = (pthread_t *)calloc((size_t)state->worker_count, sizeof(pthread_t));
	if (state->workers == NULL) return -1;
	for (int index = 0; index < state->worker_count; index++) {
		if (pthread_create(&state->workers[index], NULL, marmot_native_worker, state) != 0) {
			marmot_native_queue_cancel(&state->queue);
			state->worker_count = index;
			break;
		}
	}
	marmot_native_task root_task;
	memset(&root_task, 0, sizeof(root_task));
	root_task.parent_id = 1;
	root_task.fd = -1;
	root_task.is_root = 1;
	root_task.path = strdup(root);
	if (root_task.path != NULL) root_task.path_length = strlen(root);
	if (root_task.path == NULL || !marmot_native_queue_push(&state->queue, root_task)) {
		marmot_native_task_dispose(&state->queue, &root_task);
		marmot_native_queue_cancel(&state->queue);
	} else {
		pthread_mutex_lock(&state->queue.mutex);
		while (state->queue.pending > 0) pthread_cond_wait(&state->queue.done, &state->queue.mutex);
		pthread_mutex_unlock(&state->queue.mutex);
	}
	for (int index = 0; index < state->worker_count; index++) pthread_join(state->workers[index], NULL);
	return state->queue.cancelled ? -1 : 0;
}
*/
import "C"

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime/cgo"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"example.com/marmot/internal/domain/scan"
)

type nativeDirectoryState struct {
	path     string
	parentID int64
}

// nativeBatchBuffers is one worker's scratch space. ADR-0057 §1 narrows the
// emit contract so a batch is valid only while the callback runs, which is what
// lets these be recycled instead of allocated per batch: the scanner emits
// 421,701 batches and 45.7% of them carry a single node (R-058 §4.1), so the
// fixed per-batch cost, not the per-node cost, dominated the scan's allocation
// total.
type nativeBatchBuffers struct {
	nodes       []Node
	linkCount   []uint32
	parentPaths []string
	deltaIDs    []int64
	deltaValues []scan.DirectorySize
}

func (buffers *nativeBatchBuffers) reset(size int) {
	// Deliberately no floor capacity here: sync.Pool drops its contents at every
	// GC, so a floor would be paid again on each new buffer rather than once.
	// Letting append size them costs one growth curve per buffer.
	buffers.nodes = buffers.nodes[:0]
	buffers.linkCount = buffers.linkCount[:0]
	buffers.deltaIDs = buffers.deltaIDs[:0]
	buffers.deltaValues = buffers.deltaValues[:0]
	if cap(buffers.parentPaths) < size {
		buffers.parentPaths = make([]string, size)
		return
	}
	buffers.parentPaths = buffers.parentPaths[:size]
	// Cleared rather than merely re-sliced so a recycled buffer cannot keep the
	// previous batch's parent paths alive.
	clear(buffers.parentPaths)
}

type nativeScanContext struct {
	ctx      context.Context
	emitter  scan.BatchEmitter
	phase    scan.PhaseEmitter
	root     string
	volumeID string
	buffers  sync.Pool
	mu       sync.Mutex
	// Node IDs come from a single counter on the C side and the root is 1, so
	// they are dense. ADR-0057 §3 replaces the map keyed by node ID with an
	// ordinal indexed by node ID: dirOrdinal[id] is zero when id is not a
	// directory, and otherwise one more than the index into dirStates.
	dirOrdinal []int32
	dirStates  pagedSlice[nativeDirectoryState]
	dirSizes   pagedSlice[scan.DirectorySize]
	seen       map[[2]uint64]struct{}
	result     scan.Result
	err        error
}

func growOrdinals(ordinals []int32, length int64) []int32 {
	if int64(cap(ordinals)) >= length {
		return ordinals[:length]
	}
	capacity := int64(cap(ordinals)) * 2
	if capacity < length {
		capacity = length
	}
	grown := make([]int32, length, capacity)
	copy(grown, ordinals)
	return grown
}

// directoryStateLocked and putDirectoryStateLocked must be called with mu held.
func (native *nativeScanContext) directoryStateLocked(id int64) (nativeDirectoryState, bool) {
	if id < 0 || id >= int64(len(native.dirOrdinal)) {
		return nativeDirectoryState{}, false
	}
	ordinal := native.dirOrdinal[id]
	if ordinal == 0 {
		return nativeDirectoryState{}, false
	}
	return *native.dirStates.at(int(ordinal - 1)), true
}

func (native *nativeScanContext) putDirectoryStateLocked(id int64, state nativeDirectoryState, size scan.DirectorySize) {
	if id >= int64(len(native.dirOrdinal)) {
		native.dirOrdinal = growOrdinals(native.dirOrdinal, id+1)
	}
	native.dirStates.append(state)
	native.dirSizes.append(size)
	native.dirOrdinal[id] = int32(native.dirStates.len())
}

// directoryPageSize is the page size for the two per-directory arrays. Paged for
// the same reason ADR-0057 §3 pages the node table: doubling them cost 162 MB of
// allocation to hold 47 MiB, because doubling's copies total about twice the
// final capacity and the capacity itself overshoots by up to 2x. Pages are never
// copied.
const directoryPageSize = 1 << 14

type pagedSlice[T any] struct {
	pages  [][]T
	length int
}

func (p *pagedSlice[T]) len() int { return p.length }

func (p *pagedSlice[T]) at(index int) *T {
	return &p.pages[index/directoryPageSize][index%directoryPageSize]
}

func (p *pagedSlice[T]) append(value T) {
	if p.length == len(p.pages)*directoryPageSize {
		p.pages = append(p.pages, make([]T, directoryPageSize))
	}
	*p.at(p.length) = value
	p.length++
}

// directorySizeLocked returns a pointer so the roll-up merges in place. nil means
// the ID is not a directory.
func (native *nativeScanContext) directorySizeLocked(id int64) *scan.DirectorySize {
	if id < 0 || id >= int64(len(native.dirOrdinal)) {
		return nil
	}
	ordinal := native.dirOrdinal[id]
	if ordinal == 0 {
		return nil
	}
	return native.dirSizes.at(int(ordinal - 1))
}

// directorySizeMap materialises the boundary type scan.Result still uses. It is
// built once, pre-sized, instead of being grown entry by entry through the scan
// (ADR-0057 §3): growing it cost 160 MB, this costs one pass over the ordinals.
func (native *nativeScanContext) directorySizeMap() map[int64]scan.DirectorySize {
	sizes := make(map[int64]scan.DirectorySize, native.dirSizes.len())
	for id := int64(1); id < int64(len(native.dirOrdinal)); id++ {
		if ordinal := native.dirOrdinal[id]; ordinal != 0 {
			sizes[id] = *native.dirSizes.at(int(ordinal - 1))
		}
	}
	return sizes
}

func scanConfiguredTree(ctx context.Context, root string, emit scan.BatchEmitter, phase scan.PhaseEmitter, resolveMounts MountResolver) (scan.Result, error) {
	if emit == nil {
		emit = func([]Node) error { return nil }
	}
	if phase == nil {
		phase = func(scan.Phase) error { return nil }
	}
	scanCtx, stop := context.WithCancel(ctx)
	defer stop()
	if err := phase(scan.PhaseCatalog); err != nil {
		return scan.Result{}, err
	}
	root = filepath.Clean(root)
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return scan.Result{}, err
	}
	if !rootInfo.IsDir() {
		return scan.Result{}, fmt.Errorf("scan root is not a directory: %s", root)
	}
	mounts, err := resolveMounts()
	if err != nil {
		return scan.Result{}, fmt.Errorf("resolve mount boundaries: %w", err)
	}
	volumeID, profile := mountForRoot(root, mounts)
	boundaries := newMountBoundaries(root, mounts)
	native := &nativeScanContext{
		ctx: scanCtx, emitter: emit, phase: phase, root: root, volumeID: volumeID,
		seen: map[[2]uint64]struct{}{}, result: scan.Result{DirectorySizes: map[int64]DirectorySize{}},
	}
	rootStat, ok := rootInfo.Sys().(*syscall.Stat_t)
	if !ok || rootStat == nil {
		return native.result, errors.New("scan root stat identity unavailable")
	}
	rootNode := nativeRootNode(root, *rootStat, volumeID)
	if err := native.addRoot(rootNode); err != nil {
		return native.result, err
	}
	if err := phase(scan.PhaseVolumeOverview); err != nil {
		return native.result, err
	}

	handle := cgo.NewHandle(native)
	boundaryArray := C.marmot_native_alloc_boundaries(C.size_t(len(boundaries)))
	if len(boundaries) > 0 && boundaryArray == nil {
		handle.Delete()
		return native.result, errors.New("native scanner boundary allocation failed")
	}
	for index, boundary := range boundaries {
		value := C.CString(boundary.path)
		C.marmot_native_set_boundary((*C.marmot_native_boundary)(boundaryArray), C.size_t(index), value)
		C.free(unsafe.Pointer(value))
	}
	defer C.marmot_native_free_boundaries((*C.marmot_native_boundary)(boundaryArray), C.size_t(len(boundaries)))
	state := C.marmot_native_new(C.uintptr_t(handle), (*C.marmot_native_boundary)(boundaryArray), C.size_t(len(boundaries)), C.int(workersForProfile(profile)))
	if state == nil {
		handle.Delete()
		return native.result, errors.New("native scanner state allocation failed")
	}
	done := make(chan struct{})
	watcherDone := make(chan struct{})
	go func() {
		defer close(watcherDone)
		select {
		case <-scanCtx.Done():
			C.marmot_native_cancel(state)
		case <-done:
		}
	}()
	cRoot := C.CString(root)
	runCode := C.marmot_native_run(state, cRoot)
	C.free(unsafe.Pointer(cRoot))
	close(done)
	<-watcherDone
	C.marmot_native_free(state)
	handle.Delete()
	if native.err != nil {
		return native.result, native.err
	}
	if err := scanCtx.Err(); err != nil {
		return native.result, err
	}
	if runCode != 0 {
		return native.result, context.Canceled
	}
	native.finishDirectorySizes()
	native.result.DirectorySizes = native.directorySizeMap()
	if err := phase(scan.PhaseFinalize); err != nil {
		return native.result, err
	}
	return native.result, nil
}

func nativeRootNode(root string, info syscall.Stat_t, volumeID string) Node {
	name := filepath.Base(root)
	return Node{ID: 1, Path: root, Name: name, Kind: "directory", VolumeID: volumeID, Confidence: "exact", SizeBasis: "darwin_native_root_v1", Device: uint64(info.Dev), Inode: uint64(info.Ino), ModifiedAt: time.Unix(info.Mtimespec.Sec, int64(info.Mtimespec.Nsec)), HasChildren: true}
}

// Native directory entries are already validated by getattrlistbulk. Their
// names cannot contain a path separator, and parent paths come from the
// cleaned scan root or this function, so filepath.Join's cleaning is not
// needed on this hot path.
func joinNativePathAndName(parent string, name []byte) (string, string) {
	if parent == "" {
		pathBytes := append([]byte(nil), name...)
		path := unsafe.String(unsafe.SliceData(pathBytes), len(pathBytes))
		return path, path
	}
	if len(name) == 1 && name[0] == '.' {
		return parent, "."
	}
	if len(name) == 2 && name[0] == '.' && name[1] == '.' {
		return filepath.Dir(parent), ".."
	}
	separator := 0
	if parent[len(parent)-1] != '/' {
		separator = 1
	}
	pathBytes := make([]byte, len(parent)+separator+len(name))
	offset := copy(pathBytes, parent)
	if separator != 0 {
		pathBytes[offset] = '/'
		offset++
	}
	copy(pathBytes[offset:], name)
	path := unsafe.String(unsafe.SliceData(pathBytes), len(pathBytes))
	nameStart := len(parent) + separator
	return path, path[nameStart:]
}

func joinNativePath(parent, name string) string {
	path, _ := joinNativePathAndName(parent, []byte(name))
	return path
}

func (native *nativeScanContext) addRoot(root Node) error {
	native.mu.Lock()
	native.putDirectoryStateLocked(root.ID, nativeDirectoryState{path: root.Path}, DirectorySize{Confidence: root.Confidence, SizeBasis: root.SizeBasis})
	native.result.Nodes = 1
	native.result.Directories = 1
	native.mu.Unlock()
	return native.emit([]Node{root})
}

func (native *nativeScanContext) addNodes(raw []C.marmot_native_entry, includesRoot bool) error {
	buffers, _ := native.buffers.Get().(*nativeBatchBuffers)
	if buffers == nil {
		buffers = &nativeBatchBuffers{}
	}
	buffers.reset(len(raw))
	// Returned only after emit, because ADR-0057 §1 makes the batch valid for
	// exactly the duration of the callback.
	defer native.buffers.Put(buffers)

	native.mu.Lock()
	for index := range raw {
		entry := &raw[index]
		if includesRoot && entry.node_id == 1 {
			continue
		}
		parentState, ok := native.directoryStateLocked(int64(entry.parent_id))
		if !ok {
			err := fmt.Errorf("native scanner parent path missing: %d", entry.parent_id)
			native.err = err
			native.mu.Unlock()
			return err
		}
		buffers.parentPaths[index] = parentState.path
	}
	native.mu.Unlock()

	prepared := buffers
	for index := range raw {
		entry := &raw[index]
		if includesRoot && entry.node_id == 1 {
			continue
		}
		parentPath := buffers.parentPaths[index]
		nameBytes := unsafe.Slice((*byte)(unsafe.Pointer(&entry.name[0])), int(entry.name_length))
		kind := "file"
		if entry.objtype == 2 {
			kind = "directory"
		} else if entry.objtype == 5 {
			kind = "symlink"
		}
		var path, name string
		if kind == "directory" {
			// A directory keeps its path: the walk opens it, and its children
			// build their own names against it.
			path, name = joinNativePathAndName(parentPath, nameBytes)
		} else {
			// ADR-0057 §2: a file node carries no path. The store rebuilds paths
			// from the parent chain, so the 263.4 MiB of file paths this used to
			// build were discarded immediately (R-058 §4.2). It also makes DDD
			// invariant 17 structural — a node without a path cannot be used to
			// authorise a file operation.
			name = string(nameBytes)
		}
		logicalSize := int64(entry.data_length)
		allocatedSize := int64(entry.alloc_size)
		if logicalSize < 0 {
			logicalSize = 0
		}
		if allocatedSize < 0 {
			allocatedSize = 0
		}
		confidence := "exact"
		if uint32(entry.common_attrs)&(bulkCommonDevid|bulkCommonFileID|bulkCommonModtime) != (bulkCommonDevid | bulkCommonFileID | bulkCommonModtime) {
			confidence = "partial"
		}
		if kind != "directory" && uint32(entry.file_attrs)&(bulkFileAllocSize|bulkFileDataLength) != (bulkFileAllocSize|bulkFileDataLength) {
			confidence = "partial"
		}
		node := Node{ID: int64(entry.node_id), ParentID: int64(entry.parent_id), Path: path, Name: name, Kind: kind, LogicalSize: logicalSize, AllocatedSize: allocatedSize, OwnedAllocated: allocatedSize, VolumeID: native.volumeID, Confidence: confidence, SizeBasis: "darwin_getattrlistbulk_native_v1", Device: uint64(entry.device), Inode: uint64(entry.file_id), ModifiedAt: time.Unix(int64(entry.mod_seconds), int64(entry.mod_nanoseconds)), HasChildren: kind == "directory"}
		if kind == "directory" {
			node.LogicalSize = 0
			node.AllocatedSize = 0
			node.OwnedAllocated = 0
		}
		prepared.nodes = append(prepared.nodes, node)
		prepared.linkCount = append(prepared.linkCount, uint32(entry.link_count))
	}
	// Deltas are appended rather than accumulated in a map keyed by parent
	// (ADR-0057 §3). mergeDirectorySize is additive and mergeConfidence is a
	// monotone lattice join, so merging N un-deduplicated entries is exactly
	// equivalent to merging one accumulated entry — and it drops a map that was
	// built 421,701 times to hold a handful of keys each (R-058 §4.3).
	for index := range prepared.nodes {
		node := prepared.nodes[index]
		if node.Kind == "directory" {
			continue
		}
		var delta scan.DirectorySize
		addDirectorySize(&delta, node)
		prepared.deltaIDs = append(prepared.deltaIDs, node.ParentID)
		prepared.deltaValues = append(prepared.deltaValues, delta)
	}

	native.mu.Lock()
	var batchBytes int64
	var batchFiles int64
	var batchDirectories int64
	for index := range prepared.nodes {
		node := &prepared.nodes[index]
		if node.Kind == "directory" {
			native.putDirectoryStateLocked(node.ID, nativeDirectoryState{path: node.Path, parentID: node.ParentID}, DirectorySize{Confidence: node.Confidence, SizeBasis: node.SizeBasis})
			batchDirectories++
		} else {
			if node.Kind == "file" || node.Kind == "symlink" {
				batchFiles++
			}
			if prepared.linkCount[index] > 1 && node.Kind == "file" {
				key := [2]uint64{node.Device, node.Inode}
				if _, exists := native.seen[key]; exists {
					node.OwnedAllocated = 0
					// A correcting entry rather than a read-modify-write on an
					// accumulated one: the merge below is additive.
					prepared.deltaIDs = append(prepared.deltaIDs, node.ParentID)
					prepared.deltaValues = append(prepared.deltaValues, scan.DirectorySize{OwnedAllocated: -node.AllocatedSize})
				} else {
					native.seen[key] = struct{}{}
				}
			}
			batchBytes += node.OwnedAllocated
		}
	}
	for index, parentID := range prepared.deltaIDs {
		if directorySize := native.directorySizeLocked(parentID); directorySize != nil {
			mergeDirectorySize(directorySize, prepared.deltaValues[index])
		}
	}
	native.result.Nodes += int64(len(prepared.nodes))
	native.result.Files += batchFiles
	native.result.Directories += batchDirectories
	native.result.Bytes += batchBytes
	native.mu.Unlock()
	return native.emit(prepared.nodes)
}

func (native *nativeScanContext) emit(nodes []Node) error {
	if err := native.ctx.Err(); err != nil {
		return err
	}
	if err := native.emitter(nodes); err != nil {
		native.mu.Lock()
		native.err = err
		native.mu.Unlock()
		return err
	}
	return nil
}

// finishDirectorySizes rolls every directory's size into its parent. A child is
// always numbered after its parent, so walking node IDs downwards visits every
// child before its parent — which is what the previous sort by descending ID
// established, without the sort or the ID slice it needed (ADR-0057 §3).
func (native *nativeScanContext) finishDirectorySizes() {
	native.mu.Lock()
	defer native.mu.Unlock()
	for id := int64(len(native.dirOrdinal)) - 1; id >= 1; id-- {
		ordinal := native.dirOrdinal[id]
		if ordinal == 0 {
			continue
		}
		parentID := native.dirStates.at(int(ordinal - 1)).parentID
		if parentID == 0 {
			continue
		}
		total := native.directorySizeLocked(parentID)
		if total == nil {
			continue
		}
		mergeDirectorySize(total, *native.dirSizes.at(int(ordinal - 1)))
	}
}

//export marmotNativeBatchCallback
func marmotNativeBatchCallback(handle C.uintptr_t, entries *C.marmot_native_entry, count C.size_t) C.int {
	native := cgo.Handle(handle).Value().(*nativeScanContext)
	if entries == nil || count == 0 {
		return 0
	}
	raw := unsafe.Slice(entries, int(count))
	if err := native.addNodes(raw, false); err != nil {
		return 1
	}
	return 0
}

//export marmotNativeIssueCallback
func marmotNativeIssueCallback(handle C.uintptr_t, path, message *C.char) C.int {
	native := cgo.Handle(handle).Value().(*nativeScanContext)
	issue := Issue{Path: C.GoString(path), Message: C.GoString(message)}
	native.mu.Lock()
	native.result.Issues = append(native.result.Issues, issue)
	native.mu.Unlock()
	return 0
}

//export marmotNativeRootDoneCallback
func marmotNativeRootDoneCallback(handle C.uintptr_t) C.int {
	native := cgo.Handle(handle).Value().(*nativeScanContext)
	if err := native.ctx.Err(); err != nil {
		native.mu.Lock()
		native.err = err
		native.mu.Unlock()
		return 1
	}
	if err := native.phase(scan.PhaseTopLevelPublish); err != nil {
		native.mu.Lock()
		native.err = err
		native.mu.Unlock()
		return 1
	}
	if err := native.phase(scan.PhaseDeepScan); err != nil {
		native.mu.Lock()
		native.err = err
		native.mu.Unlock()
		return 1
	}
	return 0
}
