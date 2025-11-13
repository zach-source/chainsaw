package operations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/kyverno/chainsaw/pkg/apis/v1alpha1"
	apibindings "github.com/kyverno/chainsaw/pkg/engine/bindings"
	"github.com/kyverno/chainsaw/pkg/engine/operations/internal"
	"github.com/kyverno/chainsaw/pkg/engine/operations/watch"
	"github.com/kyverno/chainsaw/pkg/engine/outputs"
	"github.com/kyverno/chainsaw/pkg/logging"
	enginecontext "github.com/kyverno/chainsaw/pkg/runner/context"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8swatch "k8s.io/apimachinery/pkg/watch"
	"sigs.k8s.io/yaml"
)

type watchAction struct {
	op v1alpha1.Watch
}

func (o watchAction) Execute(ctx context.Context, tc enginecontext.TestContext) (outputs.Outputs, error) {
	contextData := enginecontext.ContextData{
		Cluster:  o.op.Cluster,
		Clusters: o.op.Clusters,
		Timeouts: &v1alpha1.Timeouts{},
	}
	if o.op.Timeout != nil {
		contextData.Timeouts.Assert = o.op.Timeout
	}
	tc, err := enginecontext.SetupContextAndBindings(tc, contextData)
	if err != nil {
		return nil, err
	}

	_, client, err := tc.CurrentClusterClient()
	if err != nil {
		return nil, err
	}

	// Default timeout if not specified
	timeout := 30 * time.Second
	if o.op.Timeout != nil {
		timeout = o.op.Timeout.Duration
	}
	o.op.Timeout = &metav1.Duration{Duration: timeout}

	// Create watcher
	obj := unstructured.Unstructured{}
	defer func() {
		internal.LogEnd(ctx, logging.Watch, &obj, err)
	}()

	watcher, resourceInfo, err := watch.CreateWatcher(ctx, tc.Compilers(), client, tc.Namespacer(), tc.Bindings(), &o.op, &obj)
	if err != nil {
		return nil, err
	}
	defer watcher.Stop()

	internal.LogStart(ctx, logging.Watch, &obj)

	// Add buffer to the context timeout
	ctx, cancel := context.WithTimeout(ctx, timeout+10*time.Second)
	defer cancel()

	// Watch loop
	return nil, o.watchLoop(ctx, tc, watcher, resourceInfo)
}

func (o watchAction) watchLoop(ctx context.Context, tc enginecontext.TestContext, watcher k8swatch.Interface, resourceInfo *watch.ResourceInfo) error {
	bindings := tc.Bindings()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case event, ok := <-watcher.ResultChan():
			if !ok {
				return errors.New("watch channel closed unexpectedly")
			}

			if event.Type == k8swatch.Error {
				return fmt.Errorf("watch error: %v", event.Object)
			}

			if event.Type == k8swatch.Deleted {
				return errors.New("resource was deleted during watch")
			}

			obj, ok := event.Object.(*unstructured.Unstructured)
			if !ok {
				continue
			}

			// Execute onProgress handlers
			if o.op.Handlers != nil && len(o.op.Handlers.OnProgress) > 0 {
				if err := o.executeHandlers(ctx, tc, o.op.Handlers.OnProgress, obj, event.Type); err != nil {
					logging.Log(ctx, logging.Watch, logging.WarnStatus, nil, logging.EmptyColor,
						logging.ErrSection(fmt.Errorf("onProgress handler failed: %w", err)))
				}
			}

			// Check failure conditions first (they immediately fail)
			if len(o.op.FailureConditions) > 0 {
				for _, condition := range o.op.FailureConditions {
					matched, err := watch.EvaluateCondition(ctx, tc.Compilers(), bindings, obj, condition)
					if err != nil {
						return fmt.Errorf("error evaluating failure condition: %w", err)
					}
					if matched {
						// Execute onFailure handlers
						if o.op.Handlers != nil && len(o.op.Handlers.OnFailure) > 0 {
							_ = o.executeHandlers(ctx, tc, o.op.Handlers.OnFailure, obj, event.Type)
						}
						return fmt.Errorf("failure condition met: %s %s %v", condition.Path, condition.Op, condition.Value)
					}
				}
			}

			// Check success conditions
			if len(o.op.SuccessConditions) > 0 {
				allMatched := true
				for _, condition := range o.op.SuccessConditions {
					matched, err := watch.EvaluateCondition(ctx, tc.Compilers(), bindings, obj, condition)
					if err != nil {
						return fmt.Errorf("error evaluating success condition: %w", err)
					}
					if !matched {
						allMatched = false
						break
					}
				}
				if allMatched {
					// Execute onSuccess handlers
					if o.op.Handlers != nil && len(o.op.Handlers.OnSuccess) > 0 {
						_ = o.executeHandlers(ctx, tc, o.op.Handlers.OnSuccess, obj, event.Type)
					}
					// Success!
					return nil
				}
			} else {
				// No success conditions means just watching for the resource to exist
				// Execute onSuccess handlers
				if o.op.Handlers != nil && len(o.op.Handlers.OnSuccess) > 0 {
					_ = o.executeHandlers(ctx, tc, o.op.Handlers.OnSuccess, obj, event.Type)
				}
				return nil
			}
		}
	}
}

func (o watchAction) executeHandlers(ctx context.Context, tc enginecontext.TestContext, handlers []v1alpha1.CatchFinally, obj *unstructured.Unstructured, eventType k8swatch.EventType) error {
	if len(handlers) == 0 {
		return nil
	}

	// Add watch-specific bindings
	handlerBindings := tc.Bindings()
	if obj != nil {
		// Add resource as JSON
		resourceJSON, err := json.MarshalIndent(obj.Object, "", "  ")
		if err == nil {
			handlerBindings = apibindings.RegisterBinding(handlerBindings, "resource", string(resourceJSON))
		}

		// Add resource as YAML
		resourceYAML, err := yaml.Marshal(obj.Object)
		if err == nil {
			handlerBindings = apibindings.RegisterBinding(handlerBindings, "resourceYAML", string(resourceYAML))
		}

		// Add individual resource fields
		handlerBindings = apibindings.RegisterBinding(handlerBindings, "resourceName", obj.GetName())
		handlerBindings = apibindings.RegisterBinding(handlerBindings, "resourceNamespace", obj.GetNamespace())
		handlerBindings = apibindings.RegisterBinding(handlerBindings, "resourceKind", obj.GetKind())
		handlerBindings = apibindings.RegisterBinding(handlerBindings, "resourceAPIVersion", obj.GetAPIVersion())
	}
	handlerBindings = apibindings.RegisterBinding(handlerBindings, "eventType", string(eventType))

	// Create a new test context with handler bindings
	handlerTC := tc.WithBindings(handlerBindings)

	// Execute each handler using CatchOperation
	for _, handler := range handlers {
		ops, err := CatchOperation(ctx, handlerTC, handler)
		if err != nil {
			return err
		}

		for _, op := range ops {
			_, err := op.Execute(ctx, handlerTC)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

func watchOperation(op v1alpha1.Watch) Operation {
	return watchAction{
		op: op,
	}
}
