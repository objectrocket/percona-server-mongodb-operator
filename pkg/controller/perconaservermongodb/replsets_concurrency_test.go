package perconaservermongodb

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	api "github.com/percona/percona-server-mongodb-operator/pkg/apis/psmdb/v1"
	"github.com/percona/percona-server-mongodb-operator/pkg/version"
)

func initCondition(rsName string) api.ClusterCondition {
	return api.ClusterCondition{
		Status:             api.ConditionTrue,
		Type:               api.AppStateInit,
		Message:            rsName,
		LastTransitionTime: metav1.NewTime(metav1.Now().Time),
	}
}

func TestMergeReplsetResults(t *testing.T) {
	t.Parallel()

	t.Run("cluster state is the most severe replset state", func(t *testing.T) {
		tests := []struct {
			name     string
			states   []api.AppState
			expected api.AppState
		}{
			{
				name:     "no replsets",
				states:   nil,
				expected: api.AppStateNone,
			},
			{
				name:     "all ready",
				states:   []api.AppState{api.AppStateReady, api.AppStateReady},
				expected: api.AppStateReady,
			},
			{
				name:     "init wins over ready",
				states:   []api.AppState{api.AppStateReady, api.AppStateInit, api.AppStateReady},
				expected: api.AppStateInit,
			},
			{
				name:     "error wins over everything",
				states:   []api.AppState{api.AppStateReady, api.AppStateInit, api.AppStateError, api.AppStatePaused},
				expected: api.AppStateError,
			},
			{
				name:     "paused wins over init",
				states:   []api.AppState{api.AppStateInit, api.AppStatePaused},
				expected: api.AppStatePaused,
			},
			{
				name:     "stopping wins over paused",
				states:   []api.AppState{api.AppStatePaused, api.AppStateStopping},
				expected: api.AppStateStopping,
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				// The reduce must not depend on the order the states arrive in,
				// which is exactly what changes when replsets are reconciled
				// concurrently. Assert on every rotation of the input.
				for shift := range len(tt.states) + 1 {
					states := rotate(tt.states, shift)

					cr := newStatusCR(namesFor(states)...)
					results := make([]replsetClusterResult, 0, len(states))
					for i, s := range states {
						results = append(results, replsetClusterResult{
							name:   fmt.Sprintf("rs%d", i),
							result: clusterReconcileResult{state: s},
						})
					}

					got, err := mergeReplsetResults(logr.Discard(), cr, results)
					require.NoError(t, err)
					assert.Equal(t, tt.expected, got, "shift %d", shift)
				}
			})
		}
	})

	t.Run("applies status fields per replset", func(t *testing.T) {
		cr := newStatusCR("rs0", "rs1", "rs2")
		cr.Status.Replsets["rs2"] = api.ReplsetStatus{
			Initialized: true,
			Members:     map[string]api.ReplsetMemberStatus{"stale": {Name: "stale"}},
		}

		results := []replsetClusterResult{
			{
				name: "rs0",
				result: clusterReconcileResult{
					state:          api.AppStateInit,
					setInitialized: true,
					members:        map[string]api.ReplsetMemberStatus{"rs0-0": {Name: "rs0-0"}},
				},
			},
			{
				name: "rs1",
				result: clusterReconcileResult{
					state:        api.AppStateReady,
					addedAsShard: ptr.To(true),
					members: map[string]api.ReplsetMemberStatus{
						"rs1-0": {Name: "rs1-0"},
						"rs1-1": {Name: "rs1-1"},
					},
				},
			},
			{
				name:   "rs2",
				result: clusterReconcileResult{state: api.AppStateReady},
			},
		}

		state, err := mergeReplsetResults(logr.Discard(), cr, results)
		require.NoError(t, err)
		assert.Equal(t, api.AppStateInit, state)

		assert.True(t, cr.Status.Replsets["rs0"].Initialized)
		assert.Nil(t, cr.Status.Replsets["rs0"].AddedAsShard)
		assert.Equal(t, []string{"rs0-0"}, memberNames(cr.Status.Replsets["rs0"]))

		assert.False(t, cr.Status.Replsets["rs1"].Initialized)
		require.NotNil(t, cr.Status.Replsets["rs1"].AddedAsShard)
		assert.True(t, *cr.Status.Replsets["rs1"].AddedAsShard)
		assert.Equal(t, []string{"rs1-0", "rs1-1"}, memberNames(cr.Status.Replsets["rs1"]))

		// setInitialized is only ever set to true, never cleared, and a result
		// with no members replaces the existing ones with an empty map.
		assert.True(t, cr.Status.Replsets["rs2"].Initialized)
		assert.Empty(t, cr.Status.Replsets["rs2"].Members)
	})

	t.Run("skips replsets with no status entry", func(t *testing.T) {
		cr := newStatusCR("rs0")

		results := []replsetClusterResult{
			{name: "rs0", result: clusterReconcileResult{state: api.AppStateReady}},
			{name: "gone", result: clusterReconcileResult{state: api.AppStateReady, setInitialized: true}},
		}

		_, err := mergeReplsetResults(logr.Discard(), cr, results)
		require.NoError(t, err)
		assert.Len(t, cr.Status.Replsets, 1)
		assert.NotContains(t, cr.Status.Replsets, "gone")
	})

	t.Run("conditions are applied in input order", func(t *testing.T) {
		cr := newStatusCR("rs0", "rs1", "rs2")

		results := []replsetClusterResult{
			{name: "rs0", result: clusterReconcileResult{state: api.AppStateInit, conditions: []api.ClusterCondition{initCondition("rs0")}}},
			{name: "rs1", result: clusterReconcileResult{state: api.AppStateInit, conditions: []api.ClusterCondition{initCondition("rs1")}}},
			{name: "rs2", result: clusterReconcileResult{state: api.AppStateInit, conditions: []api.ClusterCondition{initCondition("rs2")}}},
		}

		_, err := mergeReplsetResults(logr.Discard(), cr, results)
		require.NoError(t, err)

		// AddCondition dedupes by type, so all three collapse into one entry
		// whose message comes from the last result in input order. That is only
		// deterministic because the merge walks results by index.
		require.Len(t, cr.Status.Conditions, 1)
		assert.Equal(t, api.AppStateInit, cr.Status.Conditions[0].Type)
		assert.Equal(t, "rs2", cr.Status.Conditions[0].Message)
	})

	t.Run("joins all errors", func(t *testing.T) {
		cr := newStatusCR("rs0", "rs1", "rs2")

		results := []replsetClusterResult{
			{name: "rs0", result: clusterReconcileResult{state: api.AppStateError}, err: errors.New("boom rs0")},
			{name: "rs1", result: clusterReconcileResult{state: api.AppStateReady}},
			{name: "rs2", result: clusterReconcileResult{state: api.AppStateError}, err: errors.New("boom rs2")},
		}

		state, err := mergeReplsetResults(logr.Discard(), cr, results)
		assert.Equal(t, api.AppStateError, state)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "boom rs0")
		assert.Contains(t, err.Error(), "boom rs2")
	})
}

