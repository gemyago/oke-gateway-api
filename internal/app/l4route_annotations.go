package app

import (
	"sort"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

func annotatedBackendSetNames(route client.Object, annotation string) map[string]struct{} {
	result := make(map[string]struct{})
	value := route.GetAnnotations()[annotation]
	for name := range strings.SplitSeq(value, ",") {
		name = strings.TrimSpace(name)
		if name != "" {
			result[name] = struct{}{}
		}
	}
	return result
}

func setAnnotatedBackendSetNames(route client.Object, annotation string, names map[string]struct{}) {
	annotations := route.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}

	if len(names) == 0 {
		delete(annotations, annotation)
		route.SetAnnotations(annotations)
		return
	}

	sortedNames := make([]string, 0, len(names))
	for name := range names {
		sortedNames = append(sortedNames, name)
	}
	sort.Strings(sortedNames)
	annotations[annotation] = strings.Join(sortedNames, ",")
	route.SetAnnotations(annotations)
}
