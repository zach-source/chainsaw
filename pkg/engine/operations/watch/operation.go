package watch

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/kyverno/chainsaw/pkg/apis"
	"github.com/kyverno/chainsaw/pkg/apis/v1alpha1"
	"github.com/kyverno/chainsaw/pkg/engine/namespacer"
	"github.com/kyverno/kyverno-json/pkg/core/compilers"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

// ResourceInfo contains information about the watched resource
type ResourceInfo struct {
	APIVersion string
	Kind       string
	Name       string
	Namespace  string
}

// CreateWatcher creates a Kubernetes watcher for the specified resource
func CreateWatcher(
	ctx context.Context,
	config *rest.Config,
	compilers compilers.Compilers,
	namespacer namespacer.Namespacer,
	bindings apis.Bindings,
	watchSpec *v1alpha1.Watch,
	obj *unstructured.Unstructured,
) (watch.Interface, *ResourceInfo, error) {
	// Resolve resource details
	apiVersion, err := watchSpec.Resource.APIVersion.Value(ctx, compilers, bindings)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to resolve apiVersion: %w", err)
	}
	kind, err := watchSpec.Resource.Kind.Value(ctx, compilers, bindings)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to resolve kind: %w", err)
	}
	name, err := watchSpec.Resource.Name.Value(ctx, compilers, bindings)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to resolve name: %w", err)
	}
	namespace, err := watchSpec.Resource.Namespace.Value(ctx, compilers, bindings)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to resolve namespace: %w", err)
	}

	// Apply namespace from namespacer if not specified
	if namespace == "" && namespacer != nil {
		namespace = namespacer.GetNamespace()
	}

	obj.SetAPIVersion(apiVersion)
	obj.SetKind(kind)
	obj.SetName(name)
	obj.SetNamespace(namespace)

	resourceInfo := &ResourceInfo{
		APIVersion: apiVersion,
		Kind:       kind,
		Name:       name,
		Namespace:  namespace,
	}

	// Parse GVR
	gv, err := schema.ParseGroupVersion(apiVersion)
	if err != nil {
		return nil, resourceInfo, fmt.Errorf("invalid apiVersion %s: %w", apiVersion, err)
	}

	// Create discovery client
	discoveryClient, err := discovery.NewDiscoveryClientForConfig(config)
	if err != nil {
		return nil, resourceInfo, fmt.Errorf("failed to create discovery client: %w", err)
	}

	// Get resource mapping
	resources, err := discoveryClient.ServerResourcesForGroupVersion(apiVersion)
	if err != nil {
		return nil, resourceInfo, fmt.Errorf("failed to discover resources for %s: %w", apiVersion, err)
	}

	var resourceName string
	for _, resource := range resources.APIResources {
		if resource.Kind == kind {
			resourceName = resource.Name
			break
		}
	}
	if resourceName == "" {
		return nil, resourceInfo, fmt.Errorf("failed to find resource name for kind %s", kind)
	}

	gvr := schema.GroupVersionResource{
		Group:    gv.Group,
		Version:  gv.Version,
		Resource: resourceName,
	}

	// Create dynamic client
	dynamicClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, resourceInfo, fmt.Errorf("failed to create dynamic client: %w", err)
	}

	var resourceInterface dynamic.ResourceInterface
	if namespace != "" {
		resourceInterface = dynamicClient.Resource(gvr).Namespace(namespace)
	} else {
		resourceInterface = dynamicClient.Resource(gvr)
	}

	// Start watch
	watcher, err := resourceInterface.Watch(ctx, metav1.ListOptions{
		FieldSelector: "metadata.name=" + name,
	})
	if err != nil {
		return nil, resourceInfo, fmt.Errorf("failed to start watch: %w", err)
	}

	return watcher, resourceInfo, nil
}

// EvaluateCondition evaluates a watch condition against a resource
func EvaluateCondition(ctx context.Context, compilers compilers.Compilers, bindings apis.Bindings, obj *unstructured.Unstructured, condition v1alpha1.WatchCondition) (bool, error) {
	// Resolve the path
	path, err := condition.Path.Value(ctx, compilers, bindings)
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
	expectedValueRaw := condition.Value.Value()
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
