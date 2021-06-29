package configs

import (
	"context"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/errors"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/tilt-dev/tilt/internal/controllers/apicmp"
	"github.com/tilt-dev/tilt/internal/store"
	"github.com/tilt-dev/tilt/internal/tiltfile"
	"github.com/tilt-dev/tilt/pkg/apis"
	"github.com/tilt-dev/tilt/pkg/apis/core/v1alpha1"
)

const LabelOwnerKind = v1alpha1.LabelOwnerKind
const LabelOwnerKindTiltfile = v1alpha1.LabelOwnerKindTiltfile

var ownerSelector = labels.SelectorFromSet(labels.Set{LabelOwnerKind: LabelOwnerKindTiltfile})

type object interface {
	ctrlclient.Object
	GetSpec() interface{}
	GetGroupVersionResource() schema.GroupVersionResource
}

type objectSet map[schema.GroupVersionResource]typedObjectSet
type typedObjectSet map[string]object

// Update all the objects in the apiserver that are owned by the Tiltfile.
//
// Here we have one big API object (the Tiltfile loader) create lots of
// API objects of different types. This is not a common pattern in Kubernetes-land
// (where often each type will only own one or two other types). But it's the best way
// to model how the Tiltfile works.
//
// For that reason, this code is much more generic than owned-object creation should be.
//
// In the future, anything that creates objects based on the Tiltfile (e.g., FileWatch specs,
// LocalServer specs) should go here.
func updateOwnedObjects(ctx context.Context, client ctrlclient.Client, tlr tiltfile.TiltfileLoadResult, mode store.EngineMode) error {
	apiObjects := toAPIObjects(tlr, mode)

	// Retry until the cache has started.
	var retryCount = 0
	var existingObjects objectSet
	var err error
	for {
		existingObjects, err = getExistingAPIObjects(ctx, client)
		if err != nil {
			if _, ok := err.(*cache.ErrCacheNotStarted); ok && retryCount < 5 {
				retryCount++
				time.Sleep(200 * time.Millisecond)
				continue
			}
			return err
		}
		break
	}
	return updateObjects(ctx, client, apiObjects, existingObjects)
}

// Pulls out all the API objects generated by the Tiltfile.
func toAPIObjects(tlr tiltfile.TiltfileLoadResult, mode store.EngineMode) objectSet {
	result := objectSet{}
	result[(&v1alpha1.KubernetesApply{}).GetGroupVersionResource()] = toKubernetesApplyObjects(tlr)
	result[(&v1alpha1.ImageMap{}).GetGroupVersionResource()] = toImageMapObjects(tlr)
	result[(&v1alpha1.FileWatch{}).GetGroupVersionResource()] = ToFileWatchObjects(WatchInputs{
		Manifests:     tlr.Manifests,
		ConfigFiles:   tlr.ConfigFiles,
		Tiltignore:    tlr.Tiltignore,
		WatchSettings: tlr.WatchSettings,
		EngineMode:    mode,
	})
	return result
}

// Pulls out all the KubernetesApply objects generated by the Tiltfile.
func toKubernetesApplyObjects(tlr tiltfile.TiltfileLoadResult) typedObjectSet {
	result := typedObjectSet{}
	for _, m := range tlr.Manifests {
		if !m.IsK8s() {
			continue
		}

		kTarget := m.K8sTarget()
		name := m.Name.String()
		ka := &v1alpha1.KubernetesApply{
			ObjectMeta: metav1.ObjectMeta{
				Name: name,
				Labels: map[string]string{
					LabelOwnerKind: LabelOwnerKindTiltfile,
				},
				Annotations: map[string]string{
					v1alpha1.AnnotationManifest: name,
					v1alpha1.AnnotationSpanID:   fmt.Sprintf("kubernetesapply:%s", name),
				},
			},
			Spec: kTarget.KubernetesApplySpec,
		}
		result[name] = ka
	}
	return result
}

