// Copyright (c) 2021 Red Hat, Inc.
// Copyright Contributors to the Open Cluster Management project

package dbsyncers

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v4/pgxpool"
	policiesv1 "github.com/open-cluster-management/governance-policy-propagator/api/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	placementrulesv1 "open-cluster-management.io/multicloud-operators-subscription/pkg/apis/apps/placementrule/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	dbEnumCompliant    = "compliant"
	dbEnumNonCompliant = "non_compliant"

	policiesSpecTableName     = "policies"
	complianceStatusTableName = "compliance"
)

var log = ctrl.Log.WithName("policies-db-syncer")

func addPolicyDBSyncer(mgr ctrl.Manager, databaseConnectionPool *pgxpool.Pool, syncInterval time.Duration) error {
	err := mgr.Add(&genericDBSyncer{
		syncInterval: syncInterval,
		syncFunc: func(ctx context.Context) {
			syncPolicies(ctx,
				databaseConnectionPool, mgr.GetClient(),
				map[string]policiesv1.ComplianceState{
					dbEnumCompliant:    policiesv1.Compliant,
					dbEnumNonCompliant: policiesv1.NonCompliant,
				})
		},
	})
	if err != nil {
		return fmt.Errorf("failed to add policies status syncer to the manager: %w", err)
	}

	return nil
}

func syncPolicies(ctx context.Context, databaseConnectionPool *pgxpool.Pool, k8sClient client.Client,
	dbEnumToPolicyComplianceStateMap map[string]policiesv1.ComplianceState) {
	log.Info("performing sync of policies status")

	rows, err := databaseConnectionPool.Query(ctx,
		fmt.Sprintf(`SELECT id, payload->'metadata'->>'name', payload->'metadata'->>'namespace' 
		FROM spec.%s WHERE deleted = FALSE`, policiesSpecTableName))
	if err != nil {
		log.Error(err, "error in getting policies spec")
		return
	}

	for rows.Next() {
		var id, name, namespace string

		err := rows.Scan(&id, &name, &namespace)
		if err != nil {
			log.Error(err, "error in select", "table", policiesSpecTableName)
			continue
		}

		instance := &policiesv1.Policy{}

		err = k8sClient.Get(ctx, client.ObjectKey{Name: name, Namespace: namespace}, instance)
		if err != nil {
			log.Error(err, "error in getting CR", "name", name, "namespace", namespace)
			continue
		}

		go handlePolicy(ctx, databaseConnectionPool, k8sClient, dbEnumToPolicyComplianceStateMap, instance)
	}
}

func handlePolicy(ctx context.Context, databaseConnectionPool *pgxpool.Pool,
	k8sClient client.Client, dbEnumToPolicyComplianceStateMap map[string]policiesv1.ComplianceState,
	policy *policiesv1.Policy) {
	compliancePerClusterStatuses, hasNonCompliantClusters, err := getComplianceStatus(ctx, databaseConnectionPool,
		dbEnumToPolicyComplianceStateMap, policy)
	if err != nil {
		log.Error(err, "failed to get compliance status of a policy", "uid", policy.GetUID())
		return
	}

	if err = updatePoliceStatus(ctx, k8sClient, policy, compliancePerClusterStatuses,
		hasNonCompliantClusters); err != nil {
		log.Error(err, "failed to update policy status")
	}
}

