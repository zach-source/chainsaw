package operations

import (
	"context"
	"time"

	"github.com/kyverno/chainsaw/pkg/apis/v1alpha1"
	"github.com/kyverno/chainsaw/pkg/engine/operations/watch"
	"github.com/kyverno/chainsaw/pkg/engine/outputs"
	enginecontext "github.com/kyverno/chainsaw/pkg/runner/context"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type watchAction struct {
	op v1alpha1.Watch
}

func (o watchAction) Execute(ctx context.Context, tc enginecontext.TestContext) (outputs.Outputs, error) {
	ns := ""
	if namespacer := tc.Namespacer(); namespacer != nil {
		ns = namespacer.GetNamespace()
	}
	contextData := enginecontext.ContextData{
		Cluster:  o.op.Cluster,
		Clusters: o.op.Clusters,
		Timeouts: &v1alpha1.Timeouts{},
	}
	if o.op.Timeout != nil {
		contextData.Timeouts.Assert = o.op.Timeout
	}
	if tc, err := enginecontext.SetupContextAndBindings(tc, contextData); err != nil {
		return nil, err
	} else if _, client, err := tc.CurrentClusterClient(); err != nil {
		return nil, err
	} else {
		// Default timeout if not specified
		timeout := 30 * time.Second
		if o.op.Timeout != nil {
			timeout = o.op.Timeout.Duration
		}
		o.op.Timeout = &metav1.Duration{Duration: timeout}

		// Create namespacer
		namespacer := tc.Namespacer()

		op := watch.New(
			tc.Compilers(),
			client,
			o.op,
			namespacer,
		)
		// Add some buffer to the context timeout
		ctx, cancel := context.WithTimeout(ctx, timeout+10*time.Second)
		defer cancel()
		return op.Exec(ctx, tc.Bindings())
	}
}

func watchOperation(op v1alpha1.Watch) Operation {
	return watchAction{
		op: op,
	}
}
