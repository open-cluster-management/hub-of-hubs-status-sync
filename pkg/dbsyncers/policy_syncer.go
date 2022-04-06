// Copyright (c) 2021 Red Hat, Inc.
// Copyright Contributors to the Open Cluster Management project

package dbsyncers

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	"github.com/jackc/pgx/v4/pgxpool"
	policiesv1 "github.com/open-cluster-management/governance-policy-propagator/api/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	dbEnumCompliant    = "compliant"
	dbEnumNonCompliant = "non_compliant"

	policiesSpecTableName     = "policies"
	complianceStatusTableName = "compliance"
	placementStatusTableName  = "policies_placement"
)

func addPolicyDBSyncer(mgr ctrl.Manager, databaseConnectionPool *pgxpool.Pool, syncInterval time.Duration) error {
	err := mgr.Add(&policyDBSyncer{
		client:                 mgr.GetClient(),
		log:                    ctrl.Log.WithName("policies-db-syncer"),
		databaseConnectionPool: databaseConnectionPool,
		syncInterval:           syncInterval,
		dbEnumToPolicyComplianceStateMap: map[string]policiesv1.ComplianceState{
			dbEnumCompliant:    policiesv1.Compliant,
			dbEnumNonCompliant: policiesv1.NonCompliant,
		},
	})
	if err != nil {
		return fmt.Errorf("failed to add policies status syncer to the manager: %w", err)
	}

	return nil
}

type policyDBSyncer struct {
	client                           client.Client
	log                              logr.Logger
	databaseConnectionPool           *pgxpool.Pool
	syncInterval                     time.Duration
	dbEnumToPolicyComplianceStateMap map[string]policiesv1.ComplianceState
}

func (syncer *policyDBSyncer) Start(ctx context.Context) error {
	ctxWithCancel, cancelContext := context.WithCancel(ctx)
	defer cancelContext()

	go syncer.periodicSync(ctxWithCancel)

	<-ctx.Done() // blocking wait for stop event
	syncer.log.Info("stop performing sync")

	return nil // context cancel is called before exiting this function
}

func (syncer *policyDBSyncer) periodicSync(ctx context.Context) {
	ticker := time.NewTicker(syncer.syncInterval)

	var (
		cancelFunc     context.CancelFunc
		ctxWithTimeout context.Context
	)

	for {
		select {
		case <-ctx.Done(): // we have received a signal to stop
			ticker.Stop()

			if cancelFunc != nil {
				cancelFunc()
			}

			return

		case <-ticker.C:
			// cancel the operation of the previous tick
			if cancelFunc != nil {
				cancelFunc()
			}

			ctxWithTimeout, cancelFunc = context.WithTimeout(ctx, syncer.syncInterval)

			syncer.sync(ctxWithTimeout)
		}
	}
}

func (syncer *policyDBSyncer) sync(ctx context.Context) {
	syncer.log.Info("performing sync of policies status")

	rows, err := syncer.databaseConnectionPool.Query(ctx,
		fmt.Sprintf(`SELECT id, payload->'metadata'->>'name', payload->'metadata'->>'namespace' 
		FROM spec.%s WHERE deleted = FALSE`, policiesSpecTableName))
	if err != nil {
		syncer.log.Error(err, "error in getting policies spec")
		return
	}

	for rows.Next() {
		var id, name, namespace string

		err := rows.Scan(&id, &name, &namespace)
		if err != nil {
			syncer.log.Error(err, "error in select", "table", policiesSpecTableName)
			continue
		}

		instance := &policiesv1.Policy{}
		err = syncer.client.Get(ctx, client.ObjectKey{Name: name, Namespace: namespace}, instance)

		if err != nil {
			syncer.log.Error(err, "error in getting CR", "name", name, "namespace", namespace)
			continue
		}

		go syncer.handlePolicy(ctx, instance)
	}
}