// returns array of CompliancePerClusterStatus, whether the policy has any NonCompliant cluster, and error.
func getComplianceStatus(ctx context.Context, databaseConnectionPool *pgxpool.Pool,
	dbEnumToPolicyComplianceStateMap map[string]policiesv1.ComplianceState,
	policy *policiesv1.Policy) ([]*policiesv1.CompliancePerClusterStatus, bool, error) {
	rows, err := databaseConnectionPool.Query(ctx,
		fmt.Sprintf(`SELECT cluster_name,leaf_hub_name,compliance FROM status.%s
			WHERE id=$1 ORDER BY leaf_hub_name, cluster_name`, complianceStatusTableName), string(policy.GetUID()))
	if err != nil {
		return []*policiesv1.CompliancePerClusterStatus{}, false,
			fmt.Errorf("error in getting policy compliance statuses from DB - %w", err)
	}

	defer rows.Close()

	var compliancePerClusterStatuses []*policiesv1.CompliancePerClusterStatus

	hasNonCompliantClusters := false

	for rows.Next() {
		var clusterName, leafHubName, complianceInDB string

		if err := rows.Scan(&clusterName, &leafHubName, &complianceInDB); err != nil {
			return []*policiesv1.CompliancePerClusterStatus{}, false,
				fmt.Errorf("error in getting policy compliance statuses from DB - %w", err)
		}

		compliance := dbEnumToPolicyComplianceStateMap[complianceInDB]

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

func updatePoliceStatus(ctx context.Context, k8sClient client.Client, policy *policiesv1.Policy,
	compliancePerClusterStatuses []*policiesv1.CompliancePerClusterStatus,
	hasNonCompliantClusters bool) error {
	originalPolicy := policy.DeepCopy()

	policy.Status.Status = compliancePerClusterStatuses
	policy.Status.ComplianceState = ""

	if hasNonCompliantClusters {
		policy.Status.ComplianceState = policiesv1.NonCompliant
	} else if len(compliancePerClusterStatuses) > 0 {
		policy.Status.ComplianceState = policiesv1.Compliant
	}

	placements, err := getPlacements(k8sClient, policy)
	if err != nil {
		return err
	}
	policy.Status.Placement = placements

	err = k8sClient.Status().Patch(ctx, policy, client.MergeFrom(originalPolicy))
	if err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("failed to update policy CR: %w", err)
	}

	return nil
}

func getPlacements(k8sClient client.Client, instance *policiesv1.Policy) ([]*policiesv1.Placement, error) {
	log.Info("get the placements for the policy", "policy", instance.GetName())
	pbList, err := getPlacementBindingList(k8sClient, instance)
	if err != nil {
		return nil, err
	}
	var placements []*policiesv1.Placement
	for _, pb := range pbList.Items {
		subjects := pb.Subjects
		for _, subject := range subjects {
			if !(subject.APIGroup == policiesv1.SchemeGroupVersion.Group &&
				subject.Kind == policiesv1.Kind &&
				subject.Name == instance.GetName()) {
				continue
			}
			p, err := getPlacementsPerBinding(k8sClient, pb, instance)
			if err != nil {
				return nil, err
			}
			placements = append(placements, p...)
		}

	}
	return placements, nil
}

func getPlacementBindingList(k8sClient client.Client, instance *policiesv1.Policy) (*policiesv1.PlacementBindingList, error) {
	// Get the placement binding in order to later get the placements
	pbList := &policiesv1.PlacementBindingList{}

	err := k8sClient.List(context.TODO(), pbList, &client.ListOptions{Namespace: instance.GetNamespace()})
	if err != nil {
		log.Error(err, "failed to list placementbindings")
		return nil, err
	}
	return pbList, nil
}

// getPlacementsPerBinding returns the placements for the policy per placementbinding
func getPlacementsPerBinding(k8sClient client.Client, pb policiesv1.PlacementBinding,
	instance *policiesv1.Policy) ([]*policiesv1.Placement, error) {
	plr := &placementrulesv1.PlacementRule{}

	err := k8sClient.Get(context.TODO(), types.NamespacedName{
		Namespace: instance.GetNamespace(),
		Name:      pb.PlacementRef.Name,
	}, plr)
	// no error when not found
	if err != nil && !errors.IsNotFound(err) {
		return nil, fmt.Errorf("failed to get placementrule: %w", err)
	}

	var placements []*policiesv1.Placement

	plcPlacementAdded := false

	for _, subject := range pb.Subjects {
		if subject.Kind == policiesv1.Kind && subject.Name == instance.GetName() && !plcPlacementAdded {
			placement := &policiesv1.Placement{
				PlacementBinding: pb.GetName(),
				PlacementRule:    plr.GetName(),
			}
			placements = append(placements, placement)
			// should only add policy placement once in case placement binding subjects contains duplicated policies
			plcPlacementAdded = true
		}
	}

	return placements, nil
}
