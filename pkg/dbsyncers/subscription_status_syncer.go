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
	appsv1alpha1 "open-cluster-management.io/multicloud-operators-subscription/pkg/apis/apps/v1alpha1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	subscriptionsSpecTableName    = "subscriptions"
	subscriptionStatusesTableName = "subscription_statuses"
)

func addSubscriptionStatusDBSyncer(mgr ctrl.Manager, databaseConnectionPool *pgxpool.Pool,
	syncInterval time.Duration) error {
	err := mgr.Add(&genericDBSyncer{
		syncInterval: syncInterval,
		syncFunc: func(ctx context.Context) {
			syncSubscriptionStatuses(ctx,
				ctrl.Log.WithName("subscription-statuses-db-syncer"),
				databaseConnectionPool,
				mgr.GetClient())
		},
	})
	if err != nil {
		return fmt.Errorf("failed to add subscription statuses syncer to the manager: %w", err)
	}

	return nil
}

func syncSubscriptionStatuses(ctx context.Context, log logr.Logger, databaseConnectionPool *pgxpool.Pool,
	k8sClient client.Client) {
	log.Info("performing sync of subscription-status")

	rows, err := databaseConnectionPool.Query(ctx,
		fmt.Sprintf(`SELECT payload->'metadata'->>'name', payload->'metadata'->>'namespace' 
		FROM spec.%s WHERE deleted = FALSE`, subscriptionsSpecTableName))
	if err != nil {
		log.Error(err, "error in getting subscriptions spec")
		return
	}

	for rows.Next() {
		var name, namespace string

		err := rows.Scan(&name, &namespace)
		if err != nil {
			log.Error(err, "error in select", "table", subscriptionsSpecTableName)
			continue
		}

		go handleSubscriptionStatus(ctx, log, databaseConnectionPool, k8sClient, name, namespace)
	}
}

func handleSubscriptionStatus(ctx context.Context, log logr.Logger, databaseConnectionPool *pgxpool.Pool,
	k8sClient client.Client, subscriptionName string, subscriptionNamespace string) {
	log.Info("handling a subscription", "name", subscriptionName, "namespace", subscriptionNamespace)

	subscriptionStatus, err := getAggregatedSubscriptionStatuses(ctx, databaseConnectionPool, subscriptionName,
		subscriptionNamespace)
	if err != nil {
		log.Error(err, "failed to get aggregated subscription-status", "name", subscriptionName,
			"namespace", subscriptionNamespace)

		return
	}

	if err := updateSubscriptionStatus(ctx, k8sClient, subscriptionName, subscriptionNamespace,
		subscriptionStatus); err != nil {
		log.Error(err, "failed to update subscription-status status")
	}
}

// returns aggregated SubscriptionStatus.statuses and error.
func getAggregatedSubscriptionStatuses(ctx context.Context, databaseConnectionPool *pgxpool.Pool,
	subscriptionName string, subscriptionNamespace string) (*appsv1alpha1.SubscriptionStatus, error) {
	rows, err := databaseConnectionPool.Query(ctx,
		fmt.Sprintf(`SELECT payload->'statuses' FROM status.%s
			WHERE payload->'metadata'->>'name'=$1 AND payload->'metadata'->>'namespace'=$2`,
			subscriptionStatusesTableName), subscriptionName, subscriptionNamespace)
	if err != nil {
		return nil, fmt.Errorf("error in getting subscription-statuses from DB - %w", err)
	}

	defer rows.Close()

	subscriptionStatus := appsv1alpha1.SubscriptionStatus{}

	for rows.Next() {
		var leafHubSubscriptionStatuses appsv1alpha1.SubscriptionClusterStatusMap

		if err := rows.Scan(&leafHubSubscriptionStatuses); err != nil {
			return nil, fmt.Errorf("error getting subscription-status from DB - %w", err)
		}

		// assuming that cluster names are unique across the hubs, all we need to do is a complete merge
		subscriptionStatus.Statuses.SubscriptionStatus = append(subscriptionStatus.Statuses.SubscriptionStatus,
			leafHubSubscriptionStatuses.SubscriptionStatus...)
	}

	return &subscriptionStatus, nil
}

//nolint
func updateSubscriptionStatus(ctx context.Context, k8sClient client.Client, subscriptionName string,
	subscriptionNamespace string, subscriptionStatus *appsv1alpha1.SubscriptionStatus) error {
	originalSubscriptionStatus := &appsv1alpha1.SubscriptionStatus{}

	err := k8sClient.Get(ctx, client.ObjectKey{Name: subscriptionName, Namespace: subscriptionNamespace},
		originalSubscriptionStatus)
	if err != nil {
		if errors.IsNotFound(err) { // create CR
			// assign names
			subscriptionStatus.Name = subscriptionName
			subscriptionStatus.Namespace = subscriptionNamespace
			// assign labels
			subscriptionStatus.Labels[appsv1.AnnotationHosting] = fmt.Sprintf("%s.%s",
				subscriptionNamespace, subscriptionName)

			if err := k8sClient.Create(ctx, subscriptionStatus); err != nil {
				return fmt.Errorf("failed to create subscription-status {name=%s, namespace=%s} - %w",
					subscriptionName, subscriptionNamespace, err)
			}
		}
	}

	err = k8sClient.Status().Patch(ctx, subscriptionStatus, client.MergeFrom(originalSubscriptionStatus))
	if err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("failed to update subscription-status CR (name=%s, namespace=%s): %w", subscriptionName,
			subscriptionNamespace, err)
	}

	return nil
}
