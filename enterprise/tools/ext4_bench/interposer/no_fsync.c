// Benchmark-only interposer. Never load into the executor: suppressing fsync
// weakens durability and changes crash behavior.
int fsync(int fd) { (void)fd; return 0; }
int fdatasync(int fd) { (void)fd; return 0; }
