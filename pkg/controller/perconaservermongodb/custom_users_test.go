package perconaservermongodb

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	api "github.com/percona/percona-server-mongodb-operator/pkg/apis/psmdb/v1"
	"github.com/percona/percona-server-mongodb-operator/pkg/naming"
	"github.com/percona/percona-server-mongodb-operator/pkg/psmdb/mongo"
	"github.com/percona/percona-server-mongodb-operator/pkg/version"
)

// mockMongoClientForRoles is a minimal mock to test updateRoles behavior.
type mockMongoClientForRoles struct {
	mongo.Client
	updateRolesCalled bool
}

func (m *mockMongoClientForRoles) UpdateUserRoles(ctx context.Context, db, username string, roles []mongo.Role) error {
	m.updateRolesCalled = true
	return nil
}

func TestUpdateRoles(t *testing.T) {
	tests := []struct {
		name               string
		user               *api.User
		userInfo           *mongo.User
		expectUpdateCalled bool
	}{
		{
			name: "same roles same order - no update",
			user: &api.User{
				Name: "testuser",
				DB:   "testdb",
				Roles: []api.UserRole{
					{Name: "readWrite", DB: "db1"},
					{Name: "read", DB: "db2"},
				},
			},
			userInfo: &mongo.User{
				Roles: []mongo.Role{
					{Role: "readWrite", DB: "db1"},
					{Role: "read", DB: "db2"},
				},
			},
			expectUpdateCalled: false,
		},
		{
			name: "same roles different order - no update",
			user: &api.User{
				Name: "testuser",
				DB:   "testdb",
				Roles: []api.UserRole{
					{Name: "readWrite", DB: "db1"},
					{Name: "clusterMonitor", DB: "admin"},
					{Name: "readWrite", DB: "db2"},
				},
			},
			userInfo: &mongo.User{
				Roles: []mongo.Role{
					{Role: "clusterMonitor", DB: "admin"},
					{Role: "readWrite", DB: "db2"},
					{Role: "readWrite", DB: "db1"},
				},
			},
			expectUpdateCalled: false,
		},
		{
			name: "different roles - update called",
			user: &api.User{
				Name: "testuser",
				DB:   "testdb",
				Roles: []api.UserRole{
					{Name: "readWrite", DB: "db1"},
					{Name: "read", DB: "db2"},
				},
			},
			userInfo: &mongo.User{
				Roles: []mongo.Role{
					{Role: "readWrite", DB: "db1"},
					{Role: "readWrite", DB: "db3"},
				},
			},
			expectUpdateCalled: true,
		},
		{
			name: "nil userInfo - no update",
			user: &api.User{
				Name: "testuser",
				DB:   "testdb",
				Roles: []api.UserRole{
					{Name: "readWrite", DB: "db1"},
				},
			},
			userInfo:           nil,
			expectUpdateCalled: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &mockMongoClientForRoles{}
			err := updateRoles(t.Context(), mock, tt.user, tt.userInfo)
			assert.NoError(t, err)
			assert.Equal(t, tt.expectUpdateCalled, mock.updateRolesCalled,
				"UpdateUserRoles called = %v, want %v", mock.updateRolesCalled, tt.expectUpdateCalled)
		})
	}
}

