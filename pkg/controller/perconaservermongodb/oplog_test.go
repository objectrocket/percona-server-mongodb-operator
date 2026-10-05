package perconaservermongodb

import (
	"context"
	"io"
	"strconv"
	"testing"

	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	api "github.com/percona/percona-server-mongodb-operator/pkg/apis/psmdb/v1"
	"github.com/percona/percona-server-mongodb-operator/pkg/naming"
	"github.com/percona/percona-server-mongodb-operator/pkg/psmdb"
	"github.com/percona/percona-server-mongodb-operator/pkg/psmdb/mongo"
	mongoFake "github.com/percona/percona-server-mongodb-operator/pkg/psmdb/mongo/fake"
	"github.com/percona/percona-server-mongodb-operator/pkg/version"
)

// oplogMemberState is the per-host oplog state shared between the cluster
// client (which reports member health/state) and the standalone clients (which
// read and resize each member's oplog).
type oplogMemberState struct {
	sizeMB       float64
	state        mongo.MemberState
	health       mongo.MemberHealth
	resizeCalls  int
	compactCalls int
	// unhealthyAfterResize flips the member to unhealthy once it is resized, to
	// exercise the abort-on-unhealthy path.
	unhealthyAfterResize bool
}

// oplogClientProvider hands out fake cluster and standalone clients backed by a
// shared per-host state map so a resize issued through a standalone client is
// visible to the cluster client's health check.
type oplogClientProvider struct {
	cr      *api.PerconaServerMongoDB
	rs      *api.ReplsetSpec
	byHost  map[string]*oplogMemberState
	hostFor func(podName string) string
}

func (p *oplogClientProvider) Mongo(ctx context.Context, cr *api.PerconaServerMongoDB, rs *api.ReplsetSpec, role api.SystemUserRole) (mongo.Client, error) {
	return &oplogClusterClient{provider: p, Client: mongoFake.NewClient()}, nil
}

func (p *oplogClientProvider) Mongos(ctx context.Context, cr *api.PerconaServerMongoDB, role api.SystemUserRole) (mongo.Client, error) {
	return &oplogClusterClient{provider: p, Client: mongoFake.NewClient()}, nil
}

func (p *oplogClientProvider) Standalone(ctx context.Context, cr *api.PerconaServerMongoDB, role api.SystemUserRole, host string, tlsEnabled bool) (mongo.Client, error) {
	return &oplogStandaloneClient{provider: p, host: host, Client: mongoFake.NewClient()}, nil
}

type oplogClusterClient struct {
	provider *oplogClientProvider
	mongo.Client
}

func (c *oplogClusterClient) Disconnect(ctx context.Context) error { return nil }

func (c *oplogClusterClient) RSStatus(ctx context.Context) (mongo.Status, error) {
	members := make([]*mongo.Member, 0, len(c.provider.byHost))
	id := 0
	for host, st := range c.provider.byHost {
		members = append(members, &mongo.Member{
			Id:     id,
			Name:   host,
			State:  st.state,
			Health: st.health,
		})
		id++
	}
	return mongo.Status{Members: members, OKResponse: mongo.OKResponse{OK: 1}}, nil
}

type oplogStandaloneClient struct {
	provider *oplogClientProvider
	host     string
	mongo.Client
}

func (c *oplogStandaloneClient) Disconnect(ctx context.Context) error { return nil }

func (c *oplogStandaloneClient) GetOplogSizeMB(ctx context.Context) (float64, error) {
	st, ok := c.provider.byHost[c.host]
	if !ok {
		return 0, errors.Errorf("unknown host %s", c.host)
	}
	return st.sizeMB, nil
}

func (c *oplogStandaloneClient) ResizeOplog(ctx context.Context, sizeMB float64) error {
	st := c.provider.byHost[c.host]
	st.resizeCalls++
	st.sizeMB = sizeMB
	if st.unhealthyAfterResize {
		st.health = mongo.MemberHealthDown
	}
	return nil
}

func (c *oplogStandaloneClient) CompactOplog(ctx context.Context) error {
	c.provider.byHost[c.host].compactCalls++
	return nil
}