// TestReconcileReplsetClustersConcurrency drives the cluster loop over a sharded
// CR with a config server plus ten shards. The statefulsets do not exist in the
// fake client, so every replset short-circuits on "waiting for the pods" without
// dialing MongoDB, which is enough to exercise the pool and the merge phase.
//
// Under -race this fails loudly if any goroutine writes cr.Status.
func TestReconcileReplsetClustersConcurrency(t *testing.T) {
	const shards = 10

	run := func(t *testing.T, limit int) *api.PerconaServerMongoDB {
		t.Helper()

		cr, repls := newShardedCR(shards)
		r := buildFakeClient(cr)
		r.replsetConcurrency = limit

		ctx := logf.IntoContext(context.Background(), logr.Discard())

		state, err := r.reconcileReplsetClusters(ctx, cr, repls, nil)
		require.NoError(t, err)
		assert.Equal(t, api.AppStateInit, state)

		return cr
	}

	sequential := run(t, 1)
	concurrent := run(t, 8)

	// Every replset must be present, including the config server.
	assert.Len(t, concurrent.Status.Replsets, shards+1)
	assert.Contains(t, concurrent.Status.Replsets, api.ConfigReplSetName)

	// Concurrency must not change the observable status.
	assert.Equal(t, sequential.Status.Replsets, concurrent.Status.Replsets)
	assert.Equal(t, len(sequential.Status.Conditions), len(concurrent.Status.Conditions))
}

