package perconaservermongodb

import (
	"context"
	"math"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	api "github.com/percona/percona-server-mongodb-operator/pkg/apis/psmdb/v1"
	"github.com/percona/percona-server-mongodb-operator/pkg/psmdb"
	"github.com/percona/percona-server-mongodb-operator/pkg/psmdb/mongo"
)

// oplogSizeTolerance is the allowed difference, in MB, between the live oplog
// size and the desired size. collStats reports maxSize rounded to the capped
// collection allocation, so an exact match is not guaranteed after a resize.
const oplogSizeTolerance = 1.0

// allowOplogDecrease gates the shrink path. Decreasing the oplog requires a
// compact on each member to reclaim space, which blocks replication on the
// member it runs against; the path is kept disabled by default pending review
// (LIB-1431).
const allowOplogDecrease = false

// reconcileOplogSize resizes the live oplog of each member of the replset to
// match replication.oplogSizeMB from the CR configuration. mongod only applies
// oplogSizeMB when creating the oplog, so an existing oplog.rs keeps its old
// size across restarts and must be resized with replSetResizeOplog.
//
// The reconcile is a no-op once every member already matches the desired size,
// and must only run after the set is confirmed stable (all pods live). It
// returns an error to requeue if a member becomes unhealthy mid-resize.
func (r *ReconcilePerconaServerMongoDB) reconcileOplogSize(ctx context.Context, cr *api.PerconaServerMongoDB, replset *api.ReplsetSpec, cli mongo.Client) error {
	log := logf.FromContext(ctx)

	if cr.Spec.Unmanaged {
		return nil
	}

	desired, err := replset.Configuration.GetOplogSizeMB()
	if err != nil {
		return errors.Wrap(err, "parse desired oplogSizeMB")
	}
	if desired <= 0 {
		return nil
	}
	desiredMB := float64(desired)

	status, err := cli.RSStatus(ctx)
	if err != nil {
		return errors.Wrap(err, "get replset status")
	}

	primary := status.Primary()
	if primary == nil {
		log.V(1).Info("No primary found, skipping oplog resize", "replset", replset.Name)
		return nil
	}

	pods, err := psmdb.GetRSPods(ctx, r.client, cr, replset.Name)
	if err != nil {
		return errors.Wrap(err, "get rs pods")
	}

	// Resize secondaries first, primary last. replSetResizeOplog is node-local
	// and does not replicate, so each member is resized through its own
	// standalone connection.
	var primaryPod *corev1.Pod
	for i := range pods.Items {
		pod := pods.Items[i]
		if !isMongodPod(pod) {
			continue
		}

		host, err := psmdb.MongoHost(ctx, r.client, cr, cr.Spec.ClusterServiceDNSMode, replset, replset.Expose.Enabled, pod)
		if err != nil {
			return errors.Wrapf(err, "get host for pod %s", pod.Name)
		}

		member := memberByHost(status, host)
		if member == nil {
			log.V(1).Info("Pod not found in replset status, skipping", "pod", pod.Name)
			continue
		}

		if member.State == mongo.MemberStatePrimary {
			primaryPod = &pods.Items[i]
			continue
		}

		if err := r.resizeOplogMember(ctx, cr, replset, pod, host, desiredMB); err != nil {
			return errors.Wrapf(err, "resize oplog on secondary %s", pod.Name)
		}
	}

	if primaryPod == nil {
		return nil
	}

	primaryHost, err := psmdb.MongoHost(ctx, r.client, cr, cr.Spec.ClusterServiceDNSMode, replset, replset.Expose.Enabled, *primaryPod)
	if err != nil {
		return errors.Wrapf(err, "get host for primary pod %s", primaryPod.Name)
	}

	// On a shrink the primary must be compacted too, but compact blocks
	// replication; step the primary down first so compact runs against a former
	// primary. Gated behind allowOplogDecrease (LIB-1431).
	if allowOplogDecrease && oplogShrinkNeeded(ctx, r, cr, replset, *primaryPod, primaryHost, desiredMB) {
		log.Info("Stepping down primary before oplog shrink", "replset", replset.Name, "pod", primaryPod.Name)
		if err := cli.StepDown(ctx, 60, false); err != nil {
			return errors.Wrap(err, "step down primary")
		}
	}

	if err := r.resizeOplogMember(ctx, cr, replset, *primaryPod, primaryHost, desiredMB); err != nil {
		return errors.Wrapf(err, "resize oplog on primary %s", primaryPod.Name)
	}

	return nil
}