func setupOplogTest(t *testing.T, conf api.MongoConfiguration, clusterRole api.ClusterRole, rsName string) (*ReconcilePerconaServerMongoDB, *api.PerconaServerMongoDB, *api.ReplsetSpec, *oplogClientProvider) {
	t.Helper()
	ctx := context.Background()

	cr := &api.PerconaServerMongoDB{
		ObjectMeta: metav1.ObjectMeta{Name: "oplog-test", Namespace: "psmdb", Generation: 1},
		Spec: api.PerconaServerMongoDBSpec{
			CRVersion: version.Version(),
			Image:     "percona/percona-server-mongodb:latest",
			Replsets: []*api.ReplsetSpec{
				{Name: rsName, Size: 3, ClusterRole: clusterRole, Configuration: conf},
			},
		},
		Status: api.PerconaServerMongoDBStatus{Replsets: map[string]api.ReplsetStatus{}},
	}
	cr.Spec.Replsets[0].VolumeSpec = fakeVolumeSpec(t)
	require.NoError(t, cr.CheckNSetDefaults(ctx, version.PlatformKubernetes))
	cr.Status.Replsets = map[string]api.ReplsetStatus{}

	rs := cr.Spec.Replsets[0]

	objs := []client.Object{cr, internalUsersSecret(cr)}
	objs = append(objs, fakeStatefulset(cr, rs, rs.Size, "rev", naming.ComponentMongod))
	pods := make([]client.Object, 0, 3)
	for i := 0; i < 3; i++ {
		p := fakeMongodPod(cr, rs, cr.Name+"-"+rs.Name+"-"+strconv.Itoa(i))
		p.Labels[naming.LabelKubernetesComponent] = naming.ComponentMongod
		pods = append(pods, p)
		objs = append(objs, p)
	}

	r := buildFakeClient(objs...)
	r.serverVersion = &version.ServerVersion{Platform: version.PlatformKubernetes}

	hostFor := func(podName string) string {
		for _, o := range pods {
			pod := o.(*corev1.Pod)
			if pod.Name != podName {
				continue
			}
			host, err := psmdb.MongoHost(ctx, r.client, cr, cr.Spec.ClusterServiceDNSMode, rs, rs.Expose.Enabled, *pod)
			require.NoError(t, err)
			return host
		}
		return ""
	}

	provider := &oplogClientProvider{cr: cr, rs: rs, byHost: map[string]*oplogMemberState{}, hostFor: hostFor}
	r.mongoClientProvider = provider

	return r, cr, rs, provider
}

// seedMembers populates the provider state: the first pod is primary, the rest
// are secondaries, all healthy with the given starting oplog size.
func seedMembers(t *testing.T, cr *api.PerconaServerMongoDB, rs *api.ReplsetSpec, provider *oplogClientProvider, startSizeMB float64) {
	t.Helper()
	for i := 0; i < int(rs.Size); i++ {
		host := provider.hostFor(cr.Name + "-" + rs.Name + "-" + strconv.Itoa(i))
		require.NotEmpty(t, host)
		state := mongo.MemberStateSecondary
		if i == 0 {
			state = mongo.MemberStatePrimary
		}
		provider.byHost[host] = &oplogMemberState{
			sizeMB: startSizeMB,
			state:  state,
			health: mongo.MemberHealthUp,
		}
	}
}

