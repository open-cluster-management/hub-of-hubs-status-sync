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
)

func addSubscriptionDBSyncer(mgr ctrl.Manager, databaseConnectionPool *pgxpool.Pool, syncInterval time.Duration) error {
	err := mgr.Add(&subscriptionDBSyncer{
		client:                 mgr.GetClient(),
		log:                    ctrl.Log.WithName("subscriptions-db-syncer"),
		databaseConnectionPool: databaseConnectionPool,
		syncInterval:           syncInterval,
	})
	if err != nil {
		return fmt.Errorf("failed to add subscriptions status syncer to the manager: %w", err)
	}

	return nil
}

type subscriptionDBSyncer struct {
	client                 client.Client
	log                    logr.Logger
	databaseConnectionPool *pgxpool.Pool
	syncInterval           time.Duration
}

func (syncer *subscriptionDBSyncer) Start(ctx context.Context) error {
	ctxWithCancel, cancelContext := context.WithCancel(ctx)
	defer cancelContext()

	go syncer.periodicSync(ctxWithCancel)

	<-ctx.Done() // blocking wait for stop event
	syncer.log.Info("stop performing sync")

	return nil // context cancel is called before exiting this function
}

func (syncer *subscriptionDBSyncer) periodicSync(ctx context.Context) {
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

func (syncer *subscriptionDBSyncer) sync(ctx context.Context) {
	syncer.log.Info("performing sync of subscription status")

	rows, err := syncer.databaseConnectionPool.Query(ctx,
		fmt.Sprintf(`SELECT id, payload->'metadata'->>'name', payload->'metadata'->>'namespace' 
		FROM spec.%s WHERE deleted = FALSE`, subscriptionsSpecTableName))
	if err != nil {
		syncer.log.Error(err, "error in getting subscriptions spec")
		return
	}

	for rows.Next() {
		var id, name, namespace string

		err := rows.Scan(&id, &name, &namespace)
		if err != nil {
			syncer.log.Error(err, "error in select", "table", subscriptionsSpecTableName)
			continue
		}

		instance := &appsv1.Subscription{}
		err = syncer.client.Get(ctx, client.ObjectKey{Name: name, Namespace: namespace}, instance)

		if err != nil {
			syncer.log.Error(err, "error in getting CR", "name", name, "namespace", namespace)
			continue
		}

		go syncer.handleSubscription(ctx, instance)
	}
}

func (syncer *subscriptionDBSyncer) handleSubscription(ctx context.Context, subscription *appsv1.Subscription) {
	syncer.log.Info("handling a subscription", "uid", subscription.GetUID())

	subscriptionStatuses, err := syncer.getSubscriptionStatus(ctx, subscription)
	if err != nil {
		syncer.log.Error(err, "failed to get status of a subscription", "uid", subscription.GetUID())
		return
	}

	if err = syncer.updateSubscriptionStatus(ctx, subscription, subscriptionStatuses); err != nil {
		syncer.log.Error(err, "failed to update subscription status")
	}
}

// returns array of CompliancePerClusterStatus and error.
func (syncer *subscriptionDBSyncer) getSubscriptionStatus(ctx context.Context,
	subscription *appsv1.Subscription) (*appsv1.SubscriptionClusterStatusMap, error) {
	rows, err := syncer.databaseConnectionPool.Query(ctx,
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
			subscriptionStatuses[fmt.Sprintf("%s.%s", leafHubName, clusterName)] = subscriptionPerClusterStatus
		}
	}

	return &subscriptionStatuses, nil
}

func (syncer *subscriptionDBSyncer) updateSubscriptionStatus(ctx context.Context, subscription *appsv1.Subscription,
	subscriptionStatuses *appsv1.SubscriptionClusterStatusMap) error {
	originalSubscription := subscription.DeepCopy()

	subscription.Status.Statuses = *subscriptionStatuses

	err := syncer.client.Status().Patch(ctx, subscription, client.MergeFrom(originalSubscription))
	if err != nil && !errors.IsNotFound(err) {
		return fmt.Errorf("failed to update subscription CR: %w", err)
	}

	return nil
}
