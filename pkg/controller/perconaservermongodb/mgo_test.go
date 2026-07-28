package perconaservermongodb

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	api "github.com/percona/percona-server-mongodb-operator/pkg/apis/psmdb/v1"
	"github.com/percona/percona-server-mongodb-operator/pkg/psmdb/mongo"
)

func TestCompareTags(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		mongoTags    mongo.ReplsetTags
		selectorTags api.PrimaryPreferTagSelectorSpec
		expected     bool
	}{
		{
			name:         "empty tags",
			mongoTags:    mongo.ReplsetTags{},
			selectorTags: api.PrimaryPreferTagSelectorSpec{},
			expected:     false,
		},
		{
			name:         "selector with podName",
			mongoTags:    mongo.ReplsetTags{},
			selectorTags: api.PrimaryPreferTagSelectorSpec{"podName": "test"},
			expected:     false,
		},
		{
			name:         "match selector with podName",
			mongoTags:    mongo.ReplsetTags{"podName": "test"},
			selectorTags: api.PrimaryPreferTagSelectorSpec{"podName": "test"},
			expected:     true,
		},
		{
			name:         "match selector with podName and other tags",
			mongoTags:    mongo.ReplsetTags{"podName": "test", "other": "tag"},
			selectorTags: api.PrimaryPreferTagSelectorSpec{"podName": "test"},
			expected:     true,
		},
		{
			name:         "match two selectors with podName and other tags",
			mongoTags:    mongo.ReplsetTags{"podName": "test", "other": "tag"},
			selectorTags: api.PrimaryPreferTagSelectorSpec{"podName": "test", "other": "tag"},
			expected:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := compareTags(tt.mongoTags, tt.selectorTags); got != tt.expected {
				t.Errorf("compareTags() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestGetRoles(t *testing.T) {
	tests := map[string]struct {
		crVersion string
		role      api.SystemUserRole
		expected  []mongo.Role
	}{
		"RoleDatabaseAdmin": {
			role: api.RoleDatabaseAdmin,
			expected: []mongo.Role{
				{DB: "admin", Role: "readWriteAnyDatabase"},
				{DB: "admin", Role: "readAnyDatabase"},
				{DB: "admin", Role: "restore"},
				{DB: "admin", Role: "backup"},
				{DB: "admin", Role: "dbAdminAnyDatabase"},
				{DB: "admin", Role: string(api.RoleClusterMonitor)},
			},
		},
		"RoleClusterMonitor with version >= 1.20.0": {
			crVersion: "1.20.0",
			role:      api.RoleClusterMonitor,
			expected: []mongo.Role{
				{DB: "admin", Role: "explainRole"},
				{DB: "local", Role: "read"},
				{DB: "admin", Role: "directShardOperations"},
				{DB: "admin", Role: string(api.RoleClusterMonitor)},
			},
		},
		"RoleClusterMonitor with version < 1.20.0": {
			crVersion: "1.19.0",
			role:      api.RoleClusterMonitor,
			expected: []mongo.Role{
				{DB: "admin", Role: "explainRole"},
				{DB: "local", Role: "read"},
				{DB: "admin", Role: string(api.RoleClusterMonitor)},
			},
		},
		"RoleBackup": {
			role: api.RoleBackup,
			expected: []mongo.Role{
				{DB: "admin", Role: "readWrite"},
				{DB: "admin", Role: string(api.RoleClusterMonitor)},
				{DB: "admin", Role: "restore"},
				{DB: "admin", Role: "pbmAnyAction"},
				{DB: "admin", Role: string(api.RoleBackup)},
			},
		},
		"RoleClusterAdmin": {
			crVersion: "1.19.0",
			role:      api.RoleClusterAdmin,
			expected: []mongo.Role{
				{DB: "admin", Role: string(api.RoleClusterAdmin)},
			},
		},
		"RoleClusterAdmin with version >= 1.20.0": {
			crVersion: "1.20.0",
			role:      api.RoleClusterAdmin,
			expected: []mongo.Role{
				{DB: "admin", Role: "directShardOperations"},
				{DB: "admin", Role: string(api.RoleClusterAdmin)},
			},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			cr := &api.PerconaServerMongoDB{Spec: api.PerconaServerMongoDBSpec{CRVersion: tt.crVersion}}
			actual := getRoles(cr, tt.role)
			assert.Equal(t, tt.expected, actual)
		})
	}
}

func TestCompareRoles(t *testing.T) {
	tests := map[string]struct {
		x        []mongo.Role
		y        []mongo.Role
		expected bool
	}{
		"length is different": {
			x: []mongo.Role{
				{DB: "admin", Role: string(api.RoleClusterAdmin)},
			},
			y: []mongo.Role{
				{DB: "admin", Role: "directShardOperations"},
				{DB: "admin", Role: string(api.RoleClusterAdmin)},
			},
			expected: false,
		},
		"order is different": {
			x: []mongo.Role{
				{DB: "admin", Role: string(api.RoleClusterAdmin)},
				{DB: "admin", Role: "directShardOperations"},
			},
			y: []mongo.Role{
				{DB: "admin", Role: "directShardOperations"},
				{DB: "admin", Role: string(api.RoleClusterAdmin)},
			},
			expected: true,
		},
		"one role is different": {
			x: []mongo.Role{
				{DB: "admin", Role: "readWriteAnyDatabase"},
				{DB: "admin", Role: "readAnyDatabase"},
				{DB: "admin", Role: "restore"},
				{DB: "admin", Role: "backup"},
				{DB: "admin", Role: "dbAdminAnyDatabase"},
				{DB: "admin", Role: string(api.RoleClusterMonitor)},
			},
			y: []mongo.Role{
				{DB: "admin", Role: "readWriteAnyDatabase"},
				{DB: "admin", Role: "readAnyDatabase"},
				{DB: "admin", Role: "restore"},
				{DB: "admin", Role: "backup"},
				{DB: "admin", Role: "dbAdminAnyDatabase2"},
				{DB: "admin", Role: string(api.RoleClusterMonitor)},
			},
			expected: false,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			actual := compareRoles(tt.x, tt.y)
			assert.Equal(t, tt.expected, actual)
		})
	}
}

func TestIsShardDDLLockError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name:     "nil error",
			err:      nil,
			expected: false,
		},
		{
			name:     "unrelated error",
			err:      errors.New("connection refused"),
			expected: false,
		},
		{
			name:     "LockBusy",
			err:      errors.New("(LockBusy) Unable to acquire DDL lock for namespace"),
			expected: true,
		},
		{
			name:     "ConflictingOperationInProgress",
			err:      errors.New("(ConflictingOperationInProgress) another operation is in progress"),
			expected: true,
		},
		{
			name:     "wrapped LockBusy",
			err:      errors.Wrap(errors.New("(LockBusy) lock held"), "add shard"),
			expected: true,
		},
		{
			name:     "CommandNotFound is not retriable",
			err:      errors.New("(CommandNotFound) no such command"),
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, isShardDDLLockError(tt.err))
		})
	}
}

