# Watch Step Type

The `watch` step type allows you to actively monitor Kubernetes resources and execute operations when conditions are met.

## Overview

Unlike the `wait` step which simply waits for a condition using kubectl, the `watch` step continuously watches a resource using the Kubernetes watch API and can:

- Execute Chainsaw operations on every watch event (onProgress handlers)
- Evaluate complex success/failure conditions with comparison operators
- Execute operations when success conditions are met (onSuccess handlers)
- Execute operations immediately when failure conditions are met (onFailure handlers)
- Provide full resource context to handlers via environment variables

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
        - script:
            content: echo "Watching..."
      onSuccess:
        - script:
            content: echo "Success!"
      onFailure:
        - describe:
            apiVersion: apps/v1
            kind: Deployment
            name: my-deployment
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

## Event Handlers

Handlers are Chainsaw operations that execute at different stages of the watch operation. They provide powerful integration points for logging, diagnostics, and custom actions.

### Handler Types

#### onProgress

Executes on **every watch event** (ADDED, MODIFIED, DELETED).

```yaml
handlers:
  onProgress:
    - script:
        content: |
          echo "Available replicas: $(echo '$resource' | jq -r '.status.availableReplicas // 0')"
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
    - script:
        content: echo "✅ Success!"
    - get:
        apiVersion: v1
        kind: Pod
        namespace: default
        selector: app=my-app
```

Use cases:
- Logging final state
- Verifying related resources
- Running post-success commands

#### onFailure

Executes **once** when any failure condition is met (just before returning failure).

```yaml
handlers:
  onFailure:
    - script:
        content: echo "❌ Failure detected!"
    - describe:
        apiVersion: apps/v1
        kind: Deployment
        name: my-deployment
    - podLogs:
        selector: app=my-app
        tail: 50
```

Use cases:
- Collecting diagnostics
- Dumping pod logs
- Describing failed resources
- Saving failure state for analysis

### Handler Operations

Handlers can be any operation supported in `catch` and `finally` blocks:

| Operation | Description | Example |
|-----------|-------------|---------|
| `script` | Run shell scripts | Execute custom logic, jq processing |
| `command` | Execute commands | Run kubectl, custom binaries |
| `describe` | Describe Kubernetes resources | Get detailed resource info |
| `get` | Get Kubernetes resources | List pods, services, etc. |
| `events` | Get Kubernetes events | View cluster events |
| `podLogs` | Get pod logs | Collect container logs |
| `wait` | Wait for resources | Wait for dependent resources |
| `sleep` | Sleep for duration | Add delays |
| `delete` | Delete resources | Cleanup on failure |

### Available Environment Variables

All handlers have access to these environment variables:

| Variable | Description | Example |
|----------|-------------|---------|
| `$eventType` | Kubernetes watch event type | `ADDED`, `MODIFIED`, `DELETED` |
| `$resource` | Complete resource as JSON | Full resource object |
| `$resourceYAML` | Complete resource as YAML | Multi-line YAML string |
| `$resourceName` | Resource name | `my-deployment` |
| `$resourceNamespace` | Resource namespace | `default` |
| `$resourceKind` | Resource kind | `Deployment` |
| `$resourceAPIVersion` | Resource API version | `apps/v1` |
| `$NAMESPACE` | Test namespace (from Chainsaw) | `chainsaw-test-123` |

### Handler Examples

#### Simple Script

```yaml
handlers:
  onProgress:
    - script:
        content: echo "Status: $(echo '$resource' | jq -r '.status.phase')"
```

#### Using kubectl Commands

```yaml
handlers:
  onFailure:
    - command:
        entrypoint: kubectl
        args:
          - describe
          - deployment
          - $(echo $resourceName)
          - -n
          - $(echo $resourceNamespace)
```

#### Describe Resources

```yaml
handlers:
  onFailure:
    - describe:
        apiVersion: apps/v1
        kind: Deployment
        namespace: default
        name: my-deployment
```

#### Get Related Resources

```yaml
handlers:
  onSuccess:
    - get:
        apiVersion: v1
        kind: Pod
        namespace: default
        selector: app=my-app
        format: wide
```

#### Collect Pod Logs

```yaml
handlers:
  onFailure:
    - podLogs:
        namespace: default
        selector: app=my-app
        tail: 100
```

#### Multiple Handlers

```yaml
handlers:
  onFailure:
    - script:
        content: echo "Deployment failed - collecting diagnostics"
    - describe:
        apiVersion: apps/v1
        kind: Deployment
        name: my-deployment
    - get:
        apiVersion: v1
        kind: Pod
        selector: app=my-app
    - podLogs:
        selector: app=my-app
        tail: 100
    - events:
        namespace: default
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
            - script:
                content: |
                  AVAILABLE=$(echo '$resource' | jq -r '.status.availableReplicas // 0')
                  READY=$(echo '$resource' | jq -r '.status.readyReplicas // 0')
                  DESIRED=$(echo '$resource' | jq -r '.spec.replicas')
                  echo "Rollout progress: $AVAILABLE/$DESIRED available, $READY/$DESIRED ready"
            - get:
                apiVersion: v1
                kind: Pod
                namespace: test-ns
                selector: app=my-app
                format: wide

          # Celebrate success
          onSuccess:
            - script:
                content: echo "✅ Deployment rollout successful!"
            - get:
                apiVersion: apps/v1
                kind: Deployment
                namespace: test-ns
                name: my-app
            - get:
                apiVersion: v1
                kind: Pod
                namespace: test-ns
                selector: app=my-app
                format: wide

          # Collect diagnostics on failure
          onFailure:
            - script:
                content: echo "❌ Deployment rollout failed"
            - describe:
                apiVersion: apps/v1
                kind: Deployment
                namespace: test-ns
                name: my-app
            - get:
                apiVersion: v1
                kind: Pod
                namespace: test-ns
                selector: app=my-app
            - events:
                namespace: test-ns
            - podLogs:
                namespace: test-ns
                selector: app=my-app
                tail: 100
                allContainers: true
```

## Comparison with Wait Step

| Feature | watch | wait |
|---------|-------|------|
| Implementation | Native Kubernetes watch API | kubectl wait command |
| Custom conditions | Yes (with operators) | Limited to kubectl wait options |
| Event handlers | Yes (full Chainsaw operations) | No |
| Progress monitoring | Yes (onProgress handlers) | No |
| Failure conditions | Yes (immediate failure) | No |
| Access to resource state | Yes (full JSON/YAML in handlers) | No |
| Multiple conditions | Yes (all must pass for success) | Single condition |
| Handler operations | script, command, describe, get, events, podLogs, wait, sleep, delete | N/A |

Use `watch` when you need:
- Custom success/failure logic with operators
- Event handlers for logging or diagnostics
- Immediate failure on specific conditions
- Progress monitoring during resource changes
- Access to full resource state in handlers
- Complex multi-step operations on success/failure

Use `wait` when you need:
- Simple kubectl wait functionality
- Standard Kubernetes conditions (Ready, Available, etc.)
- Minimal overhead

## Examples

See the comprehensive examples:
- [watch-example.yaml](./watch-example.yaml) - Simple example with handlers
- [watch-with-handlers-example.yaml](./watch-with-handlers-example.yaml) - Deployment watch with comprehensive handlers
- [watch-handlers-guide.yaml](./watch-handlers-guide.yaml) - Complete guide showing all handler operation types
