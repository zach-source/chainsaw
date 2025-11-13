# Watch Step Type in Chainsaw

The `watch` step type allows you to actively monitor Kubernetes resources and evaluate custom conditions for test success or failure.

## Overview

Unlike the `wait` step which simply waits for a condition using kubectl, the `watch` step continuously watches a resource and can:

- Execute custom handlers on every watch event
- Evaluate complex success/failure conditions with comparison operators
- Provide detailed context to handlers about the resource state
- Immediately fail tests when failure conditions are met

## Basic Syntax

```yaml
- name: watch-deployment
  watch:
    resource:
      apiVersion: apps/v1
      kind: Deployment
      namespace: default
      name: my-deployment
    timeout: 120s
    successConditions:
      - path: status.availableReplicas
        op: ">="
        value: 1
    failureConditions:
      - path: status.conditions[?(@.type=="Available")].status
        op: "=="
        value: "False"
    handlers:
      onProgress:
        - echo "Watching..."
      onSuccess:
        - echo "Success!"
      onFailure:
        - echo "Failed!"
```

## Resource Specification

The `resource` field specifies which Kubernetes resource to watch:

```yaml
resource:
  apiVersion: apps/v1    # Required: API version of the resource
  kind: Deployment       # Required: Kind of the resource
  namespace: default     # Optional: Namespace (uses test namespace if not specified)
  name: my-deployment    # Required: Name of the resource to watch
```

## Conditions

### Success Conditions

Success conditions define when the watch should complete successfully. ALL conditions must be met for success.

```yaml
successConditions:
  - path: status.availableReplicas
    op: ">="
    value: 1
  - path: status.readyReplicas
    op: ">="
    value: 1
```

### Failure Conditions

Failure conditions cause the test to fail immediately when ANY condition is met.

```yaml
failureConditions:
  - path: status.conditions[?(@.type=="Progressing")].status
    op: "=="
    value: "False"
  - path: status.failedReplicas
    op: ">"
    value: 0
```

### Path

The `path` field is a dot-separated JSONPath expression to extract a value from the resource.

Examples:
- `status.availableReplicas` - Simple field access
- `metadata.name` - Nested field access
- `status.conditions[?(@.type=="Available")].status` - JSONPath filter (basic support)

### Operators

Supported comparison operators:

- `==` - Equals
- `!=` - Not equals
- `>` - Greater than (numeric comparison)
- `>=` - Greater than or equal (numeric comparison)
- `<` - Less than (numeric comparison)
- `<=` - Less than or equal (numeric comparison)

### Value

The `value` to compare against. Can be:
- Numbers: `1`, `5`, `100`
- Strings: `"Ready"`, `"True"`, `"False"`
- The comparison will attempt numeric conversion for numeric operators

## Event Handlers

Handlers are shell scripts that execute at different stages of the watch operation. They provide powerful integration points for logging, diagnostics, and custom actions.

### Handler Types

#### onProgress

Executes on **every watch event** (ADDED, MODIFIED, DELETED).

```yaml
handlers:
  onProgress:
    - echo "Event: $eventType - Available: $(echo '$resource' | jq -r '.status.availableReplicas // 0')"
```

Use cases:
- Logging progress
- Debugging watch events
- Monitoring state changes

#### onSuccess

Executes **once** when all success conditions are met (just before returning success).

```yaml
handlers:
  onSuccess:
    - echo "✅ Success!"
    - kubectl get deployment my-deployment -o wide
```

Use cases:
- Logging final state
- Capturing successful results
- Running verification commands

#### onFailure

Executes **once** when any failure condition is met (just before returning failure).

```yaml
handlers:
  onFailure:
    - echo "❌ Failure detected!"
    - kubectl describe deployment my-deployment
    - kubectl logs -l app=my-app --tail=50
```

Use cases:
- Collecting diagnostics
- Dumping pod logs
- Describing failed resources
- Saving failure state for analysis

### Available Environment Variables

All handlers have access to these environment variables:

| Variable | Description | Example |
|----------|-------------|---------|
| `$eventType` | Kubernetes watch event type | `ADDED`, `MODIFIED`, `DELETED` |
| `$resource` | Complete resource as JSON | `{"apiVersion": "apps/v1", ...}` |
| `$resourceYAML` | Complete resource as YAML | Multi-line YAML string |
| `$resourceName` | Resource name | `my-deployment` |
| `$resourceNamespace` | Resource namespace | `default` |
| `$resourceKind` | Resource kind | `Deployment` |
| `$resourceAPIVersion` | Resource API version | `apps/v1` |
| `$NAMESPACE` | Test namespace (from Chainsaw) | `chainsaw-test-123` |

### Handler Script Examples

#### Simple Logging

```yaml
handlers:
  onProgress:
    - echo "Status: $(echo '$resource' | jq -r '.status.phase')"
```

#### Multi-line Script

```yaml
handlers:
  onProgress:
    - |
      STATUS=$(echo '$resource' | jq -r '.status.availableReplicas // 0')
      echo "Available replicas: $STATUS"
      if [ "$STATUS" -gt 0 ]; then
        echo "At least one replica is available"
      fi
```

#### Using kubectl

```yaml
handlers:
  onFailure:
    - kubectl describe deployment $resourceName -n $resourceNamespace
    - kubectl get pods -n $resourceNamespace -l app=myapp
    - kubectl logs -n $resourceNamespace -l app=myapp --tail=100
```