// oplogShrinkNeeded reports whether the given member's live oplog is larger than
// the desired size and would therefore be shrunk. Errors are treated as "no
// shrink needed"; resizeOplogMember re-reads and surfaces any real failure.
func oplogShrinkNeeded(ctx context.Context, r *ReconcilePerconaServerMongoDB, cr *api.PerconaServerMongoDB, replset *api.ReplsetSpec, pod corev1.Pod, host string, desiredMB float64) bool {
	cli, err := r.standaloneClientWithRole(ctx, cr, replset, api.RoleClusterAdmin, pod)
	if err != nil {
		return false
	}
	defer func() {
		_ = cli.Disconnect(ctx)
	}()

	liveMB, err := cli.GetOplogSizeMB(ctx)
	if err != nil {
		return false
	}

	return desiredMB < liveMB-oplogSizeTolerance
}

// resizeOplogMember reads a member's live oplog size through a standalone
// connection and, if it does not match the desired size, resizes it and
// validates the result. On a shrink it also compacts oplog.rs to reclaim disk
// space, gated behind allowOplogDecrease.
func (r *ReconcilePerconaServerMongoDB) resizeOplogMember(ctx context.Context, cr *api.PerconaServerMongoDB, replset *api.ReplsetSpec, pod corev1.Pod, host string, desiredMB float64) error {
	log := logf.FromContext(ctx)

	cli, err := r.standaloneClientWithRole(ctx, cr, replset, api.RoleClusterAdmin, pod)
	if err != nil {
		return errors.Wrap(err, "get standalone mongo client")
	}
	defer func() {
		if err := cli.Disconnect(ctx); err != nil {
			log.Error(err, "failed to close standalone connection", "pod", pod.Name)
		}
	}()

	liveMB, err := cli.GetOplogSizeMB(ctx)
	if err != nil {
		return errors.Wrap(err, "get live oplog size")
	}

	if math.Abs(liveMB-desiredMB) <= oplogSizeTolerance {
		return nil
	}

	shrinking := desiredMB < liveMB
	if shrinking && !allowOplogDecrease {
		log.Info("Oplog decrease is disabled, skipping member",
			"replset", replset.Name, "pod", pod.Name, "live", liveMB, "desired", desiredMB)
		return nil
	}

	log.Info("Resizing oplog", "replset", replset.Name, "pod", pod.Name, "live", liveMB, "desired", desiredMB)

	if err := cli.ResizeOplog(ctx, desiredMB); err != nil {
		return errors.Wrap(err, "resize oplog")
	}

	if shrinking {
		if err := cli.CompactOplog(ctx); err != nil {
			return errors.Wrap(err, "compact oplog")
		}
	}

	newMB, err := cli.GetOplogSizeMB(ctx)
	if err != nil {
		return errors.Wrap(err, "validate oplog size")
	}
	if math.Abs(newMB-desiredMB) > oplogSizeTolerance {
		return errors.Errorf("oplog size validation failed for %s: got %v, want %v", pod.Name, newMB, desiredMB)
	}

	if err := r.verifyMemberHealthy(ctx, cr, replset, host); err != nil {
		return errors.Wrapf(err, "member %s unhealthy after resize", pod.Name)
	}

	return nil
}

// verifyMemberHealthy re-reads the replset status and confirms the given member
// is still up and in a primary or secondary state. It is called after a resize
// so the reconcile aborts rather than continuing to the next member when a
// mongod crashes.
func (r *ReconcilePerconaServerMongoDB) verifyMemberHealthy(ctx context.Context, cr *api.PerconaServerMongoDB, replset *api.ReplsetSpec, host string) error {
	cli, err := r.mongoClientWithRole(ctx, cr, replset, api.RoleClusterAdmin)
	if err != nil {
		return errors.Wrap(err, "get mongo client")
	}
	defer func() {
		if err := cli.Disconnect(ctx); err != nil {
			logf.FromContext(ctx).Error(err, "failed to close connection")
		}
	}()

	status, err := cli.RSStatus(ctx)
	if err != nil {
		return errors.Wrap(err, "get replset status")
	}

	member := memberByHost(status, host)
	if member == nil {
		return errors.Errorf("member %s not found in replset status", host)
	}

	if member.Health != mongo.MemberHealthUp {
		return errors.Errorf("member %s is not healthy", host)
	}

	switch member.State {
	case mongo.MemberStatePrimary, mongo.MemberStateSecondary:
		return nil
	}

	return errors.Errorf("member %s is in state %s", host, mongo.MemberStateStrings[member.State])
}

func memberByHost(status mongo.Status, host string) *mongo.Member {
	for _, member := range status.Members {
		if member.Name == host {
			return member
		}
	}
	return nil
}
