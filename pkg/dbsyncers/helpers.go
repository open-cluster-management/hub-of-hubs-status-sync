package dbsyncers

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	hubOfHubsStatusResourceAnnotation = "hub-of-hubs.open-cluster-management.io/hubOfHubsStatusResource"
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

func cleanK8sResource(ctx context.Context, k8sClient client.Client, resourceHolder client.Object, resourceName string,
	resourceNamespace string) error {
	err := k8sClient.Get(ctx, client.ObjectKey{
		Name:      resourceName,
		Namespace: resourceNamespace,
	},
		resourceHolder)
	if err != nil {
		if errors.IsNotFound(err) {
			return nil
		}

		return fmt.Errorf("failed to get k8s-resource - %w", err)
	}

	// if resource was created by this controller then it should be deleted
	if _, found := resourceHolder.GetAnnotations()[hubOfHubsStatusResourceAnnotation]; found {
		if err := k8sClient.Delete(ctx, resourceHolder); err != nil {
			return fmt.Errorf("failed to delete k8s-resource - %w", err)
		}
	}

	return nil
}
