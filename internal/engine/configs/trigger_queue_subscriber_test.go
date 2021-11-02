package configs

import (
	"context"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"

	"github.com/tilt-dev/tilt/internal/controllers/apis/configmap"
	"github.com/tilt-dev/tilt/internal/controllers/fake"
	"github.com/tilt-dev/tilt/internal/store"
	"github.com/tilt-dev/tilt/pkg/apis/core/v1alpha1"
	"github.com/tilt-dev/tilt/pkg/model"
)

func TestTriggerQueue(t *testing.T) {
	st := store.NewTestingStore()
	st.WithState(func(s *store.EngineState) {
		s.UpsertManifestTarget(store.NewManifestTarget(model.Manifest{Name: "a"}))
		s.UpsertManifestTarget(store.NewManifestTarget(model.Manifest{Name: "b"}))
		s.UpsertManifestTarget(store.NewManifestTarget(model.Manifest{Name: "c"}))
		s.AppendToTriggerQueue("a", model.BuildReasonFlagTriggerCLI)
		s.AppendToTriggerQueue("b", model.BuildReasonFlagTriggerWeb)
	})

	ctx := context.Background()
	client := fake.NewFakeTiltClient()
	cm, err := configmap.TriggerQueue(ctx, client)
	require.NoError(t, err)

	nnA := types.NamespacedName{Name: "a"}
	nnB := types.NamespacedName{Name: "b"}
	nnC := types.NamespacedName{Name: "c"}
	assert.False(t, configmap.InTriggerQueue(cm, nnA))

	tqs := NewTriggerQueueSubscriber(client)
	require.NoError(t, tqs.OnChange(ctx, st, store.ChangeSummary{}))

	cm, err = configmap.TriggerQueue(ctx, client)
	require.NoError(t, err)

	assert.True(t, configmap.InTriggerQueue(cm, nnA))
	assert.True(t, configmap.InTriggerQueue(cm, nnB))
	assert.False(t, configmap.InTriggerQueue(cm, nnC))

	assert.Equal(t, model.BuildReasonFlagTriggerCLI, configmap.TriggerQueueReason(cm, nnA))
	assert.Equal(t, model.BuildReasonFlagTriggerWeb, configmap.TriggerQueueReason(cm, nnB))
	assert.Equal(t, model.BuildReasonNone, configmap.TriggerQueueReason(cm, nnC))
}

func TestTriggerQueueRemovesDisabledResources(t *testing.T) {
	st := store.NewTestingStore()
	st.WithState(func(s *store.EngineState) {
		s.UpsertManifestTarget(store.NewManifestTarget(model.Manifest{Name: "a"}))
		s.UpsertManifestTarget(store.NewManifestTarget(model.Manifest{Name: "b"}))
		s.UpsertManifestTarget(store.NewManifestTarget(model.Manifest{Name: "c"}))
		s.AppendToTriggerQueue("a", model.BuildReasonFlagTriggerCLI)
		s.AppendToTriggerQueue("b", model.BuildReasonFlagTriggerWeb)
		s.AppendToTriggerQueue("c", model.BuildReasonFlagTriggerWeb)
		s.UIResources["b"] = &v1alpha1.UIResource{
			Status: v1alpha1.UIResourceStatus{
				DisableStatus: v1alpha1.DisableResourceStatus{
					DisabledCount: 1,
				},
			},
		}
	})

	ctx := context.Background()
	client := fake.NewFakeTiltClient()
	tqs := NewTriggerQueueSubscriber(client)
	require.NoError(t, tqs.OnChange(ctx, st, store.ChangeSummary{}))

	ai := st.WaitForAction(t, reflect.TypeOf(RemoveFromTriggerQueueAction{}))
	require.IsType(t, ai, RemoveFromTriggerQueueAction{})
	a := ai.(RemoveFromTriggerQueueAction)
	require.Equal(t, model.ManifestName("b"), a.Name)
}
