package sky_wrappers

import (
	"crypto/sha256"
	"hash"
)

func Sky_crypto_sha256_New() hash.Hash {
	return sha256.New()
}

func Sky_crypto_sha256_New224() hash.Hash {
	return sha256.New224()
}

func Sky_crypto_sha256_Sum224(arg0 any) [28]byte {
	_arg0 := arg0.([]byte)
	return sha256.Sum224(_arg0)
}

func Sky_crypto_sha256_Sum256(arg0 any) [32]byte {
	_arg0 := arg0.([]byte)
	return sha256.Sum256(_arg0)
}

func Sky_crypto_sha256_BlockSize() any {
	return sha256.BlockSize
}

func Sky_crypto_sha256_Size() any {
	return sha256.Size
}

func Sky_crypto_sha256_Size224() any {
	return sha256.Size224
}

