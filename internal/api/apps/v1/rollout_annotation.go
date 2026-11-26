package v1

const rolloutRestartAnnotation = "kubectl.kubernetes.io/restartedAt"

func projectRolloutAnnotation(annotations map[string]string, configured *map[string]string) {
	if !hasRolloutAnnotation(configured) {
		delete(annotations, rolloutRestartAnnotation)
	}
}

func preserveRolloutAnnotation(
	annotations, live map[string]string, prior, desired *map[string]string,
) map[string]string {
	if hasRolloutAnnotation(prior) || hasRolloutAnnotation(desired) {
		return annotations
	}
	value, present := live[rolloutRestartAnnotation]
	if !present {
		return annotations
	}
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations[rolloutRestartAnnotation] = value
	return annotations
}

func hasRolloutAnnotation(annotations *map[string]string) bool {
	if annotations == nil {
		return false
	}
	_, present := (*annotations)[rolloutRestartAnnotation]
	return present
}