func (syncer *policyDBSyncer) handlePolicy(ctx context.Context, policy *policiesv1.Policy) {
	syncer.log.Info("handling a policy", "uid", policy.GetUID())

	compliancePerClusterStatuses, hasNonCompliantClusters, err := syncer.getComplianceStatus(ctx, policy)
	if err != nil {
		syncer.log.Error(err, "failed to get compliance status of a policy", "uid", policy.GetUID())
		return
	}

	placementStatus, err := syncer.getPlacementStatus(ctx, policy)
	if err != nil {
		syncer.log.Error(err, "failed to get placement status of a policy", "uid", policy.GetUID())
		return
	}

	if err = syncer.updateComplianceStatus(ctx, policy, compliancePerClusterStatuses,
		hasNonCompliantClusters, placementStatus); err != nil {
		syncer.log.Error(err, "Failed to update policy status")
	}
}

// returns array of CompliancePerClusterStatus, whether the policy has any NonCompliant cluster, and error.
func (syncer *policyDBSyncer) getComplianceStatus(ctx context.Context,
	policy *policiesv1.Policy) ([]*policiesv1.CompliancePerClusterStatus, bool, error) {
	rows, err := syncer.databaseConnectionPool.Query(ctx,
		fmt.Sprintf(`SELECT cluster_name,leaf_hub_name,compliance FROM status.%s
			WHERE id=$1 ORDER BY leaf_hub_name, cluster_name`, complianceStatusTableName), string(policy.GetUID()))
	if err != nil {
		return []*policiesv1.CompliancePerClusterStatus{}, false,
			fmt.Errorf("error in getting policy compliance statuses from DB - %w", err)
	}

	var compliancePerClusterStatuses []*policiesv1.CompliancePerClusterStatus

	hasNonCompliantClusters := false

	for rows.Next() {
		var clusterName, leafHubName, complianceInDB string

		if err := rows.Scan(&clusterName, &leafHubName, &complianceInDB); err != nil {
			return []*policiesv1.CompliancePerClusterStatus{}, false,
				fmt.Errorf("error in getting policy compliance statuses from DB - %w", err)
		}

		compliance := syncer.dbEnumToPolicyComplianceStateMap[complianceInDB]

		if compliance == policiesv1.NonCompliant {
			hasNonCompliantClusters = true
		}

		compliancePerClusterStatuses = append(compliancePerClusterStatuses, &policiesv1.CompliancePerClusterStatus{
			ComplianceState:  compliance,
			ClusterName:      clusterName,
			ClusterNamespace: clusterName,
		})
	}

	return compliancePerClusterStatuses, hasNonCompliantClusters, nil
}

func (syncer *policyDBSyncer) getPlacementStatus(ctx context.Context,
	policy *policiesv1.Policy) ([]*policiesv1.Placement, error) {
	var placement []*policiesv1.Placement

	if err := syncer.databaseConnectionPool.QueryRow(ctx, fmt.Sprintf(`SELECT placement FROM status.%s
			WHERE id=$1`, placementStatusTableName), string(policy.GetUID())).Scan(&placement); err != nil {
		return []*policiesv1.Placement{}, fmt.Errorf("failed to read placement from database: %w", err)
	}

	return placement, nil
}

func (syncer *policyDBSyncer) updateComplianceStatus(ctx context.Context, policy *policiesv1.Policy,
	compliancePerClusterStatuses []*policiesv1.CompliancePerClusterStatus, hasNonCompliantClusters bool,
	placementStatus []*policiesv1.Placement) error {
	originalPolicy := policy.DeepCopy()

	policy.Status.Status = compliancePerClusterStatuses
	policy.Status.ComplianceState = ""

	if hasNonCompliantClusters {
		policy.Status.ComplianceState = policiesv1.NonCompliant
	} else if len(compliancePerClusterStatuses) > 0 {
		policy.Status.ComplianceState = policiesv1.Compliant
	}

	policy.Status.Placement = placementStatus

	err := syncer.client.Status().Patch(ctx, policy, client.MergeFrom(originalPolicy))
	if err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("failed to update policy CR: %w", err)
	}

	return nil
}