func TestRolesChanged(t *testing.T) {
	r2 := &mongo.Role{
		Privileges: []mongo.RolePrivilege{
			{
				Resource: map[string]interface{}{
					"db":         "test",
					"collection": "test",
				},
				Actions: []string{"find"},
			},
			{
				Resource: map[string]interface{}{
					"db":         "test-two",
					"collection": "test-two",
				},
				Actions: []string{"find", "insert"},
			},
		},
		AuthenticationRestrictions: []mongo.RoleAuthenticationRestriction{
			{
				ClientSource: []string{"localhost", "111.111.111.111"},
			},
			{
				ServerAddress: []string{"localhost", "10.10.10.10"},
				ClientSource:  []string{"localhost", "111.111.111.111"},
			},
		},
		Roles: []mongo.InheritenceRole{
			{
				Role: "read",
				DB:   "test",
			},
			{
				Role: "insert",
				DB:   "test",
			},
		},
	}

	tests := []struct {
		name string
		r1   *mongo.Role
		r2   *mongo.Role
		want bool
	}{
		{
			name: "Roles the same",
			want: false,
			r1: &mongo.Role{
				Privileges: []mongo.RolePrivilege{
					{
						Resource: map[string]interface{}{
							"collection": "test",
							"db":         "test",
						},
						Actions: []string{"find"},
					},
					{
						Resource: map[string]interface{}{
							"db":         "test-two",
							"collection": "test-two",
						},
						Actions: []string{"insert", "find"},
					},
				},
				AuthenticationRestrictions: []mongo.RoleAuthenticationRestriction{
					{
						ClientSource: []string{"111.111.111.111", "localhost"},
					},
					{
						ServerAddress: []string{"10.10.10.10", "localhost"},
						ClientSource:  []string{"localhost", "111.111.111.111"},
					},
				},
				Roles: []mongo.InheritenceRole{
					{
						Role: "read",
						DB:   "test",
					},
					{
						Role: "insert",
						DB:   "test",
					},
				},
			},
			r2: r2,
		},
		{
			name: "Roles different",
			want: true,
			r1: &mongo.Role{
				Privileges: []mongo.RolePrivilege{
					{
						Resource: map[string]interface{}{
							"collection": "test",
							"db":         "test",
						},
						Actions: []string{"find", "update"},
					},
					{
						Resource: map[string]interface{}{
							"db":         "test-two",
							"collection": "test-two-different",
						},
						Actions: []string{"insert"},
					},
				},
				AuthenticationRestrictions: []mongo.RoleAuthenticationRestriction{
					{
						ClientSource: []string{"111.111.111.111", "localhost"},
					},
					{
						ServerAddress: []string{"10.10.10.10", "localhost"},
						ClientSource:  []string{"localhost", "111.111.111.111"},
					},
				},
				Roles: []mongo.InheritenceRole{
					{
						Role: "read",
						DB:   "test",
					},
					{
						Role: "update",
						DB:   "test-two",
					},
					{
						Role: "insert",
						DB:   "test",
					},
				},
			},
			r2: r2,
		},
		{
			name: "Privileges different",
			want: true,
			r1: &mongo.Role{
				Privileges: []mongo.RolePrivilege{
					{
						Resource: map[string]interface{}{
							"collection": "test",
							"db":         "test",
						},
						Actions: []string{"find", "update"},
					},
					{
						Resource: map[string]interface{}{
							"db":         "test-two",
							"collection": "test-two-different",
						},
						Actions: []string{"insert"},
					},
				},
				AuthenticationRestrictions: []mongo.RoleAuthenticationRestriction{
					{
						ClientSource: []string{"111.111.111.111", "localhost"},
					},
					{
						ServerAddress: []string{"10.10.10.10", "localhost"},
						ClientSource:  []string{"localhost", "111.111.111.111"},
					},
				},
				Roles: []mongo.InheritenceRole{
					{
						Role: "read",
						DB:   "test",
					},
					{
						Role: "insert",
						DB:   "test",
					},
				},
			},
			r2: r2,
		},
		{
			name: "AuthenticationRestrictions different",
			want: true,
			r1: &mongo.Role{
				Privileges: []mongo.RolePrivilege{
					{
						Resource: map[string]interface{}{
							"db":         "test",
							"collection": "test",
						},
						Actions: []string{"find"},
					},
					{
						Resource: map[string]interface{}{
							"collection": "test-two",
							"db":         "test-two",
						},
						Actions: []string{"insert", "find"},
					},
				},
				AuthenticationRestrictions: []mongo.RoleAuthenticationRestriction{
					{
						ServerAddress: []string{"1.1.1.1", "localhost"},
					},
					{
						ClientSource: []string{"localhost"},
					},
				},
				Roles: []mongo.InheritenceRole{
					{
						Role: "read",
						DB:   "test",
					},
					{
						Role: "insert",
						DB:   "test",
					},
				},
			},
			r2: r2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := rolesChanged(tt.r1, tt.r2); got != tt.want {
				t.Errorf("rolesChanged() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestValidateUser(t *testing.T) {

	tests := map[string]struct {
		user            *api.User
		actualUser      *api.User
		sysUserNames    map[string]struct{}
		uniqueUserNames map[string]struct{}
		expectedErr     error
	}{
		"invalid input for sysUserNames and uniqueUserNames": {
			user:        &api.User{Name: "john", Roles: []api.UserRole{{Name: "rolename", DB: "testdb"}}, DB: "testdb"},
			expectedErr: errors.New("invalid sys or unique usernames config"),
		},
		"valid non-existing username": {
			user:            &api.User{Name: "john", Roles: []api.UserRole{{Name: "rolename", DB: "testdb"}}, DB: "testdb"},
			actualUser:      &api.User{Name: "john", Roles: []api.UserRole{{Name: "rolename", DB: "testdb"}}, DB: "testdb"},
			sysUserNames:    map[string]struct{}{},
			uniqueUserNames: map[string]struct{}{},
		},
		"valid non-existing username, missing db and password secret ref": {
			user: &api.User{Name: "john", Roles: []api.UserRole{{Name: "rolename"}}, PasswordSecretRef: &api.SecretKeySelector{}},
			actualUser: &api.User{
				Name:              "john",
				Roles:             []api.UserRole{{Name: "rolename"}},
				DB:                "admin",
				PasswordSecretRef: &api.SecretKeySelector{Key: "password"},
			},
			sysUserNames:    map[string]struct{}{},
			uniqueUserNames: map[string]struct{}{},
		},
		"sys reserved username": {
			user:            &api.User{Name: "root", Roles: []api.UserRole{{Name: "rolename", DB: "testdb"}}, DB: "testdb"},
			sysUserNames:    map[string]struct{}{"root": {}},
			uniqueUserNames: map[string]struct{}{},
			expectedErr:     errors.New("creating user with reserved user name root is forbidden"),
		},
		"not unique username": {
			user:            &api.User{Name: "useradmin", Roles: []api.UserRole{{Name: "rolename", DB: "testdb"}}, DB: "testdb"},
			sysUserNames:    map[string]struct{}{},
			uniqueUserNames: map[string]struct{}{"useradmin": {}},
			expectedErr:     errors.New("username useradmin should be unique"),
		},
		"no roles defined": {
			user:            &api.User{Name: "john", Roles: []api.UserRole{}, DB: "testdb"},
			sysUserNames:    map[string]struct{}{},
			uniqueUserNames: map[string]struct{}{},
			expectedErr:     errors.New("user john must have at least one role"),
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			err := validateUser(tt.user, tt.sysUserNames, tt.uniqueUserNames)
			if tt.expectedErr != nil {
				assert.EqualError(t, err, tt.expectedErr.Error())
			} else {
				assert.Equal(t, tt.user, tt.actualUser)
				assert.NoError(t, err)
			}
		})
	}
}

func TestGetCustomUserSecret(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	err := corev1.AddToScheme(scheme)
	assert.NoError(t, err)
	err = api.SchemeBuilder.AddToScheme(scheme)
	assert.NoError(t, err)

	ns := "test-ns"
	passKey := "password"

	tests := map[string]struct {
		crName            string
		client            func() client.Client
		user              *api.User
		hasExistingSecret bool
		errMsg            string
	}{
		"create default secret if not exists": {
			crName: "my-cluster-create-default-secret",
			client: func() client.Client {
				return fake.NewClientBuilder().WithScheme(scheme).Build()
			},
			user: &api.User{},
		},
		"user has custom secret reference that exists": {
			crName: "my-cluster-user-has-secret",
			client: func() client.Client {
				existingSecret := &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "custom-secret",
						Namespace: ns,
					},
					Data: map[string][]byte{
						passKey: []byte("existing-password"),
					},
				}

				return fake.NewClientBuilder().WithScheme(scheme).WithObjects(existingSecret).Build()
			},
			user: &api.User{
				PasswordSecretRef: &api.SecretKeySelector{
					Name: "custom-secret",
				},
			},
			hasExistingSecret: true,
		},
		"user has custom secret reference but secret does not exist": {
			crName: "my-cluster-has-missing-secret",
			client: func() client.Client {
				return fake.NewClientBuilder().WithScheme(scheme).Build()
			},
			user: &api.User{
				PasswordSecretRef: &api.SecretKeySelector{
					Name: "missing-secret",
				},
			},
			errMsg: "failed to get user secret",
		},
		"existing default secret missing password key, create new": {
			crName: "my-cluster-existing-secret-missing-password",
			client: func() client.Client {
				defaultSecret := &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "my-cluster-existing-secret-missing-password-custom-user-secret",
						Namespace: ns,
					},
					Data: map[string][]byte{},
				}

				return fake.NewClientBuilder().WithScheme(scheme).WithObjects(defaultSecret).Build()
			},
			user: &api.User{},
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			cr := &api.PerconaServerMongoDB{
				ObjectMeta: metav1.ObjectMeta{
					Name:      tt.crName,
					Namespace: ns,
				},
			}

			secret, err := getCustomUserSecret(ctx, tt.client(), cr, tt.user, passKey)
			if tt.hasExistingSecret && tt.errMsg == "" {
				assert.NoError(t, err)
				assert.Equal(t, secret.Name, "custom-secret")
				assert.Equal(t, tt.user.SecretName(cr), secret.Name)
				assert.Equal(t, naming.SecretCustomUserConnStrName(cr, tt.user), secret.Name+"-conn-str")
				assert.Equal(t, string(secret.Data[passKey]), "existing-password")
				return
			}
			if !tt.hasExistingSecret && tt.errMsg == "" {
				assert.NoError(t, err)
				assert.Equal(t, secret.Name, tt.crName+"-custom-user-secret")
				assert.Equal(t, tt.user.SecretName(cr), secret.Name)
				assert.Equal(t, naming.SecretCustomUserConnStrName(cr, tt.user), secret.Name+"-conn-str")
				assert.NotEmpty(t, string(secret.Data[passKey]))
			}
			if tt.errMsg != "" {
				assert.Nil(t, secret)
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.errMsg)
			}

		})
	}
}

