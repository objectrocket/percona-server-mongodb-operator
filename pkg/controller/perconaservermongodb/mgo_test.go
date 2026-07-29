package perconaservermongodb

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

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

// TestWithShardAddSlot checks that the shard-add gate admits at most the
// configured capacity per cluster, scopes independently per CR, and honors
// context cancellation.
func TestWithShardAddSlot(t *testing.T) {
	t.Parallel()

	testCR := &api.PerconaServerMongoDB{
		ObjectMeta: metav1.ObjectMeta{Name: "test-cluster", Namespace: "ns1"},
	}

	t.Run("nil semaphore map does not gate", func(t *testing.T) {
		r := &ReconcilePerconaServerMongoDB{}

		called := false
		require.NoError(t, r.withShardAddSlot(context.Background(), testCR, func() error {
			called = true
			return nil
		}))
		assert.True(t, called)
	})

	t.Run("bounds concurrency per cluster", func(t *testing.T) {
		const limit = 2
		r := &ReconcilePerconaServerMongoDB{
			shardAddSems: new(sync.Map),
			shardAddCap:  limit,
		}

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
				_ = r.withShardAddSlot(context.Background(), testCR, func() error {
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
		r := &ReconcilePerconaServerMongoDB{
			shardAddSems: new(sync.Map),
			shardAddCap:  1,
		}
		// Pre-populate the slot for this cluster so the next acquisition has to wait.
		sem := make(chan struct{}, 1)
		sem <- struct{}{}
		r.shardAddSems.Store(testCR.Namespace+"/"+testCR.Name, sem)

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		called := false
		err := r.withShardAddSlot(ctx, testCR, func() error {
			called = true
			return nil
		})
		assert.ErrorIs(t, err, context.Canceled)
		assert.False(t, called)
	})

	t.Run("independent clusters do not block each other", func(t *testing.T) {
		r := &ReconcilePerconaServerMongoDB{
			shardAddSems: new(sync.Map),
			shardAddCap:  1,
		}
		crA := &api.PerconaServerMongoDB{
			ObjectMeta: metav1.ObjectMeta{Name: "cluster-a", Namespace: "ns1"},
		}
		crB := &api.PerconaServerMongoDB{
			ObjectMeta: metav1.ObjectMeta{Name: "cluster-b", Namespace: "ns1"},
		}

		// Hold the slot for cluster A.
		holding := make(chan struct{})
		release := make(chan struct{})
		go func() {
			_ = r.withShardAddSlot(context.Background(), crA, func() error {
				close(holding)
				<-release
				return nil
			})
		}()
		<-holding // Cluster A's slot is now occupied.

		// Cluster B should NOT be blocked.
		bDone := make(chan struct{})
		go func() {
			_ = r.withShardAddSlot(context.Background(), crB, func() error {
				close(bDone)
				return nil
			})
		}()

		select {
		case <-bDone:
			// success: cluster B was not blocked
		case <-time.After(time.Second):
			t.Fatal("cluster B was blocked by cluster A's semaphore")
		}

		close(release) // Let cluster A finish.
	})

	t.Run("same cluster serializes", func(t *testing.T) {
		r := &ReconcilePerconaServerMongoDB{
			shardAddSems: new(sync.Map),
			shardAddCap:  1,
		}
		cr := &api.PerconaServerMongoDB{
			ObjectMeta: metav1.ObjectMeta{Name: "cluster-same", Namespace: "ns1"},
		}

		// Hold the slot for this cluster.
		holding := make(chan struct{})
		release := make(chan struct{})
		go func() {
			_ = r.withShardAddSlot(context.Background(), cr, func() error {
				close(holding)
				<-release
				return nil
			})
		}()
		<-holding

		// Second call for the same cluster should block.
		secondStarted := make(chan struct{})
		go func() {
			_ = r.withShardAddSlot(context.Background(), cr, func() error {
				close(secondStarted)
				return nil
			})
		}()

		select {
		case <-secondStarted:
			t.Fatal("second addShard for same cluster was not serialized")
		case <-time.After(50 * time.Millisecond):
			// expected: it's blocked
		}

		close(release) // Let the first one finish.

		select {
		case <-secondStarted:
			// success: second call proceeded after first released
		case <-time.After(time.Second):
			t.Fatal("second addShard for same cluster never completed")
		}
	})
}