// Pulls out all the ImageMap objects generated by the Tiltfile.
func toImageMapObjects(tlr tiltfile.TiltfileLoadResult) typedObjectSet {
	result := typedObjectSet{}

	for _, m := range tlr.Manifests {
		for _, iTarget := range m.ImageTargets {
			name := apis.SanitizeName(iTarget.ID().Name.String())
			// Note that an ImageMap might be in more than one Manifest, so we
			// can't annotate them to a particular manifest.
			im := &v1alpha1.ImageMap{
				ObjectMeta: metav1.ObjectMeta{
					Name: name,
					Labels: map[string]string{
						LabelOwnerKind: LabelOwnerKindTiltfile,
					},
				},
				Spec: iTarget.ImageMapSpec,
			}
			result[name] = im
		}
	}
	return result
}

// Fetch all the existing API objects that were generated from the Tiltfile.
func getExistingAPIObjects(ctx context.Context, client ctrlclient.Client) (objectSet, error) {
	result := objectSet{}

	// TODO(nick): There's got to be a more generic way to do this.
	var kaList v1alpha1.KubernetesApplyList
	err := client.List(ctx, &kaList, &ctrlclient.ListOptions{LabelSelector: ownerSelector})
	if err != nil {
		return nil, err
	}

	kaMap := typedObjectSet{}
	for _, item := range kaList.Items {
		item := item
		kaMap[item.Name] = &item
	}
	result[(&v1alpha1.KubernetesApply{}).GetGroupVersionResource()] = kaMap

	var imList v1alpha1.ImageMapList
	err = client.List(ctx, &imList, &ctrlclient.ListOptions{LabelSelector: ownerSelector})
	if err != nil {
		return nil, err
	}

	imMap := typedObjectSet{}
	for _, item := range imList.Items {
		item := item
		imMap[item.Name] = &item
	}
	result[(&v1alpha1.ImageMap{}).GetGroupVersionResource()] = imMap

	var fwList v1alpha1.FileWatchList
	err = client.List(ctx, &fwList, &ctrlclient.ListOptions{LabelSelector: ownerSelector})
	if err != nil {
		return nil, err
	}

	fwMap := typedObjectSet{}
	for _, item := range fwList.Items {
		item := item
		fwMap[item.Name] = &item
	}
	result[(&v1alpha1.FileWatch{}).GetGroupVersionResource()] = fwMap

	return result, nil
}

// Reconcile the new API objects against the existing API objects.
func updateObjects(ctx context.Context, client ctrlclient.Client, newObjects, oldObjects objectSet) error {
	// TODO(nick): Does it make sense to parallelize the API calls?
	errs := []error{}

	// Upsert the new objects.
	for t, s := range newObjects {
		for name, obj := range s {
			var old object
			oldSet, ok := oldObjects[t]
			if ok {
				old = oldSet[name]
			}

			if old == nil {
				err := client.Create(ctx, obj)
				if err != nil {
					errs = append(errs, fmt.Errorf("create %s/%s: %v", obj.GetGroupVersionResource().Resource, obj.GetName(), err))
				}
				continue
			}

			// Are there other fields here we should check?
			// e.g., once labels are generated from the Tiltfile, it seems
			// we should also update the labels when they change.
			if !apicmp.DeepEqual(old.GetSpec(), obj.GetSpec()) {
				obj.SetResourceVersion(old.GetResourceVersion())
				err := client.Update(ctx, obj)
				if err != nil {
					errs = append(errs, fmt.Errorf("update %s/%s: %v", obj.GetGroupVersionResource().Resource, obj.GetName(), err))
				}
				continue
			}
		}
	}

	// Delete any objects that aren't in the new tiltfile.
	for t, s := range oldObjects {
		for name, obj := range s {
			newSet, ok := newObjects[t]
			if ok {
				_, ok := newSet[name]
				if ok {
					continue
				}
			}

			err := client.Delete(ctx, obj)
			if err != nil {
				errs = append(errs, fmt.Errorf("delete %s/%s: %v", obj.GetGroupVersionResource().Resource, obj.GetName(), err))
			}
		}
	}
	return errors.NewAggregate(errs)
}