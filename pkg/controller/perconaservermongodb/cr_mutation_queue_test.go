package perconaservermongodb

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/percona/percona-server-mongodb-operator/pkg/apis"
	api "github.com/percona/percona-server-mongodb-operator/pkg/apis/psmdb/v1"
	"github.com/percona/percona-server-mongodb-operator/pkg/naming"
	"github.com/percona/percona-server-mongodb-operator/pkg/psmdb/config"
)

// newTestScheme returns an isolated runtime.Scheme with all required types
// registered. Each test gets its own scheme to avoid races on the global
// scheme.Scheme when tests run in parallel.
func newTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(s))
	require.NoError(t, appsv1.AddToScheme(s))
	require.NoError(t, apis.AddToScheme(s))
	return s
}

func TestCRMutationQueueBasic(t *testing.T) {
	t.Parallel()

	t.Run("empty queue drains without error", func(t *testing.T) {
		q := &crMutationQueue{}
		require.NoError(t, q.Drain(context.Background()))
	})

	t.Run("enqueue and drain in sorted order", func(t *testing.T) {
		q := &crMutationQueue{}
		var order []string

		q.Enqueue(crMutation{name: "c/op", apply: func(_ context.Context) error {
			order = append(order, "c")
			return nil
		}})
		q.Enqueue(crMutation{name: "a/op", apply: func(_ context.Context) error {
			order = append(order, "a")
			return nil
		}})
		q.Enqueue(crMutation{name: "b/op", apply: func(_ context.Context) error {
			order = append(order, "b")
			return nil
		}})

		require.NoError(t, q.Drain(context.Background()))
		assert.Equal(t, []string{"a", "b", "c"}, order)
	})

	t.Run("drain joins errors", func(t *testing.T) {
		q := &crMutationQueue{}
		q.Enqueue(crMutation{name: "a", apply: func(_ context.Context) error {
			return errors.New("err-a")
		}})
		q.Enqueue(crMutation{name: "b", apply: func(_ context.Context) error {
			return nil
		}})
		q.Enqueue(crMutation{name: "c", apply: func(_ context.Context) error {
			return errors.New("err-c")
		}})

		err := q.Drain(context.Background())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "err-a")
		assert.Contains(t, err.Error(), "err-c")
	})

	t.Run("concurrent enqueue is safe", func(t *testing.T) {
		q := &crMutationQueue{}
		var wg sync.WaitGroup
		for i := range 100 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				q.Enqueue(crMutation{
					name:  fmt.Sprintf("op/%03d", i),
					apply: func(_ context.Context) error { return nil },
				})
			}()
		}
		wg.Wait()

		require.NoError(t, q.Drain(context.Background()))
		// Queue is now empty.
		require.NoError(t, q.Drain(context.Background()))
	})

	t.Run("context helpers round-trip", func(t *testing.T) {
		// No queue present.
		assert.Nil(t, crMutationQueueFrom(context.Background()))

		q := &crMutationQueue{}
		ctx := withCRMutationQueue(context.Background(), q)
		assert.Same(t, q, crMutationQueueFrom(ctx))
	})
}