// TestWithShardAddSlot checks that the shard-add gate admits at most
// cap(shardAddSem) callers at a time and honors context cancellation.
func TestWithShardAddSlot(t *testing.T) {
	t.Parallel()

	t.Run("nil semaphore does not gate", func(t *testing.T) {
		r := &ReconcilePerconaServerMongoDB{}

		called := false
		require.NoError(t, r.withShardAddSlot(context.Background(), func() error {
			called = true
			return nil
		}))
		assert.True(t, called)
	})

	t.Run("bounds concurrency", func(t *testing.T) {
		const limit = 2
		r := &ReconcilePerconaServerMongoDB{shardAddSem: make(chan struct{}, limit)}

		var (
			mu      sync.Mutex
			inside  int
			maxSeen int
		)

		var wg sync.WaitGroup
		for range 20 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_ = r.withShardAddSlot(context.Background(), func() error {
					mu.Lock()
					inside++
					if inside > maxSeen {
						maxSeen = inside
					}
					mu.Unlock()

					time.Sleep(time.Millisecond)

					mu.Lock()
					inside--
					mu.Unlock()

					return nil
				})
			}()
		}
		wg.Wait()

		assert.LessOrEqual(t, maxSeen, limit)
	})

	t.Run("honors cancellation", func(t *testing.T) {
		r := &ReconcilePerconaServerMongoDB{shardAddSem: make(chan struct{}, 1)}
		// Occupy the only slot so the next acquisition has to wait.
		r.shardAddSem <- struct{}{}

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		called := false
		err := r.withShardAddSlot(ctx, func() error {
			called = true
			return nil
		})
		assert.ErrorIs(t, err, context.Canceled)
		assert.False(t, called)
	})
}