// trackingMongoClientProvider records which connections (mongos vs shard replset)
// were made during reconcileCustomUsers.
type trackingMongoClientProvider struct {
	mu          sync.Mutex
	mongosCount int
	shardCalls  []string // replset names connected to directly
	client      client.Client
}

func (p *trackingMongoClientProvider) Mongo(ctx context.Context, cr *api.PerconaServerMongoDB, rs *api.ReplsetSpec, role api.SystemUserRole) (mongo.Client, error) {
	p.mu.Lock()
	p.shardCalls = append(p.shardCalls, rs.Name)
	p.mu.Unlock()
	return &noopMongoClient{client: p.client, cr: cr}, nil
}

func (p *trackingMongoClientProvider) Mongos(ctx context.Context, cr *api.PerconaServerMongoDB, role api.SystemUserRole) (mongo.Client, error) {
	p.mu.Lock()
	p.mongosCount++
	p.mu.Unlock()
	return &noopMongoClient{client: p.client, cr: cr}, nil
}

func (p *trackingMongoClientProvider) Standalone(ctx context.Context, cr *api.PerconaServerMongoDB, role api.SystemUserRole, host string, tlsEnabled bool) (mongo.Client, error) {
	return &noopMongoClient{client: p.client, cr: cr}, nil
}

