package content_addressable_storage_server_proxy

import (
	"flag"
	"time"
)

func init() {
	flag.Duration("cache_proxy.find_missing_blobs_refresh_interval", 0, "Test compatibility only")
	flag.Duration("cache_proxy.find_missing_blobs_tombstone_ttl", 3*time.Minute, "Test compatibility only")
}
