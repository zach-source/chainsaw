package watch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"

	"github.com/kyverno/chainsaw/pkg/apis"
	"github.com/kyverno/chainsaw/pkg/apis/v1alpha1"
	"github.com/kyverno/chainsaw/pkg/client"
	apibindings "github.com/kyverno/chainsaw/pkg/engine/bindings"
	"github.com/kyverno/chainsaw/pkg/engine/namespacer"
	"github.com/kyverno/chainsaw/pkg/engine/operations"
	"github.com/kyverno/chainsaw/pkg/engine/operations/internal"
	"github.com/kyverno/chainsaw/pkg/engine/outputs"
	"github.com/kyverno/chainsaw/pkg/logging"
	"github.com/kyverno/kyverno-json/pkg/core/compilers"
	"github.com/kyverno/pkg/ext/output/color"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	"sigs.k8s.io/yaml"
)

type operation struct {
	compilers  compilers.Compilers
	client     client.Client
	watch      v1alpha1.Watch
	namespacer namespacer.Namespacer
}

func New(
	compilers compilers.Compilers,
	client client.Client,
	watchSpec v1alpha1.Watch,
	namespacer namespacer.Namespacer,
) operations.Operation {
	return &operation{
		compilers:  compilers,
		client:     client,
		watch:      watchSpec,
		namespacer: namespacer,
	}
}

func (o *operation) Exec(ctx context.Context, bindings apis.Bindings) (_ outputs.Outputs, _err error) {
	if bindings == nil {
		bindings = apis.NewBindings()
	}

	obj := unstructured.Unstructured{}
	defer func() {
		internal.LogEnd(ctx, logging.Watch, &obj, _err)
	}()

	// Resolve resource details
	apiVersion, err := o.watch.Resource.APIVersion.Value(ctx, o.compilers, bindings)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve apiVersion: %w", err)
	}
	kind, err := o.watch.Resource.Kind.Value(ctx, o.compilers, bindings)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve kind: %w", err)
	}
	name, err := o.watch.Resource.Name.Value(ctx, o.compilers, bindings)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve name: %w", err)
	}
	namespace, err := o.watch.Resource.Namespace.Value(ctx, o.compilers, bindings)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve namespace: %w", err)
	}

	// Apply namespace from namespacer if not specified
	if namespace == "" && o.namespacer != nil {
		namespace = o.namespacer.GetNamespace()
	}

	obj.SetAPIVersion(apiVersion)
	obj.SetKind(kind)
	obj.SetName(name)
	obj.SetNamespace(namespace)

	internal.LogStart(ctx, logging.Watch, &obj)
	return nil, o.execute(ctx, bindings, apiVersion, kind, name, namespace)
}

func (o *operation) execute(ctx context.Context, bindings apis.Bindings, apiVersion, kind, name, namespace string) error {
	// Parse GVR
	gv, err := schema.ParseGroupVersion(apiVersion)
	if err != nil {
		return fmt.Errorf("invalid apiVersion %s: %w", apiVersion, err)
	}

	// Get resource mapping
	resources, err := o.client.Discovery().ServerResourcesForGroupVersion(apiVersion)
	if err != nil {
		return fmt.Errorf("failed to discover resources for %s: %w", apiVersion, err)
	}

	var resourceName string
	for _, resource := range resources.APIResources {
		if resource.Kind == kind {
			resourceName = resource.Name
			break
		}
	}
	if resourceName == "" {
		return fmt.Errorf("failed to find resource name for kind %s", kind)
	}

	gvr := schema.GroupVersionResource{
		Group:    gv.Group,
		Version:  gv.Version,
		Resource: resourceName,
	}

	// Create dynamic client
	dynamicClient := o.client.DynamicClient()
	var resourceInterface dynamic.ResourceInterface
	if namespace != "" {
		resourceInterface = dynamicClient.Resource(gvr).Namespace(namespace)
	} else {
		resourceInterface = dynamicClient.Resource(gvr)
	}

	// Start watch
	watcher, err := resourceInterface.Watch(ctx, client.ListOptions{
		FieldSelector: "metadata.name=" + name,
	})
	if err != nil {
		return fmt.Errorf("failed to start watch: %w", err)
	}
	defer watcher.Stop()

	// Watch loop
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case event, ok := <-watcher.ResultChan():
			if !ok {
				return errors.New("watch channel closed unexpectedly")
			}

			if event.Type == watch.Error {
				return fmt.Errorf("watch error: %v", event.Object)
			}

			if event.Type == watch.Deleted {
				return errors.New("resource was deleted during watch")
			}

			obj, ok := event.Object.(*unstructured.Unstructured)
			if !ok {
				continue
			}

			// Execute onProgress handlers
			if o.watch.Handlers != nil && len(o.watch.Handlers.OnProgress) > 0 {
				o.executeHandlers(ctx, bindings, o.watch.Handlers.OnProgress, obj, event.Type)
			}

			// Check failure conditions first (they immediately fail)
			if len(o.watch.FailureConditions) > 0 {
				for _, condition := range o.watch.FailureConditions {
					matched, err := o.evaluateCondition(ctx, bindings, obj, condition)
					if err != nil {
						return fmt.Errorf("error evaluating failure condition: %w", err)
					}
					if matched {
						// Execute onFailure handlers
						if o.watch.Handlers != nil && len(o.watch.Handlers.OnFailure) > 0 {
							o.executeHandlers(ctx, bindings, o.watch.Handlers.OnFailure, obj, event.Type)
						}
						return fmt.Errorf("failure condition met: %s %s %v", condition.Path, condition.Op, condition.Value)
					}
				}
			}

			// Check success conditions
			if len(o.watch.SuccessConditions) > 0 {
				allMatched := true
				for _, condition := range o.watch.SuccessConditions {
					matched, err := o.evaluateCondition(ctx, bindings, obj, condition)
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
					if o.watch.Handlers != nil && len(o.watch.Handlers.OnSuccess) > 0 {
						o.executeHandlers(ctx, bindings, o.watch.Handlers.OnSuccess, obj, event.Type)
					}
					// Success!
					return nil
				}
			} else {
				// No success conditions means just watching for the resource to exist
				// Execute onSuccess handlers
				if o.watch.Handlers != nil && len(o.watch.Handlers.OnSuccess) > 0 {
					o.executeHandlers(ctx, bindings, o.watch.Handlers.OnSuccess, obj, event.Type)
				}
				return nil
			}
		}
	}
}