// noopMongoClient is a mongo.Client that returns empty results for all calls —
// sufficient for testing that the right connections are made.
type noopMongoClient struct {
	client client.Client
	cr     *api.PerconaServerMongoDB
}

func (c *noopMongoClient) Disconnect(ctx context.Context) error { return nil }
func (c *noopMongoClient) Database(name string, opts ...options.Lister[options.DatabaseOptions]) mongo.ClientDatabase {
	return nil
}
func (c *noopMongoClient) Ping(ctx context.Context, rp *readpref.ReadPref) error { return nil }
func (c *noopMongoClient) GetUserInfo(ctx context.Context, username, db string) (*mongo.User, error) {
	return nil, nil
}
func (c *noopMongoClient) CreateUser(ctx context.Context, db, username, pwd string, roles ...mongo.Role) error {
	return nil
}
func (c *noopMongoClient) UpdateUserPass(ctx context.Context, db, name, pass string) error {
	return nil
}
func (c *noopMongoClient) UpdateUserRoles(ctx context.Context, db, username string, roles []mongo.Role) error {
	return nil
}
func (c *noopMongoClient) UpdateUser(ctx context.Context, currName, newName, pass string) error {
	return nil
}
func (c *noopMongoClient) GetRole(ctx context.Context, db, role string) (*mongo.Role, error) {
	return nil, nil
}
func (c *noopMongoClient) CreateRole(ctx context.Context, db string, role mongo.Role) error {
	return nil
}
func (c *noopMongoClient) UpdateRole(ctx context.Context, db string, role mongo.Role) error {
	return nil
}
func (c *noopMongoClient) RSBuildInfo(ctx context.Context) (mongo.BuildInfo, error) {
	return mongo.BuildInfo{}, nil
}
func (c *noopMongoClient) RSStatus(ctx context.Context) (mongo.Status, error) {
	return mongo.Status{}, nil
}
func (c *noopMongoClient) WriteConfig(ctx context.Context, cfg mongo.RSConfig, force bool) error {
	return nil
}
func (c *noopMongoClient) ReadConfig(ctx context.Context) (mongo.RSConfig, error) {
	return mongo.RSConfig{}, nil
}
func (c *noopMongoClient) StartBalancer(ctx context.Context) error { return nil }
func (c *noopMongoClient) StopBalancer(ctx context.Context) error  { return nil }
func (c *noopMongoClient) IsBalancerRunning(ctx context.Context) (bool, error) {
	return false, nil
}
func (c *noopMongoClient) GetFCV(ctx context.Context) (string, error)       { return "", nil }
func (c *noopMongoClient) SetFCV(ctx context.Context, version string) error { return nil }
func (c *noopMongoClient) ListDBs(ctx context.Context) (mongo.DBList, error) {
	return mongo.DBList{}, nil
}
func (c *noopMongoClient) ListShard(ctx context.Context) (mongo.ShardList, error) {
	return mongo.ShardList{}, nil
}
func (c *noopMongoClient) RemoveShard(ctx context.Context, shard string) (mongo.ShardRemoveResp, error) {
	return mongo.ShardRemoveResp{}, nil
}
func (c *noopMongoClient) StepDown(ctx context.Context, seconds int, force bool) error { return nil }
func (c *noopMongoClient) IsMaster(ctx context.Context) (*mongo.IsMasterResp, error) {
	return &mongo.IsMasterResp{}, nil
}
func (c *noopMongoClient) Freeze(ctx context.Context, seconds int) error { return nil }
func (c *noopMongoClient) SetDefaultRWConcern(ctx context.Context, readConcern, writeConcern string, writeConcernWTimeout int) error {
	return nil
}
func (c *noopMongoClient) AddShard(ctx context.Context, rsName, host string) error { return nil }

