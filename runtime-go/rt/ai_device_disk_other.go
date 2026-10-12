//go:build !unix && !js

package rt

// aiDiskAvailable reports no free space where the volume cannot be read: a
// device decision then keeps a slot on the server.
func aiDiskAvailable(dir string) int64 { return 0 }
