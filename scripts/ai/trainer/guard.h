/* guard.h - the memory guard of the Sky native trainer.
 *
 * sky_guard_start forks a small child BEFORE any MLX or Metal state exists.
 * The child polls the trainer's memory every 200 ms: phys_footprint on macOS
 * (which counts Metal buffers, where RSS does not), VmRSS on Linux. Above the
 * cap it SIGKILLs the trainer, then writes <run>/guard.json and one protocol
 * error line (code ModelTooLarge, with peakBytes and capBytes) to the
 * trainer's standard output. The Go side treats guard.json as terminal and
 * does not relaunch. The child exits as soon as the trainer is gone.
 */
#ifndef SKY_GUARD_H
#define SKY_GUARD_H

#include <stdint.h>
#include <sys/types.h>

/* The memory a process uses now and the most it has used, in bytes; 0 when
 * the platform cannot say. */
uint64_t sky_mem_footprint(pid_t pid);
uint64_t sky_mem_peak(pid_t pid);

/* Starts the guard for the calling process. Returns the guard's pid, 0 when
 * cap is 0 (no guard), or -1 when it cannot fork. */
pid_t sky_guard_start(const char* run_dir, uint64_t cap_bytes);

/* Stops the guard at a normal exit. */
void sky_guard_stop(pid_t guard);

#endif