func (o *operation) evaluateCondition(ctx context.Context, bindings apis.Bindings, obj *unstructured.Unstructured, condition v1alpha1.WatchCondition) (bool, error) {
	// Resolve the path
	path, err := condition.Path.Value(ctx, o.compilers, bindings)
	if err != nil {
		return false, fmt.Errorf("failed to resolve path: %w", err)
	}

	// Get value from object using JSONPath
	actualValue, found, err := unstructured.NestedFieldNoCopy(obj.Object, splitPath(path)...)
	if err != nil {
		return false, fmt.Errorf("failed to get value at path %s: %w", path, err)
	}
	if !found {
		return false, nil
	}

	// Resolve expected value from projection
	expectedValueRaw := condition.Value.Value
	if expectedValueRaw == nil {
		return false, errors.New("condition value is nil")
	}

	// Compare based on operator
	return compareValues(actualValue, condition.Op, expectedValueRaw)
}

func splitPath(path string) []string {
	// Simple path splitter - handles dot-separated paths
	// This is a simplified version; production code might need more sophisticated JSONPath parsing
	var result []string
	current := ""
	for _, ch := range path {
		if ch == '.' {
			if current != "" {
				result = append(result, current)
				current = ""
			}
		} else {
			current += string(ch)
		}
	}
	if current != "" {
		result = append(result, current)
	}
	return result
}

func compareValues(actual interface{}, op string, expected interface{}) (bool, error) {
	switch op {
	case "==":
		return fmt.Sprint(actual) == fmt.Sprint(expected), nil
	case "!=":
		return fmt.Sprint(actual) != fmt.Sprint(expected), nil
	case ">", ">=", "<", "<=":
		// Try to compare as numbers
		actualNum, err1 := toFloat64(actual)
		expectedNum, err2 := toFloat64(expected)
		if err1 != nil || err2 != nil {
			return false, fmt.Errorf("cannot compare non-numeric values with operator %s", op)
		}
		switch op {
		case ">":
			return actualNum > expectedNum, nil
		case ">=":
			return actualNum >= expectedNum, nil
		case "<":
			return actualNum < expectedNum, nil
		case "<=":
			return actualNum <= expectedNum, nil
		}
	}
	return false, fmt.Errorf("unsupported operator: %s", op)
}

func toFloat64(v interface{}) (float64, error) {
	switch val := v.(type) {
	case float64:
		return val, nil
	case float32:
		return float64(val), nil
	case int:
		return float64(val), nil
	case int32:
		return float64(val), nil
	case int64:
		return float64(val), nil
	case string:
		return strconv.ParseFloat(val, 64)
	default:
		return 0, fmt.Errorf("cannot convert %T to float64", v)
	}
}

// executeHandlers executes a list of handler expressions as shell scripts
func (o *operation) executeHandlers(ctx context.Context, bindings apis.Bindings, handlers []v1alpha1.Expression, obj *unstructured.Unstructured, eventType watch.EventType) {
	if len(handlers) == 0 {
		return
	}

	// Add watch-specific bindings
	handlerBindings := bindings
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

	// Execute each handler
	for _, handler := range handlers {
		if err := o.executeHandler(ctx, handlerBindings, handler); err != nil {
			logging.Log(ctx, logging.Watch, logging.WarnStatus, nil, color.BoldYellow,
				logging.Section("HANDLER", string(handler)),
				logging.ErrSection(err))
		}
	}
}

// executeHandler executes a single handler expression as a shell script
func (o *operation) executeHandler(ctx context.Context, bindings apis.Bindings, handler v1alpha1.Expression) error {
	// Evaluate the handler expression
	script, err := handler.Value(ctx, o.compilers, bindings)
	if err != nil {
		return fmt.Errorf("failed to evaluate handler expression: %w", err)
	}

	if script == "" {
		return nil
	}

	// Prepare environment variables from bindings
	env := os.Environ()
	for key, value := range bindings {
		if binding := value.Value(); binding != nil {
			env = append(env, fmt.Sprintf("%s=%v", key, binding))
		}
	}

	// Execute the script
	cmd := exec.CommandContext(ctx, "sh", "-c", script)
	cmd.Env = env

	var output internal.CommandOutput
	cmd.Stdout = &output.Stdout
	cmd.Stderr = &output.Stderr

	if err := cmd.Run(); err != nil {
		// Log output even on error
		if sections := output.Sections(); len(sections) != 0 {
			logging.Log(ctx, logging.Watch, logging.ErrorStatus, nil, color.BoldRed, sections...)
		}
		return fmt.Errorf("handler script failed: %w", err)
	}

	// Log successful output
	if sections := output.Sections(); len(sections) != 0 {
		logging.Log(ctx, logging.Watch, logging.LogStatus, nil, color.BoldFgCyan, sections...)
	}

	return nil
}