#### Conditional Logic

```yaml
handlers:
  onProgress:
    - |
      STATUS=$(echo '$resource' | jq -r '.data.status')
      case $STATUS in
        pending)
          echo "⏳ Waiting..."
          ;;
        running)
          echo "🏃 Running..."
          ;;
        complete)
          echo "✅ Complete!"
          ;;
        *)
          echo "⚠️  Unknown: $STATUS"
          ;;
      esac
```

#### Saving State

```yaml
handlers:
  onSuccess:
    - echo "$resourceYAML" > /tmp/final-state.yaml
    - echo "Saved final state"

  onFailure:
    - echo "$resourceYAML" > /tmp/failed-state.yaml
    - kubectl get events -n $resourceNamespace > /tmp/events.txt
    - echo "Diagnostics saved"
```

#### Advanced jq Usage

```yaml
handlers:
  onProgress:
    - |
      echo "Resource summary:"
      echo '$resource' | jq '{
        name: .metadata.name,
        namespace: .metadata.namespace,
        availableReplicas: .status.availableReplicas,
        conditions: [.status.conditions[] | {type: .type, status: .status}]
      }'
```

## Complete Example

```yaml
apiVersion: chainsaw.kyverno.io/v1alpha2
kind: Test
metadata:
  name: deployment-rollout-test
spec:
  namespace: test-ns
  steps:
    - name: create-deployment
      apply:
        file: deployment.yaml

    - name: watch-deployment-rollout
      watch:
        resource:
          apiVersion: apps/v1
          kind: Deployment
          namespace: test-ns
          name: my-app
        timeout: 300s

        # Wait for 3 replicas to be available and ready
        successConditions:
          - path: status.availableReplicas
            op: ">="
            value: 3
          - path: status.readyReplicas
            op: ">="
            value: 3

        # Fail if deployment is not progressing
        failureConditions:
          - path: status.conditions[?(@.type=="Progressing")].status
            op: "=="
            value: "False"
          - path: status.conditions[?(@.type=="Available")].status
            op: "=="
            value: "False"

        handlers:
          # Log progress on each event
          onProgress:
            - |
              AVAILABLE=$(echo '$resource' | jq -r '.status.availableReplicas // 0')
              READY=$(echo '$resource' | jq -r '.status.readyReplicas // 0')
              DESIRED=$(echo '$resource' | jq -r '.spec.replicas')
              echo "Rollout progress: $AVAILABLE/$DESIRED available, $READY/$DESIRED ready"
              kubectl get pods -n $resourceNamespace -l app=my-app -o wide

          # Celebrate success
          onSuccess:
            - |
              echo "✅ Deployment rollout successful!"
              kubectl get deployment my-app -n test-ns
              kubectl get pods -n test-ns -l app=my-app -o wide

          # Collect diagnostics on failure
          onFailure:
            - |
              echo "❌ Deployment rollout failed"
              echo "Deployment status:"
              kubectl describe deployment my-app -n test-ns
              echo ""
              echo "Pod status:"
              kubectl get pods -n test-ns -l app=my-app
              echo ""
              echo "Recent events:"
              kubectl get events -n test-ns --sort-by='.lastTimestamp' | tail -20
              echo ""
              echo "Pod logs:"
              kubectl logs -n test-ns -l app=my-app --tail=100 --all-containers=true
```

## Comparison with Wait Step

| Feature | watch | wait |
|---------|-------|------|
| Implementation | Native Kubernetes watch API | kubectl wait command |
| Custom conditions | Yes (with operators) | Limited to kubectl wait options |
| Event handlers | Yes | No |
| Progress monitoring | Yes (onProgress handlers) | No |
| Failure conditions | Yes (immediate failure) | No |
| Access to resource state | Yes (full JSON/YAML in handlers) | No |
| Multiple conditions | Yes (all must pass for success) | Single condition |

Use `watch` when you need:
- Custom success/failure logic
- Event handlers for logging or diagnostics
- Immediate failure on specific conditions
- Progress monitoring
- Access to resource state in handlers

Use `wait` when you need:
- Simple kubectl wait functionality
- Standard Kubernetes conditions (Ready, Available, etc.)
- Minimal overhead

## Tips and Best Practices

1. **Use onProgress sparingly** - It executes on every event, which can be verbose for frequently-updated resources.

2. **Leverage jq in handlers** - The `$resource` variable contains the full resource as JSON, perfect for jq processing.

3. **Collect diagnostics in onFailure** - This is your chance to gather all relevant information before the test fails.

4. **Use meaningful timeouts** - Set timeouts appropriate for your resource (deployments may take minutes to roll out).

5. **Combine conditions thoughtfully** - All success conditions must pass, but any failure condition causes immediate failure.

6. **Test handlers separately** - Make sure your handler scripts work correctly before integrating them into tests.

7. **Use kubectl in handlers** - You have full access to kubectl and the cluster context.

8. **Save state for debugging** - Use handlers to save YAML/JSON to files for post-test analysis.

## See Also

- [watch-example.yaml](./watch-example.yaml) - Simple example
- [watch-with-handlers-example.yaml](./watch-with-handlers-example.yaml) - Example with handlers
- [watch-handlers-guide.yaml](./watch-handlers-guide.yaml) - Comprehensive handler examples
