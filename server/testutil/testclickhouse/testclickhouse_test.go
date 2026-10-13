package testclickhouse

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/stretchr/testify/require"
)

var shared SharedServer

func TestMain(m *testing.M) {
	os.Exit(shared.Run(m))
}

func TestSharedServerDatabaseIsolation(t *testing.T) {
	var previousTarget string
	for _, name := range []string{"first", "second"} {
		t.Run(name, func(t *testing.T) {
			target := shared.getOrCreateDatabase(t)
			require.Equal(t, target, shared.getOrCreateDatabase(t))
			options, err := clickhouse.ParseDSN(target)
			require.NoError(t, err)
			conn, err := clickhouse.Open(options)
			require.NoError(t, err)
			defer conn.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if previousTarget != "" {
				previous, err := clickhouse.ParseDSN(previousTarget)
				require.NoError(t, err)
				require.Equal(t, previous.Addr, options.Addr, "reuse the process")
				require.NotEqual(t, previous.Auth.Database, options.Auth.Database)
				var count uint64
				require.NoError(t, conn.QueryRow(ctx, "SELECT count() FROM system.databases WHERE name = ?", previous.Auth.Database).Scan(&count))
				require.Zero(t, count, "the preceding test's database was dropped")
			}
			var exists uint8
			require.NoError(t, conn.QueryRow(ctx, "EXISTS TABLE isolation_marker").Scan(&exists))
			require.Zero(t, exists, "each test starts with a fresh database")
			require.NoError(t, conn.Exec(ctx, "CREATE TABLE isolation_marker (value UInt8) ENGINE = Memory"))
			previousTarget = target
		})
	}
}

func TestStartRemainsIsolated(t *testing.T) {
	target := GetOrStart(t)
	require.Equal(t, target, GetOrStart(t))
	require.True(t, strings.HasSuffix(target, "/buildbuddy_test"))
	sharedTarget := shared.getOrCreateDatabase(t)
	legacy, err := clickhouse.ParseDSN(target)
	require.NoError(t, err)
	optedIn, err := clickhouse.ParseDSN(sharedTarget)
	require.NoError(t, err)
	require.NotEqual(t, legacy.Addr, optedIn.Addr)
}