// TestTriggerResizeConcurrentRace exercises the triggerResize path in a
// concurrent replset reconciliation scenario. Two replsets trigger autoscaling
// resize simultaneously. Under -race, this will fail if the CR mutation escapes
// the drain phase.
func TestTriggerResizeConcurrentRace(t *testing.T) {
	t.Parallel()

	s := newTestScheme(t)

	cr := &api.PerconaServerMongoDB{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "race-test",
			Namespace: "default",
		},
		Spec: api.PerconaServerMongoDBSpec{
			Replsets: []*api.ReplsetSpec{
				{
					Name: "rs0",
					Size: 3,
					VolumeSpec: &api.VolumeSpec{
						PersistentVolumeClaim: api.PVCSpec{
							PersistentVolumeClaimSpec: &corev1.PersistentVolumeClaimSpec{
								Resources: corev1.VolumeResourceRequirements{
									Requests: corev1.ResourceList{
										corev1.ResourceStorage: resource.MustParse("10Gi"),
									},
								},
							},
						},
					},
				},
				{
					Name: "rs1",
					Size: 3,
					VolumeSpec: &api.VolumeSpec{
						PersistentVolumeClaim: api.PVCSpec{
							PersistentVolumeClaimSpec: &corev1.PersistentVolumeClaimSpec{
								Resources: corev1.VolumeResourceRequirements{
									Requests: corev1.ResourceList{
										corev1.ResourceStorage: resource.MustParse("10Gi"),
									},
								},
							},
						},
					},
				},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(cr).
		Build()

	r := &ReconcilePerconaServerMongoDB{
		client: fakeClient,
	}

	// Set up the mutation queue as reconcileReplsetSpecs does.
	mq := &crMutationQueue{}
	ctx := withCRMutationQueue(context.Background(), mq)

	// Simulate two concurrent workers calling triggerResize.
	var wg sync.WaitGroup
	for i, rs := range cr.Spec.Replsets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			pvc := &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("%s-race-test-%s-0", config.MongodDataVolClaimName, rs.Name),
					Namespace: "default",
				},
				Status: corev1.PersistentVolumeClaimStatus{
					Capacity: corev1.ResourceList{
						corev1.ResourceStorage: resource.MustParse("10Gi"),
					},
				},
			}
			newSize := resource.MustParse(fmt.Sprintf("%dGi", 12+i*3))
			_ = r.triggerResize(ctx, cr, pvc, newSize, rs.VolumeSpec)
		}()
	}
	wg.Wait()

	// Before drain, the CR spec is unchanged (mutations are deferred).
	assert.Equal(t, resource.MustParse("10Gi"),
		cr.Spec.Replsets[0].VolumeSpec.PersistentVolumeClaim.Resources.Requests[corev1.ResourceStorage])
	assert.Equal(t, resource.MustParse("10Gi"),
		cr.Spec.Replsets[1].VolumeSpec.PersistentVolumeClaim.Resources.Requests[corev1.ResourceStorage])

	// Drain applies the mutations serially.
	require.NoError(t, mq.Drain(context.Background()))

	// After drain, both replsets have their new sizes.
	rs0Size := cr.Spec.Replsets[0].VolumeSpec.PersistentVolumeClaim.Resources.Requests[corev1.ResourceStorage]
	rs1Size := cr.Spec.Replsets[1].VolumeSpec.PersistentVolumeClaim.Resources.Requests[corev1.ResourceStorage]
	expected0 := resource.MustParse("12Gi")
	expected1 := resource.MustParse("15Gi")
	assert.Equal(t, expected0.Value(), rs0Size.Value())
	assert.Equal(t, expected1.Value(), rs1Size.Value())
}