// TestReconcileCustomUsers_ShardedPropagation verifies that for a sharded cluster,
// reconcileCustomUsers connects to mongos once (cluster-level) AND to each shard
// replset directly, ensuring custom users like clusterSuperAdmin are created on shards.
func TestReconcileCustomUsers_ShardedPropagation(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	assert.NoError(t, corev1.AddToScheme(scheme))
	assert.NoError(t, api.SchemeBuilder.AddToScheme(scheme))

	ns := "test-ns"
	clusterName := "test-cluster"

	// Build internal secret so fetchSystemUserNames doesn't fail
	internalSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "internal-" + clusterName + "-users",
			Namespace: ns,
		},
		Data: map[string][]byte{},
	}

	// Password secret for clusterSuperAdmin
	passSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "internal-" + clusterName + "-users",
			Namespace: ns,
		},
		Data: map[string][]byte{
			"MONGODB_CLUSTER_SUPER_ADMIN_PASSWORD": []byte("supersecret"),
		},
	}
	_ = passSecret

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(internalSecret).Build()

	cr := &api.PerconaServerMongoDB{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clusterName,
			Namespace: ns,
		},
		Spec: api.PerconaServerMongoDBSpec{
			CRVersion:               version.Version(),
			ClusterServiceDNSMode:   api.DNSModeInternal,
			ClusterServiceDNSSuffix: "svc.cluster.local",
			TLS:                     &api.TLSSpec{Mode: api.TLSModeDisabled},
			Secrets:                 &api.SecretsSpec{},
			Sharding: api.Sharding{
				Enabled: true,
				Mongos:  &api.MongosSpec{},
			},
			Replsets: []*api.ReplsetSpec{
				{Name: "rs0"},
				{Name: "rs1"},
			},
			Users: []api.User{
				{
					Name: "clusterSuperAdmin",
					DB:   "admin",
					PasswordSecretRef: &api.SecretKeySelector{
						Name: "internal-" + clusterName + "-users",
						Key:  "MONGODB_CLUSTER_SUPER_ADMIN_PASSWORD",
					},
					Roles: []api.UserRole{
						{Name: "clusterAdmin", DB: "admin"},
					},
				},
			},
		},
		Status: api.PerconaServerMongoDBStatus{
			State: api.AppStateReady,
		},
	}

	tracker := &trackingMongoClientProvider{client: fakeClient}
	r := &ReconcilePerconaServerMongoDB{
		client:              fakeClient,
		mongoClientProvider: tracker,
	}

	err := r.reconcileCustomUsers(ctx, cr)
	assert.NoError(t, err)

	// Must have connected to mongos exactly once
	assert.Equal(t, 1, tracker.mongosCount, "expected exactly 1 mongos connection")

	// Must have connected to each shard replset directly
	assert.ElementsMatch(t, []string{"rs0", "rs1"}, tracker.shardCalls,
		"expected direct connections to each shard replset")
}

// TestUpdateRoles_OrderInsensitive verifies that updateRoles does not issue an
// UpdateUserRoles call when the same roles are returned in a different order by
// MongoDB, and does issue one when the set genuinely differs.
func TestUpdateRoles_OrderInsensitive(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name         string
		userRoles    []api.UserRole
		mongoRoles   []mongo.Role
		expectUpdate bool
	}{
		{
			name: "same roles different order - no update",
			userRoles: []api.UserRole{
				{Name: "readWrite", DB: "mydb"},
				{Name: "clusterAdmin", DB: "admin"},
			},
			mongoRoles: []mongo.Role{
				{Role: "clusterAdmin", DB: "admin"},
				{Role: "readWrite", DB: "mydb"},
			},
			expectUpdate: false,
		},
		{
			name: "genuinely different roles - update",
			userRoles: []api.UserRole{
				{Name: "readWrite", DB: "mydb"},
				{Name: "clusterAdmin", DB: "admin"},
			},
			mongoRoles: []mongo.Role{
				{Role: "read", DB: "mydb"},
				{Role: "clusterAdmin", DB: "admin"},
			},
			expectUpdate: true,
		},
		{
			name: "extra role in mongo - update",
			userRoles: []api.UserRole{
				{Name: "readWrite", DB: "mydb"},
			},
			mongoRoles: []mongo.Role{
				{Role: "readWrite", DB: "mydb"},
				{Role: "clusterAdmin", DB: "admin"},
			},
			expectUpdate: true,
		},
		{
			name: "roles with extra fields populated by mongo - no update",
			userRoles: []api.UserRole{
				{Name: "readWrite", DB: "mydb"},
			},
			mongoRoles: []mongo.Role{
				{
					Role:       "readWrite",
					DB:         "mydb",
					Roles:      []mongo.InheritenceRole{{Role: "read", DB: "mydb"}},
					Privileges: []mongo.RolePrivilege{{Actions: []string{"find"}}},
				},
			},
			expectUpdate: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			updated := false
			cli := &trackingUpdateRolesClient{
				noopMongoClient: noopMongoClient{},
				onUpdateRoles: func() {
					updated = true
				},
			}

			user := &api.User{
				Name:  "testuser",
				DB:    "admin",
				Roles: tt.userRoles,
			}
			userInfo := &mongo.User{
				DB:    "admin",
				Roles: tt.mongoRoles,
			}

			err := updateRoles(ctx, cli, user, userInfo)
			assert.NoError(t, err)
			assert.Equal(t, tt.expectUpdate, updated, "unexpected UpdateUserRoles call status")
		})
	}
}