func TestReconcileOplogSize(t *testing.T) {
	logf.SetLogger(zap.New(zap.WriteTo(io.Discard)))
	ctx := context.Background()

	t.Run("no-op when all members match desired", func(t *testing.T) {
		r, cr, rs, provider := setupOplogTest(t, "replication:\n  oplogSizeMB: 2000", "", "rs0")
		seedMembers(t, cr, rs, provider, 2000)

		cli, err := r.mongoClientWithRole(ctx, cr, rs, api.RoleClusterAdmin)
		require.NoError(t, err)

		require.NoError(t, r.reconcileOplogSize(ctx, cr, rs, cli))

		for host, st := range provider.byHost {
			assert.Equalf(t, 0, st.resizeCalls, "no resize expected for %s", host)
		}
	})

	t.Run("increase resizes every member and validates", func(t *testing.T) {
		r, cr, rs, provider := setupOplogTest(t, "replication:\n  oplogSizeMB: 2000", "", "rs0")
		seedMembers(t, cr, rs, provider, 990)

		cli, err := r.mongoClientWithRole(ctx, cr, rs, api.RoleClusterAdmin)
		require.NoError(t, err)

		require.NoError(t, r.reconcileOplogSize(ctx, cr, rs, cli))

		for host, st := range provider.byHost {
			assert.Equalf(t, float64(2000), st.sizeMB, "member %s resized", host)
			assert.Equalf(t, 1, st.resizeCalls, "member %s resized once", host)
			assert.Equalf(t, 0, st.compactCalls, "no compact on increase for %s", host)
		}
	})

	t.Run("skips unset oplogSizeMB", func(t *testing.T) {
		r, cr, rs, provider := setupOplogTest(t, "", "", "rs0")
		seedMembers(t, cr, rs, provider, 990)

		cli, err := r.mongoClientWithRole(ctx, cr, rs, api.RoleClusterAdmin)
		require.NoError(t, err)

		require.NoError(t, r.reconcileOplogSize(ctx, cr, rs, cli))

		for _, st := range provider.byHost {
			assert.Equal(t, 0, st.resizeCalls)
		}
	})

	t.Run("aborts when a member becomes unhealthy after resize", func(t *testing.T) {
		r, cr, rs, provider := setupOplogTest(t, "replication:\n  oplogSizeMB: 2000", "", "rs0")
		seedMembers(t, cr, rs, provider, 990)

		// Flag the first secondary to crash after its resize.
		var flagged bool
		for host, st := range provider.byHost {
			if st.state == mongo.MemberStateSecondary {
				st.unhealthyAfterResize = true
				flagged = true
				_ = host
				break
			}
		}
		require.True(t, flagged)

		cli, err := r.mongoClientWithRole(ctx, cr, rs, api.RoleClusterAdmin)
		require.NoError(t, err)

		err = r.reconcileOplogSize(ctx, cr, rs, cli)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unhealthy")

		// The primary must not have been resized because the loop aborted on the
		// unhealthy secondary.
		for _, st := range provider.byHost {
			if st.state == mongo.MemberStatePrimary {
				assert.Equal(t, 0, st.resizeCalls, "primary resize must not run after abort")
			}
		}
	})

	t.Run("config-server replset is reconciled", func(t *testing.T) {
		r, cr, rs, provider := setupOplogTest(t, "replication:\n  oplogSizeMB: 2000", api.ClusterRoleConfigSvr, "cfg")
		seedMembers(t, cr, rs, provider, 990)

		cli, err := r.mongoClientWithRole(ctx, cr, rs, api.RoleClusterAdmin)
		require.NoError(t, err)

		require.NoError(t, r.reconcileOplogSize(ctx, cr, rs, cli))

		for host, st := range provider.byHost {
			assert.Equalf(t, float64(2000), st.sizeMB, "cfg member %s resized", host)
			assert.Equalf(t, 1, st.resizeCalls, "cfg member %s resized once", host)
			assert.Equalf(t, 0, st.compactCalls, "no compact on increase for %s", host)
		}
	})

	t.Run("decrease is gated off by default", func(t *testing.T) {
		r, cr, rs, provider := setupOplogTest(t, "replication:\n  oplogSizeMB: 990", "", "rs0")
		seedMembers(t, cr, rs, provider, 2000)

		cli, err := r.mongoClientWithRole(ctx, cr, rs, api.RoleClusterAdmin)
		require.NoError(t, err)

		require.NoError(t, r.reconcileOplogSize(ctx, cr, rs, cli))

		for host, st := range provider.byHost {
			assert.Equalf(t, float64(2000), st.sizeMB, "member %s must not shrink", host)
			assert.Equalf(t, 0, st.resizeCalls, "no resize on gated decrease for %s", host)
			assert.Equalf(t, 0, st.compactCalls, "no compact on gated decrease for %s", host)
		}
	})
}
