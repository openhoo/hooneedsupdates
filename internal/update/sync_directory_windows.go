package update

// Windows does not support fsync on directory handles opened by os.Open.
// atomicWrite still flushes file contents before replacement. Directory metadata
// persistence across power loss is not guaranteed on this platform.
func syncDirectory(string) error { return nil }