// TestHandlePVCResizeFailureConcurrentRace exercises the PVC resize failure
// path concurrently with another replset's reconciliation. Under -race, this
// will detect if the revertVolumeTemplate write escapes the drain phase.
func TestHandlePVCResizeFailureConcurrentRace(t *testing.T) {
	t.Parallel()

	s := newTestScheme(t)

	cr := &api.PerconaServerMongoDB{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "revert-test",
			Namespace: "default",
		},
		Spec: api.PerconaServerMongoDBSpec{
			Replsets: []*api.ReplsetSpec{
				{
					Name: "rs0",
					Size: 3,
					VolumeSpec: &api.VolumeSpec{
						PersistentVolumeClaim: api.PVCSpec{
							PersistentVolumeClaimSpec: &corev1.PersistentVolumeClaimSpec{
								Resources: corev1.VolumeResourceRequirements{
									Requests: corev1.ResourceList{
										corev1.ResourceStorage: resource.MustParse("15Gi"),
									},
								},
							},
						},
					},
				},
				{
					Name: "rs1",
					Size: 3,
					VolumeSpec: &api.VolumeSpec{
						PersistentVolumeClaim: api.PVCSpec{
							PersistentVolumeClaimSpec: &corev1.PersistentVolumeClaimSpec{
								Resources: corev1.VolumeResourceRequirements{
									Requests: corev1.ResourceList{
										corev1.ResourceStorage: resource.MustParse("20Gi"),
									},
								},
							},
						},
					},
				},
			},
		},
	}

	// Create StatefulSets with proper labels.
	sts0 := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "revert-test-rs0",
			Namespace: "default",
			Labels: map[string]string{
				naming.LabelKubernetesReplset: "rs0",
			},
			Annotations: map[string]string{
				api.AnnotationPVCResizeInProgress: "2025-01-01T00:00:00Z",
			},
		},
	}
	sts1 := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "revert-test-rs1",
			Namespace: "default",
			Labels: map[string]string{
				naming.LabelKubernetesReplset: "rs1",
			},
			Annotations: map[string]string{
				api.AnnotationPVCResizeInProgress: "2025-01-01T00:00:00Z",
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(cr, sts0, sts1).
		Build()

	r := &ReconcilePerconaServerMongoDB{
		client: fakeClient,
	}

	// Set up the mutation queue.
	mq := &crMutationQueue{}
	ctx := withCRMutationQueue(context.Background(), mq)

	// Simulate two concurrent workers hitting PVC resize failures.
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_ = r.handlePVCResizeFailure(ctx, cr, sts0, resource.MustParse("10Gi"))
	}()
	go func() {
		defer wg.Done()
		_ = r.handlePVCResizeFailure(ctx, cr, sts1, resource.MustParse("10Gi"))
	}()
	wg.Wait()

	// Before drain: CR sizes should be unchanged.
	assert.Equal(t, resource.MustParse("15Gi"),
		cr.Spec.Replsets[0].VolumeSpec.PersistentVolumeClaim.Resources.Requests[corev1.ResourceStorage])
	assert.Equal(t, resource.MustParse("20Gi"),
		cr.Spec.Replsets[1].VolumeSpec.PersistentVolumeClaim.Resources.Requests[corev1.ResourceStorage])

	// Drain.
	require.NoError(t, mq.Drain(context.Background()))

	// After drain: both reverted to 10Gi.
	rs0Size := cr.Spec.Replsets[0].VolumeSpec.PersistentVolumeClaim.Resources.Requests[corev1.ResourceStorage]
	rs1Size := cr.Spec.Replsets[1].VolumeSpec.PersistentVolumeClaim.Resources.Requests[corev1.ResourceStorage]
	expectedRevert := resource.MustParse("10Gi")
	assert.Equal(t, expectedRevert.Value(), rs0Size.Value())
	assert.Equal(t, expectedRevert.Value(), rs1Size.Value())
}

// TestUpdateAutoscalingStatusConcurrentRace exercises updateAutoscalingStatus
// called concurrently from multiple replset workers. Under -race, this ensures
// the status map write is properly deferred.
func TestUpdateAutoscalingStatusConcurrentRace(t *testing.T) {
	t.Parallel()

	cr := &api.PerconaServerMongoDB{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "status-test",
			Namespace: "default",
		},
		Status: api.PerconaServerMongoDBStatus{},
	}

	r := &ReconcilePerconaServerMongoDB{}

	mq := &crMutationQueue{}
	ctx := withCRMutationQueue(context.Background(), mq)

	// Simulate two concurrent workers updating autoscaling status.
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		r.updateAutoscalingStatus(ctx, cr, "mongod-data-test-rs0-0", &PVCUsage{
			TotalBytes:   10 * 1024 * 1024 * 1024,
			UsagePercent: 85,
		}, nil)
	}()
	go func() {
		defer wg.Done()
		r.updateAutoscalingStatus(ctx, cr, "mongod-data-test-rs1-0", &PVCUsage{
			TotalBytes:   20 * 1024 * 1024 * 1024,
			UsagePercent: 90,
		}, nil)
	}()
	wg.Wait()

	// Before drain: status map should be nil (mutations deferred).
	assert.Nil(t, cr.Status.StorageAutoscaling)

	// Drain.
	require.NoError(t, mq.Drain(context.Background()))

	// After drain: both entries present with expected values.
	require.NotNil(t, cr.Status.StorageAutoscaling)
	assert.Equal(t, "10Gi", cr.Status.StorageAutoscaling["mongod-data-test-rs0-0"].CurrentSize)
	assert.Equal(t, "20Gi", cr.Status.StorageAutoscaling["mongod-data-test-rs1-0"].CurrentSize)
}

