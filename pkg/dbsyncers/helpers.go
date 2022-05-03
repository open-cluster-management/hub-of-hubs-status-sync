package dbsyncers

import (
	"context"
	"fmt"

	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	appsv1APIGroup          = "apps.open-cluster-management.io/v1"
	clustersv1beta1APIGroup = "cluster.open-cluster-management.io/v1beta1"
	placementKind           = "Placement"
	subscriptionKind        = "Subscription"
)

func createK8sResource(ctx context.Context, k8sClient client.Client, resource client.Object) error {
	if resource == nil {
		return nil
	}

	// make sure resource version is empty - otherwise cannot create
	resource.SetResourceVersion("")

	// set annotation so that resource can be identified and deleted if needed
	annotations := resource.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}

	annotations[hubOfHubsStatusResourceAnnotation] = ""
	resource.SetAnnotations(annotations)

	if err := k8sClient.Create(ctx, resource); err != nil {
		return fmt.Errorf("failed to create k8s-resource - %w", err)
	}

	return nil
}

func setOwnerReference(resource client.Object, ownerReference *v1.OwnerReference) {
	resource.SetOwnerReferences([]v1.OwnerReference{*ownerReference})
}

func createPlacementOwnerReference(name string, uid string) *v1.OwnerReference {
	return &v1.OwnerReference{
		APIVersion:         clustersv1beta1APIGroup,
		Controller:         newTrue(),
		BlockOwnerDeletion: newTrue(),
		Kind:               placementKind,
		Name:               name,
		UID:                types.UID(uid),
	}
}

func createSubscriptionOwnerReference(name string, uid string) *v1.OwnerReference {
	return &v1.OwnerReference{
		APIVersion:         appsv1APIGroup,
		Controller:         newTrue(),
		BlockOwnerDeletion: newTrue(),
		Kind:               subscriptionKind,
		Name:               name,
		UID:                types.UID(uid),
	}
}

func newTrue() *bool {
	t := true
	return &t
}
