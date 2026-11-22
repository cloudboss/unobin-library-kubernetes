package v1

import rbacv1 "k8s.io/api/rbac/v1"

func clusterBindingSubjects(
	input []ClusterRoleBindingSubject, previous []ClusterRoleBindingSubjectOutput,
) ([]ClusterRoleBindingSubjectOutput, []bool) {
	out := make([]ClusterRoleBindingSubjectOutput, len(input))
	computed := make([]bool, len(input))
	for i, subject := range input {
		namespace := "default"
		if subject.Namespace != nil {
			namespace = *subject.Namespace
		}
		out[i] = ClusterRoleBindingSubjectOutput{Kind: subject.Kind, Name: subject.Name,
			APIGroup: roleValue(subject.APIGroup), Namespace: namespace}
		if subject.APIGroup == nil {
			if i < len(previous) && previous[i].Kind == subject.Kind && previous[i].Name == subject.Name {
				out[i].APIGroup = previous[i].APIGroup
			} else {
				computed[i] = true
			}
		}
	}
	return out, computed
}

func nativeClusterBindingSubjects(subjects []ClusterRoleBindingSubjectOutput) []rbacv1.Subject {
	out := make([]rbacv1.Subject, len(subjects))
	for i, subject := range subjects {
		out[i] = rbacv1.Subject{Kind: subject.Kind, Name: subject.Name,
			APIGroup: subject.APIGroup, Namespace: subject.Namespace}
	}
	return out
}

func projectClusterBindingSubjects(
	subjects []rbacv1.Subject, previous []ClusterRoleBindingSubjectOutput,
) []ClusterRoleBindingSubjectOutput {
	out := make([]ClusterRoleBindingSubjectOutput, len(subjects))
	for i, subject := range subjects {
		group := subject.APIGroup
		if i < len(previous) && previous[i].Kind == subject.Kind && previous[i].Name == subject.Name &&
			previous[i].APIGroup == "" && (subject.Kind == "User" || subject.Kind == "Group") &&
			group == rbacv1.GroupName {
			group = ""
		}
		out[i] = ClusterRoleBindingSubjectOutput{Kind: subject.Kind, Name: subject.Name,
			APIGroup: group, Namespace: subject.Namespace}
	}
	return out
}

func equalClusterBindingSubjects(before, after []ClusterRoleBindingSubject) bool {
	if len(before) != len(after) {
		return false
	}
	left, _ := clusterBindingSubjects(before, nil)
	right, _ := clusterBindingSubjects(after, left)
	for i := range left {
		if (before[i].APIGroup == nil && after[i].APIGroup != nil) || left[i] != right[i] {
			return false
		}
	}
	return true
}