// TestTriggerResizeInlineWithoutQueue verifies that triggerResize applies
// immediately when no mutation queue is in the context (backward compatibility
// with non-pool callers).
func TestTriggerResizeInlineWithoutQueue(t *testing.T) {
	t.Parallel()

	s := newTestScheme(t)

	cr := &api.PerconaServerMongoDB{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "inline-test",
			Namespace: "default",
		},
		Spec: api.PerconaServerMongoDBSpec{
			Replsets: []*api.ReplsetSpec{
				{
					Name: "rs0",
					VolumeSpec: &api.VolumeSpec{
						PersistentVolumeClaim: api.PVCSpec{
							PersistentVolumeClaimSpec: &corev1.PersistentVolumeClaimSpec{
								Resources: corev1.VolumeResourceRequirements{
									Requests: corev1.ResourceList{
										corev1.ResourceStorage: resource.MustParse("10Gi"),
									},
								},
							},
						},
					},
				},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(cr).
		Build()

	r := &ReconcilePerconaServerMongoDB{client: fakeClient}

	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "mongod-data-inline-test-rs0-0",
			Namespace: "default",
		},
		Status: corev1.PersistentVolumeClaimStatus{
			Capacity: corev1.ResourceList{
				corev1.ResourceStorage: resource.MustParse("10Gi"),
			},
		},
	}

	// No queue in context — should apply immediately.
	err := r.triggerResize(context.Background(), cr, pvc, resource.MustParse("15Gi"), cr.Spec.Replsets[0].VolumeSpec)
	require.NoError(t, err)

	// Verify inline application.
	expectedInline := resource.MustParse("15Gi")
	gotInline := cr.Spec.Replsets[0].VolumeSpec.PersistentVolumeClaim.Resources.Requests[corev1.ResourceStorage]
	assert.Equal(t, expectedInline.Value(), gotInline.Value())
}

// TestDrainDeterminism ensures the mutation queue produces deterministic results
// regardless of enqueue order.
func TestDrainDeterminism(t *testing.T) {
	t.Parallel()

	// Run 50 times to exercise different goroutine scheduling.
	for attempt := range 50 {
		t.Run(fmt.Sprintf("attempt_%d", attempt), func(t *testing.T) {
			cr := &api.PerconaServerMongoDB{
				Status: api.PerconaServerMongoDBStatus{},
			}

			r := &ReconcilePerconaServerMongoDB{}
			mq := &crMutationQueue{}
			ctx := withCRMutationQueue(context.Background(), mq)

			// Enqueue from goroutines (simulating concurrent workers).
			var wg sync.WaitGroup
			for i := range 5 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					pvcName := fmt.Sprintf("mongod-data-test-rs%d-0", i)
					r.updateAutoscalingStatus(ctx, cr, pvcName, &PVCUsage{
						TotalBytes:   int64((i + 1) * 10 * 1024 * 1024 * 1024),
						UsagePercent: 80 + i,
					}, nil)
				}()
			}
			wg.Wait()

			require.NoError(t, mq.Drain(context.Background()))

			// Verify deterministic final state.
			require.NotNil(t, cr.Status.StorageAutoscaling)
			assert.Len(t, cr.Status.StorageAutoscaling, 5)
			assert.Equal(t, "10Gi", cr.Status.StorageAutoscaling["mongod-data-test-rs0-0"].CurrentSize)
			assert.Equal(t, "20Gi", cr.Status.StorageAutoscaling["mongod-data-test-rs1-0"].CurrentSize)
			assert.Equal(t, "30Gi", cr.Status.StorageAutoscaling["mongod-data-test-rs2-0"].CurrentSize)
			assert.Equal(t, "40Gi", cr.Status.StorageAutoscaling["mongod-data-test-rs3-0"].CurrentSize)
			assert.Equal(t, "50Gi", cr.Status.StorageAutoscaling["mongod-data-test-rs4-0"].CurrentSize)
		})
	}
}
