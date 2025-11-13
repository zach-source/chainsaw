package watch

import (
	"context"
	"testing"

	"github.com/kyverno/chainsaw/pkg/apis"
	"github.com/kyverno/chainsaw/pkg/apis/v1alpha1"
	"github.com/kyverno/kyverno-json/pkg/core/compilers"
	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestSplitPath(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		expected []string
	}{
		{
			name:     "simple path",
			path:     "status.replicas",
			expected: []string{"status", "replicas"},
		},
		{
			name:     "nested path",
			path:     "status.conditions.type",
			expected: []string{"status", "conditions", "type"},
		},
		{
			name:     "single field",
			path:     "name",
			expected: []string{"name"},
		},
		{
			name:     "empty path",
			path:     "",
			expected: nil,
		},
		{
			name:     "path with leading dot",
			path:     ".status.replicas",
			expected: []string{"status", "replicas"},
		},
		{
			name:     "path with trailing dot",
			path:     "status.replicas.",
			expected: []string{"status", "replicas"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := splitPath(tt.path)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestToFloat64(t *testing.T) {
	tests := []struct {
		name      string
		input     interface{}
		expected  float64
		expectErr bool
	}{
		{
			name:     "float64",
			input:    float64(42.5),
			expected: 42.5,
		},
		{
			name:     "float32",
			input:    float32(42.5),
			expected: 42.5,
		},
		{
			name:     "int",
			input:    int(42),
			expected: 42.0,
		},
		{
			name:     "int32",
			input:    int32(42),
			expected: 42.0,
		},
		{
			name:     "int64",
			input:    int64(42),
			expected: 42.0,
		},
		{
			name:     "string number",
			input:    "42.5",
			expected: 42.5,
		},
		{
			name:      "invalid string",
			input:     "not a number",
			expectErr: true,
		},
		{
			name:      "bool",
			input:     true,
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := toFloat64(tt.input)
			if tt.expectErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}

func TestCompareValues(t *testing.T) {
	tests := []struct {
		name      string
		actual    interface{}
		op        string
		expected  interface{}
		result    bool
		expectErr bool
	}{
		// Equality tests
		{
			name:     "string equals",
			actual:   "hello",
			op:       "==",
			expected: "hello",
			result:   true,
		},
		{
			name:     "string not equals",
			actual:   "hello",
			op:       "==",
			expected: "world",
			result:   false,
		},
		{
			name:     "number equals",
			actual:   42,
			op:       "==",
			expected: 42,
			result:   true,
		},
		// Not equals tests
		{
			name:     "not equals true",
			actual:   "hello",
			op:       "!=",
			expected: "world",
			result:   true,
		},
		{
			name:     "not equals false",
			actual:   "hello",
			op:       "!=",
			expected: "hello",
			result:   false,
		},
		// Greater than tests
		{
			name:     "greater than true",
			actual:   10,
			op:       ">",
			expected: 5,
			result:   true,
		},
		{
			name:     "greater than false",
			actual:   5,
			op:       ">",
			expected: 10,
			result:   false,
		},
		{
			name:     "greater than equal",
			actual:   5,
			op:       ">",
			expected: 5,
			result:   false,
		},
		// Greater than or equal tests
		{
			name:     "greater or equal true (greater)",
			actual:   10,
			op:       ">=",
			expected: 5,
			result:   true,
		},
		{
			name:     "greater or equal true (equal)",
			actual:   5,
			op:       ">=",
			expected: 5,
			result:   true,
		},
		{
			name:     "greater or equal false",
			actual:   3,
			op:       ">=",
			expected: 5,
			result:   false,
		},
		// Less than tests
		{
			name:     "less than true",
			actual:   5,
			op:       "<",
			expected: 10,
			result:   true,
		},
		{
			name:     "less than false",
			actual:   10,
			op:       "<",
			expected: 5,
			result:   false,
		},
		// Less than or equal tests
		{
			name:     "less or equal true (less)",
			actual:   5,
			op:       "<=",
			expected: 10,
			result:   true,
		},
		{
			name:     "less or equal true (equal)",
			actual:   5,
			op:       "<=",
			expected: 5,
			result:   true,
		},
		{
			name:     "less or equal false",
			actual:   10,
			op:       "<=",
			expected: 5,
			result:   false,
		},
		// Float comparisons
		{
			name:     "float greater than",
			actual:   10.5,
			op:       ">",
			expected: 10.2,
			result:   true,
		},
		// String number comparisons
		{
			name:     "string number comparison",
			actual:   "10",
			op:       ">",
			expected: "5",
			result:   true,
		},
		// Error cases
		{
			name:      "non-numeric comparison with >",
			actual:    "hello",
			op:        ">",
			expected:  "world",
			expectErr: true,
		},
		{
			name:      "unsupported operator",
			actual:    5,
			op:        "===",
			expected:  5,
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := compareValues(tt.actual, tt.op, tt.expected)
			if tt.expectErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.result, result)
			}
		})
	}
}

func TestEvaluateCondition(t *testing.T) {
	tests := []struct {
		name      string
		obj       *unstructured.Unstructured
		condition v1alpha1.WatchCondition
		expected  bool
		expectErr bool
	}{
		{
			name: "simple field comparison - match",
			obj: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"status": map[string]interface{}{
						"replicas": float64(3),
					},
				},
			},
			condition: v1alpha1.WatchCondition{
				Path:  "status.replicas",
				Op:    "==",
				Value: v1alpha1.NewProjection(float64(3)),
			},
			expected: true,
		},
		{
			name: "simple field comparison - no match",
			obj: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"status": map[string]interface{}{
						"replicas": float64(3),
					},
				},
			},
			condition: v1alpha1.WatchCondition{
				Path:  "status.replicas",
				Op:    "==",
				Value: v1alpha1.NewProjection(float64(5)),
			},
			expected: false,
		},
		{
			name: "numeric greater than",
			obj: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"status": map[string]interface{}{
						"availableReplicas": float64(3),
					},
				},
			},
			condition: v1alpha1.WatchCondition{
				Path:  "status.availableReplicas",
				Op:    ">=",
				Value: v1alpha1.NewProjection(float64(1)),
			},
			expected: true,
		},
		{
			name: "string comparison",
			obj: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"status": map[string]interface{}{
						"phase": "Running",
					},
				},
			},
			condition: v1alpha1.WatchCondition{
				Path:  "status.phase",
				Op:    "==",
				Value: v1alpha1.NewProjection("Running"),
			},
			expected: true,
		},
		{
			name: "not equals",
			obj: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"status": map[string]interface{}{
						"phase": "Running",
					},
				},
			},
			condition: v1alpha1.WatchCondition{
				Path:  "status.phase",
				Op:    "!=",
				Value: v1alpha1.NewProjection("Failed"),
			},
			expected: true,
		},
		{
			name: "field not found",
			obj: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"status": map[string]interface{}{
						"replicas": float64(3),
					},
				},
			},
			condition: v1alpha1.WatchCondition{
				Path:  "status.nonexistent",
				Op:    "==",
				Value: v1alpha1.NewProjection("anything"),
			},
			expected: false,
		},
		{
			name: "nested field",
			obj: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"metadata": map[string]interface{}{
						"labels": map[string]interface{}{
							"app": "test",
						},
					},
				},
			},
			condition: v1alpha1.WatchCondition{
				Path:  "metadata.labels.app",
				Op:    "==",
				Value: v1alpha1.NewProjection("test"),
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			compilers := compilers.DefaultCompilers
			bindings := apis.NewBindings()

			result, err := EvaluateCondition(ctx, compilers, bindings, tt.obj, tt.condition)
			if tt.expectErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}
