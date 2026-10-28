/* sha256.h - SHA-256 (FIPS 180-4) for the Sky native trainer.
 *
 * Written for Sky and dedicated to the public domain: anyone may use, copy,
 * modify and distribute this file for any purpose, with or without fee.
 */
#ifndef SKY_SHA256_H
#define SKY_SHA256_H

#include <stddef.h>
#include <stdint.h>

typedef struct {
  uint32_t h[8];
  uint64_t len;
  uint8_t buf[64];
  size_t used;
} sky_sha256;

void sky_sha256_init(sky_sha256* s);
void sky_sha256_update(sky_sha256* s, const void* data, size_t n);
void sky_sha256_final(sky_sha256* s, uint8_t out[32]);

/* The lowercase hex digest of a file; 0 on success, -1 when it cannot be read. */
int sky_sha256_file_hex(const char* path, char out[65]);

#endif