// TestReconcileReplsetServicesConcurrency checks the service loop creates the
// same set of services whether it runs sequentially or in parallel.
func TestReconcileReplsetServicesConcurrency(t *testing.T) {
	const shards = 10

	for _, limit := range []int{1, 8} {
		t.Run(fmt.Sprintf("limit=%d", limit), func(t *testing.T) {
			cr, repls := newShardedCR(shards)
			r := buildFakeClient(cr)
			r.replsetConcurrency = limit

			ctx := logf.IntoContext(context.Background(), logr.Discard())
			require.NoError(t, r.reconcileReplsetServices(ctx, cr, repls))

			for _, rs := range repls {
				svc := new(corev1.Service)
				nn := types.NamespacedName{Name: cr.Name + "-" + rs.Name, Namespace: cr.Namespace}
				require.NoError(t, r.client.Get(ctx, nn, svc), "headless service for %s", rs.Name)
			}
		})
	}
}

func newStatusCR(rsNames ...string) *api.PerconaServerMongoDB {
	cr := &api.PerconaServerMongoDB{
		ObjectMeta: metav1.ObjectMeta{Name: "test-cr", Namespace: "test-ns"},
		Status: api.PerconaServerMongoDBStatus{
			Replsets: make(map[string]api.ReplsetStatus, len(rsNames)),
		},
	}
	for _, name := range rsNames {
		cr.Status.Replsets[name] = api.ReplsetStatus{}
	}

	return cr
}

// newShardedCR builds a sharded CR with a config server replset followed by
// `shards` shard replsets, mirroring the ordering reconcileReplsets relies on.
func newShardedCR(shards int) (*api.PerconaServerMongoDB, []*api.ReplsetSpec) {
	cr := &api.PerconaServerMongoDB{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "concurrency-test",
			Namespace: "psmdb",
		},
		Spec: api.PerconaServerMongoDBSpec{
			CRVersion: version.Version(),
			Sharding:  api.Sharding{Enabled: true},
		},
		Status: api.PerconaServerMongoDBStatus{
			Replsets: make(map[string]api.ReplsetStatus, shards+1),
		},
	}

	repls := make([]*api.ReplsetSpec, 0, shards+1)
	repls = append(repls, &api.ReplsetSpec{
		Name:        api.ConfigReplSetName,
		ClusterRole: api.ClusterRoleConfigSvr,
		Size:        3,
	})
	for i := range shards {
		repls = append(repls, &api.ReplsetSpec{
			Name:        fmt.Sprintf("rs%d", i),
			ClusterRole: api.ClusterRoleShardSvr,
			Size:        3,
		})
	}

	for _, rs := range repls {
		cr.Status.Replsets[rs.Name] = api.ReplsetStatus{}
	}

	return cr, repls
}

func memberNames(rs api.ReplsetStatus) []string {
	names := make([]string, 0, len(rs.Members))
	for name := range rs.Members {
		names = append(names, name)
	}
	slices.Sort(names)

	return names
}

// rotate returns states shifted left by n, so the same multiset can be fed to
// the merge in different orders.
func rotate(states []api.AppState, n int) []api.AppState {
	if len(states) == 0 {
		return nil
	}

	n %= len(states)
	out := make([]api.AppState, 0, len(states))
	out = append(out, states[n:]...)
	out = append(out, states[:n]...)

	return out
}

func namesFor(states []api.AppState) []string {
	names := make([]string, 0, len(states))
	for i := range states {
		names = append(names, fmt.Sprintf("rs%d", i))
	}

	return names
}
