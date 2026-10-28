/* guard.c - the memory guard of the Sky native trainer. See guard.h. */
#define _GNU_SOURCE
#include "guard.h"

#include <errno.h>
#include <fcntl.h>
#include <signal.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>
#include <unistd.h>

#ifdef __APPLE__
#include <libproc.h>
#include <sys/resource.h>
#endif

#ifdef __APPLE__
static int usage(pid_t pid, struct rusage_info_v4* ri) {
  return proc_pid_rusage(pid, RUSAGE_INFO_V4, (rusage_info_t*)ri);
}
uint64_t sky_mem_footprint(pid_t pid) {
  struct rusage_info_v4 ri;
  return usage(pid, &ri) == 0 ? ri.ri_phys_footprint : 0;
}
uint64_t sky_mem_peak(pid_t pid) {
  struct rusage_info_v4 ri;
  return usage(pid, &ri) == 0 ? ri.ri_lifetime_max_phys_footprint : 0;
}
#else
static uint64_t status_kb(pid_t pid, const char* key) {
  char path[64];
  snprintf(path, sizeof path, "/proc/%d/status", (int)pid);
  FILE* f = fopen(path, "r");
  if (!f) return 0;
  char line[256];
  uint64_t kb = 0;
  size_t n = strlen(key);
  while (fgets(line, sizeof line, f)) {
    if (strncmp(line, key, n) == 0) {
      kb = strtoull(line + n, NULL, 10);
      break;
    }
  }
  fclose(f);
  return kb * 1024;
}
uint64_t sky_mem_footprint(pid_t pid) { return status_kb(pid, "VmRSS:"); }
uint64_t sky_mem_peak(pid_t pid) { return status_kb(pid, "VmHWM:"); }
#endif

static void write_all(int fd, const char* s) {
  size_t n = strlen(s);
  while (n > 0) {
    ssize_t w = write(fd, s, n);
    if (w < 0) {
      if (errno == EINTR) continue;
      return;
    }
    s += w;
    n -= (size_t)w;
  }
}

static void nap_ms(long ms) {
  struct timespec t = {ms / 1000, (ms % 1000) * 1000000L};
  while (nanosleep(&t, &t) != 0 && errno == EINTR) {
  }
}

static void guard_loop(pid_t trainer, const char* run_dir, uint64_t cap) {
  uint64_t peak = 0;
  for (;;) {
    if (getppid() != trainer) _exit(0);
    uint64_t now = sky_mem_footprint(trainer);
    if (now > peak) peak = now;
    if (now > cap) {
      /* guard.json first, then the kill, then the error line: the runtime
         treats a trainer gone with a guard.json as killed over its cap
         (never relaunched), so the file must exist before the trainer dies. */
      char json[512];
      snprintf(json, sizeof json, "{\"capBytes\":%llu,\"code\":\"ModelTooLarge\",\"peakBytes\":%llu}\n",
               (unsigned long long)cap, (unsigned long long)peak);
      char tmp[4096], fin[4096];
      snprintf(tmp, sizeof tmp, "%s/guard.json.tmp", run_dir);
      snprintf(fin, sizeof fin, "%s/guard.json", run_dir);
      int fd = open(tmp, O_WRONLY | O_CREAT | O_TRUNC, 0644);
      if (fd >= 0) {
        write_all(fd, json);
        fsync(fd);
        close(fd);
        rename(tmp, fin);
      }
      kill(trainer, SIGKILL);
      for (int i = 0; i < 100 && getppid() == trainer; i++) nap_ms(10);
      char line[1024];
      snprintf(line, sizeof line,
               "{\"capBytes\":%llu,\"code\":\"ModelTooLarge\",\"message\":\"the trainer used %llu bytes, "
               "over its memory cap of %llu bytes; give the job a smaller sequence length, batch or rank, "
               "a quantised base, or a larger cap\",\"peakBytes\":%llu,\"type\":\"error\",\"v\":1}\n",
               (unsigned long long)cap, (unsigned long long)peak, (unsigned long long)cap,
               (unsigned long long)peak);
      write_all(1, line);
      _exit(0);
    }
    nap_ms(200);
  }
}

pid_t sky_guard_start(const char* run_dir, uint64_t cap_bytes) {
  if (cap_bytes == 0) return 0;
  fflush(NULL);
  pid_t trainer = getpid();
  pid_t g = fork();
  if (g < 0) return -1;
  if (g == 0) {
    signal(SIGTERM, SIG_DFL);
    signal(SIGINT, SIG_IGN);
    guard_loop(trainer, run_dir, cap_bytes);
    _exit(0);
  }
  return g;
}

void sky_guard_stop(pid_t guard) {
  if (guard > 0) kill(guard, SIGTERM);
}
