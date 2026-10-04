package modules

import (
	"os"
	"os/exec"
	"testing"
)

// Redis-tagged integration fixtures require a real process. The project helper
// supplies a pinned build without installing anything into the host system.
func testRedisBinary(t *testing.T) string {
	t.Helper()
	binary := os.Getenv("QGRAMM_REDIS_TEST_BINARY")
	if binary == "" {
		binary = "redis-server"
	}
	path, err := exec.LookPath(binary)
	if err != nil {
		t.Fatal("real Redis fixture required: run sh scripts/test-redis.sh <test command>; or set QGRAMM_REDIS_TEST_BINARY to an executable Redis binary")
	}
	return path
}