func TestBuildAnnotationKey(t *testing.T) {
	tests := []struct {
		name      string
		crName    string
		crVersion *string
		userName  string
		want      string
	}{
		{
			name:     "short names",
			crName:   "my-cluster",
			userName: "user1",
			want:     "percona.com/jutnf2os64q2pc2xoctfhea46fv4prb3amrfpvxdoxayh3vul3gq",
		},
		{
			name:     "user name fills the old limit",
			crName:   "a",
			userName: strings.Repeat("x", 44),
			want:     "percona.com/g76mrdlwvbdpwmj3oocsvaxq2aqlfk4k5m2xj557vtrennb42pmq",
		},
		{
			name:     "both names exceed the old limit",
			crName:   "very-long-cluster-name-that-exceeds",
			userName: "very-long-user-name-that-also-exceeds",
			want:     "percona.com/7grtpeecp5s6efjbylgpwojvpr5hwo7np6c2ccj6jbpsbk3xw6gq",
		},
		{
			name:     "very long cluster name",
			crName:   strings.Repeat("a", 100),
			userName: "user",
			want:     "percona.com/vjvkygmvzldwvj6m3x4lzu7s3dvp5mwqstqbcxpooxudohhek5dq",
		},
		{
			name:     "very long user name",
			crName:   "cluster",
			userName: strings.Repeat("b", 100),
			want:     "percona.com/pfu7boshbs4o6gyzm3gf4vkt4mptio5uorthdt5qhu3su5dwiaeq",
		},
		{
			name:     "both names very long",
			crName:   strings.Repeat("c", 50),
			userName: strings.Repeat("d", 50),
			want:     "percona.com/4ztrxbkdt2ldokrlo6plgpp2vapvt66ivyvgdfuvixfk5zyofc3q",
		},
		{
			name:      "v1.22.0 behavior",
			crName:    "my-cluster-name",
			crVersion: new("1.22.0"),
			userName:  "my-custom-user",
			want:      "percona.com/my-cluster-name-my-custom-user-hash",
		},
	}

	const prefix = "percona.com/"

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cr := &api.PerconaServerMongoDB{
				ObjectMeta: metav1.ObjectMeta{
					Name:      tt.crName,
					Namespace: "namespace",
				},
				Spec: api.PerconaServerMongoDBSpec{
					CRVersion: version.Version(),
				},
			}
			if tt.crVersion != nil {
				cr.Spec.CRVersion = *tt.crVersion
			}

			got := buildAnnotationKey(cr, tt.userName)

			assert.Equal(t, tt.want, got, "buildAnnotationKey() = %v, want %v", got, tt.want)

			assert.True(t, strings.HasPrefix(got, prefix), "buildAnnotationKey() = %v, should start with %v", got, prefix)

			if tt.crVersion == nil {
				namePart := got[len(prefix):]
				assert.Len(t, namePart, 52, "buildAnnotationKey() name part should be a fixed-length hash. Got: %v", got)
				assert.True(t, len(namePart) <= maxAnnotationNameLength, "buildAnnotationKey() name part length = %v, should be <= %v. Got: %v", len(namePart), maxAnnotationNameLength, got)
			}
		})
	}
}

// trackingUpdateRolesClient extends noopMongoClient to track UpdateUserRoles calls.
type trackingUpdateRolesClient struct {
	noopMongoClient
	onUpdateRoles func()
}

func (c *trackingUpdateRolesClient) UpdateUserRoles(ctx context.Context, db, username string, roles []mongo.Role) error {
	if c.onUpdateRoles != nil {
		c.onUpdateRoles()
	}
	return nil
}

