// Copyright (c) 2021 Red Hat, Inc.
// Copyright Contributors to the Open Cluster Management project

package dbsyncers

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	"github.com/jackc/pgx/v4/pgxpool"
	"k8s.io/apimachinery/pkg/api/errors"
	appsv1 "open-cluster-management.io/multicloud-operators-subscription/pkg/apis/apps/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	subscriptionsSpecTableName   = "subscriptions"
	subscriptionsStatusTableName = "subscriptions"
	statusPropagatedFromLeafHubs = "status propagated from leaf-hubs"
)

func addSubscriptionDBSyncer(mgr ctrl.Manager, databaseConnectionPool *pgxpool.Pool, syncInterval time.Duration) error {
	err := mgr.Add(&genericDBSyncer{
		syncInterval: syncInterval,
		syncFunc: func(ctx context.Context) {
			syncSubscriptions(ctx,
				ctrl.Log.WithName("subscriptions-db-syncer"),
				databaseConnectionPool,
				mgr.GetClient())
		},
	})
	if err != nil {
		return fmt.Errorf("failed to add subscriptions status syncer to the manager: %w", err)
	}

	return nil
}

func syncSubscriptions(ctx context.Context, log logr.Logger, databaseConnectionPool *pgxpool.Pool,
	k8sClient client.Client) {
	log.Info("performing sync of subscription status")

	rows, err := databaseConnectionPool.Query(ctx,
		fmt.Sprintf(`SELECT id, payload->'metadata'->>'name', payload->'metadata'->>'namespace' 
		FROM spec.%s WHERE deleted = FALSE`, subscriptionsSpecTableName))
	if err != nil {
		log.Error(err, "error in getting subscriptions spec")
		return
	}

	for rows.Next() {
		var id, name, namespace string

		err := rows.Scan(&id, &name, &namespace)
		if err != nil {
			log.Error(err, "error in select", "table", subscriptionsSpecTableName)
			continue
		}

		instance := &appsv1.Subscription{}

		err = k8sClient.Get(ctx, client.ObjectKey{Name: name, Namespace: namespace}, instance)
		if err != nil {
			log.Error(err, "error in getting CR", "name", name, "namespace", namespace)
			continue
		}

		go handleSubscription(ctx, log, databaseConnectionPool, k8sClient, instance)
	}
}

func handleSubscription(ctx context.Context, log logr.Logger, databaseConnectionPool *pgxpool.Pool,
	k8sClient client.StatusClient, subscription *appsv1.Subscription) {
	log.Info("handling a subscription", "uid", subscription.GetUID())

	subscriptionStatuses, err := getSubscriptionStatus(ctx, databaseConnectionPool, subscription)
	if err != nil {
		log.Error(err, "failed to get status of a subscription", "uid", subscription.GetUID())
		return
	}

	if err = updateSubscriptionStatus(ctx, k8sClient, subscription, subscriptionStatuses); err != nil {
		log.Error(err, "failed to update subscription status")
	}
}

// returns array of CompliancePerClusterStatus and error.
func getSubscriptionStatus(ctx context.Context, databaseConnectionPool *pgxpool.Pool,
	subscription *appsv1.Subscription) (*appsv1.SubscriptionClusterStatusMap, error) {
	rows, err := databaseConnectionPool.Query(ctx,
		fmt.Sprintf(`SELECT leaf_hub_name, payload->'status'->'statuses' FROM status.%s
			WHERE payload->'metadata'->>'name'=$1 AND payload->'metadata'->>'namespace'=$2`,
			subscriptionsStatusTableName), subscription.Name, subscription.Namespace)
	if err != nil {
		return nil, fmt.Errorf("error in getting subscription statuses from DB - %w", err)
	}

	subscriptionStatuses := appsv1.SubscriptionClusterStatusMap{}

	for rows.Next() {
		var (
			leafHubName                 string
			leafHubSubscriptionStatuses appsv1.SubscriptionClusterStatusMap
		)

		if err := rows.Scan(&leafHubName, &leafHubSubscriptionStatuses); err != nil {
			return nil, fmt.Errorf("error getting subscription statuses from DB - %w", err)
		}

		// each leaf hub has only 1 unique subscription deployment (name, ns)
		for clusterName, subscriptionPerClusterStatus := range leafHubSubscriptionStatuses {
			subscriptionStatuses[fmt.Sprintf("%s-%s", leafHubName, clusterName)] = subscriptionPerClusterStatus
		}
	}

	return &subscriptionStatuses, nil
}

func updateSubscriptionStatus(ctx context.Context, k8sClient client.StatusClient, subscription *appsv1.Subscription,
	subscriptionStatuses *appsv1.SubscriptionClusterStatusMap) error {
	originalSubscription := subscription.DeepCopy()

	subscription.Status.Statuses = *subscriptionStatuses

	if len(*subscriptionStatuses) > 0 {
		subscription.Status.Phase = appsv1.SubscriptionPropagated
		subscription.Status.Reason = statusPropagatedFromLeafHubs
	}

	err := k8sClient.Status().Patch(ctx, subscription, client.MergeFrom(originalSubscription))
	if err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("failed to update subscription CR: %w", err)
	}

	return nil
}