// TestReconcileCustomUsers_SteadyStateNoShardConnections verifies that after an
// initial reconcile (which creates users on shards), a second reconcile with
// unchanged state opens zero shard connections.
func TestReconcileCustomUsers_SteadyStateNoShardConnections(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	assert.NoError(t, corev1.AddToScheme(scheme))
	assert.NoError(t, api.SchemeBuilder.AddToScheme(scheme))

	ns := "test-ns"
	clusterName := "steady-state-cluster"

	passSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clusterName + "-custom-user-secret",
			Namespace: ns,
		},
		Data: map[string][]byte{
			"MONGODB_CLUSTER_SUPER_ADMIN_PASSWORD": []byte("supersecret"),
		},
	}

	internalSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "internal-" + clusterName + "-users",
			Namespace: ns,
		},
		Data: map[string][]byte{},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(passSecret, internalSecret).Build()

	cr := &api.PerconaServerMongoDB{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clusterName,
			Namespace: ns,
		},
		Spec: api.PerconaServerMongoDBSpec{
			CRVersion:               version.Version(),
			ClusterServiceDNSMode:   api.DNSModeInternal,
			ClusterServiceDNSSuffix: "svc.cluster.local",
			TLS:                     &api.TLSSpec{Mode: api.TLSModeDisabled},
			Secrets:                 &api.SecretsSpec{},
			Sharding:                api.Sharding{Enabled: true, Mongos: &api.MongosSpec{}},
			Replsets: []*api.ReplsetSpec{
				{Name: "rs0"},
				{Name: "rs1"},
			},
			Users: []api.User{
				{
					Name: "clusterSuperAdmin",
					DB:   "admin",
					PasswordSecretRef: &api.SecretKeySelector{
						Name: clusterName + "-custom-user-secret",
						Key:  "MONGODB_CLUSTER_SUPER_ADMIN_PASSWORD",
					},
					Roles: []api.UserRole{
						{Name: "clusterAdmin", DB: "admin"},
					},
				},
			},
		},
		Status: api.PerconaServerMongoDBStatus{
			State: api.AppStateReady,
		},
	}

	// First reconcile: should connect to shards (annotations not yet present)
	tracker1 := &trackingMongoClientProvider{client: fakeClient}
	r := &ReconcilePerconaServerMongoDB{
		client:              fakeClient,
		mongoClientProvider: tracker1,
	}

	err := r.reconcileCustomUsers(ctx, cr)
	assert.NoError(t, err)
	assert.Equal(t, 1, tracker1.mongosCount, "first reconcile: expected 1 mongos connection")
	assert.ElementsMatch(t, []string{"rs0", "rs1"}, tracker1.shardCalls,
		"first reconcile: expected connections to both shards")

	// After first reconcile, the secret should have per-replset annotations.
	// Verify they exist.
	updatedSecret := &corev1.Secret{}
	err = fakeClient.Get(ctx, types.NamespacedName{Name: clusterName + "-custom-user-secret", Namespace: ns}, updatedSecret)
	assert.NoError(t, err)
	assert.Contains(t, updatedSecret.Annotations, "percona.com/"+clusterName+"-clusterSuperAdmin-rs0-hash")
	assert.Contains(t, updatedSecret.Annotations, "percona.com/"+clusterName+"-clusterSuperAdmin-rs1-hash")

	// Second reconcile: should NOT connect to shards (guard short-circuits)
	tracker2 := &trackingMongoClientProvider{client: fakeClient}
	r.mongoClientProvider = tracker2

	err = r.reconcileCustomUsers(ctx, cr)
	assert.NoError(t, err)
	assert.Equal(t, 1, tracker2.mongosCount, "second reconcile: expected 1 mongos connection")
	assert.Empty(t, tracker2.shardCalls, "second reconcile: expected zero shard connections")
}

// TestReconcileCustomUsers_PasswordRotationPropagates verifies that when a
// password changes, it propagates to every shard and updates per-replset annotations.
func TestReconcileCustomUsers_PasswordRotationPropagates(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	assert.NoError(t, corev1.AddToScheme(scheme))
	assert.NoError(t, api.SchemeBuilder.AddToScheme(scheme))

	ns := "test-ns"
	clusterName := "rotation-cluster"

	// Start with password "oldpassword" and pre-set annotations as if prior reconcile ran
	oldPass := "oldpassword"
	oldHash := sha256Hash([]byte(oldPass))

	passSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clusterName + "-custom-user-secret",
			Namespace: ns,
			Annotations: map[string]string{
				"percona.com/" + clusterName + "-clusterSuperAdmin-hash":     oldHash,
				"percona.com/" + clusterName + "-clusterSuperAdmin-rs0-hash": oldHash,
				"percona.com/" + clusterName + "-clusterSuperAdmin-rs1-hash": oldHash,
			},
		},
		Data: map[string][]byte{
			"MONGODB_CLUSTER_SUPER_ADMIN_PASSWORD": []byte("newpassword"),
		},
	}

	internalSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "internal-" + clusterName + "-users",
			Namespace: ns,
		},
		Data: map[string][]byte{},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(passSecret, internalSecret).Build()

	cr := &api.PerconaServerMongoDB{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clusterName,
			Namespace: ns,
		},
		Spec: api.PerconaServerMongoDBSpec{
			CRVersion:               version.Version(),
			ClusterServiceDNSMode:   api.DNSModeInternal,
			ClusterServiceDNSSuffix: "svc.cluster.local",
			TLS:                     &api.TLSSpec{Mode: api.TLSModeDisabled},
			Secrets:                 &api.SecretsSpec{},
			Sharding:                api.Sharding{Enabled: true, Mongos: &api.MongosSpec{}},
			Replsets: []*api.ReplsetSpec{
				{Name: "rs0"},
				{Name: "rs1"},
			},
			Users: []api.User{
				{
					Name: "clusterSuperAdmin",
					DB:   "admin",
					PasswordSecretRef: &api.SecretKeySelector{
						Name: clusterName + "-custom-user-secret",
						Key:  "MONGODB_CLUSTER_SUPER_ADMIN_PASSWORD",
					},
					Roles: []api.UserRole{
						{Name: "clusterAdmin", DB: "admin"},
					},
				},
			},
		},
		Status: api.PerconaServerMongoDBStatus{
			State: api.AppStateReady,
		},
	}

	tracker := &trackingMongoClientProvider{client: fakeClient}
	r := &ReconcilePerconaServerMongoDB{
		client:              fakeClient,
		mongoClientProvider: tracker,
	}

	err := r.reconcileCustomUsers(ctx, cr)
	assert.NoError(t, err)

	// Should have connected to both shards because password hash doesn't match
	assert.Equal(t, 1, tracker.mongosCount)
	assert.ElementsMatch(t, []string{"rs0", "rs1"}, tracker.shardCalls,
		"password rotation should trigger connections to all shards")

	// Verify annotations are updated to the new hash
	newHash := sha256Hash([]byte("newpassword"))
	updatedSecret := &corev1.Secret{}
	err = fakeClient.Get(ctx, types.NamespacedName{Name: clusterName + "-custom-user-secret", Namespace: ns}, updatedSecret)
	assert.NoError(t, err)
	assert.Equal(t, newHash, updatedSecret.Annotations["percona.com/"+clusterName+"-clusterSuperAdmin-rs0-hash"])
	assert.Equal(t, newHash, updatedSecret.Annotations["percona.com/"+clusterName+"-clusterSuperAdmin-rs1-hash"])
	assert.Equal(t, newHash, updatedSecret.Annotations[buildAnnotationKey(cr, "clusterSuperAdmin")])
}

// TestReconcileCustomUsers_PerShardSkip verifies that a shard with no committed
// annotation is processed while a sibling shard with a matching annotation is skipped.
func TestReconcileCustomUsers_PerShardSkip(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	assert.NoError(t, corev1.AddToScheme(scheme))
	assert.NoError(t, api.SchemeBuilder.AddToScheme(scheme))

	ns := "test-ns"
	clusterName := "per-shard-skip-cluster"

	passHash := sha256Hash([]byte("thepassword"))

	// Pre-set annotation for rs0 but NOT for rs1 — simulates rs1 being new/rebuilt
	passSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clusterName + "-custom-user-secret",
			Namespace: ns,
			Annotations: map[string]string{
				"percona.com/" + clusterName + "-clusterSuperAdmin-hash":     passHash,
				"percona.com/" + clusterName + "-clusterSuperAdmin-rs0-hash": passHash,
				// rs1 annotation intentionally absent
			},
		},
		Data: map[string][]byte{
			"MONGODB_CLUSTER_SUPER_ADMIN_PASSWORD": []byte("thepassword"),
		},
	}

	internalSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "internal-" + clusterName + "-users",
			Namespace: ns,
		},
		Data: map[string][]byte{},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(passSecret, internalSecret).Build()

	cr := &api.PerconaServerMongoDB{
		ObjectMeta: metav1.ObjectMeta{
			Name:      clusterName,
			Namespace: ns,
		},
		Spec: api.PerconaServerMongoDBSpec{
			CRVersion:               version.Version(),
			ClusterServiceDNSMode:   api.DNSModeInternal,
			ClusterServiceDNSSuffix: "svc.cluster.local",
			TLS:                     &api.TLSSpec{Mode: api.TLSModeDisabled},
			Secrets:                 &api.SecretsSpec{},
			Sharding:                api.Sharding{Enabled: true, Mongos: &api.MongosSpec{}},
			Replsets: []*api.ReplsetSpec{
				{Name: "rs0"},
				{Name: "rs1"},
			},
			Users: []api.User{
				{
					Name: "clusterSuperAdmin",
					DB:   "admin",
					PasswordSecretRef: &api.SecretKeySelector{
						Name: clusterName + "-custom-user-secret",
						Key:  "MONGODB_CLUSTER_SUPER_ADMIN_PASSWORD",
					},
					Roles: []api.UserRole{
						{Name: "clusterAdmin", DB: "admin"},
					},
				},
			},
		},
		Status: api.PerconaServerMongoDBStatus{
			State: api.AppStateReady,
		},
	}

	tracker := &trackingMongoClientProvider{client: fakeClient}
	r := &ReconcilePerconaServerMongoDB{
		client:              fakeClient,
		mongoClientProvider: tracker,
	}

	err := r.reconcileCustomUsers(ctx, cr)
	assert.NoError(t, err)

	// rs0 should be skipped (matching annotation), only rs1 should be connected
	assert.Equal(t, 1, tracker.mongosCount)
	assert.Equal(t, []string{"rs1"}, tracker.shardCalls,
		"only rs1 should be connected (rs0 has matching annotation)")
}

// TestBuildAnnotationKeyNoCollision ensures that two distinct users do not produce
// the same annotation key, even when their cluster/user names share a long common
// prefix that would have been truncated to an identical value by the old logic.
func TestBuildAnnotationKeyNoCollision(t *testing.T) {
	cr := &api.PerconaServerMongoDB{
		ObjectMeta: metav1.ObjectMeta{
			Name:      strings.Repeat("c", 20),
			Namespace: "namespace",
		},
		Spec: api.PerconaServerMongoDBSpec{
			CRVersion: version.Version(),
		},
	}

	longPrefix := strings.Repeat("p", 42)
	userNameA := longPrefix + strings.Repeat("a", 21)
	userNameB := longPrefix + strings.Repeat("b", 21)

	keyA := buildAnnotationKey(cr, userNameA)
	keyB := buildAnnotationKey(cr, userNameB)

	assert.NotEqual(t, keyA, keyB, "buildAnnotationKey() should produce distinct keys for distinct users, got %q for both", keyA)
}
