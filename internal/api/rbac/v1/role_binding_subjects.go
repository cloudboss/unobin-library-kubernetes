package v1

import rbacv1 "k8s.io/api/rbac/v1"

func bindingSubjects(
	input []RoleBindingSubject, previous []RoleBindingSubjectOutput,
) ([]RoleBindingSubjectOutput, []bool) {
	out := make([]RoleBindingSubjectOutput, len(input))
	computed := make([]bool, len(input))
	for i, subject := range input {
		namespace := "default"
		if subject.Namespace != nil {
			namespace = *subject.Namespace
		}
		out[i] = RoleBindingSubjectOutput{Kind: subject.Kind, Name: subject.Name,
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

func nativeBindingSubjects(subjects []RoleBindingSubjectOutput) []rbacv1.Subject {
	out := make([]rbacv1.Subject, len(subjects))
	for i, subject := range subjects {
		out[i] = rbacv1.Subject{Kind: subject.Kind, Name: subject.Name,
			APIGroup: subject.APIGroup, Namespace: subject.Namespace}
	}
	return out
}

func projectBindingSubjects(
	subjects []rbacv1.Subject, previous []RoleBindingSubjectOutput,
) []RoleBindingSubjectOutput {
	out := make([]RoleBindingSubjectOutput, len(subjects))
	for i, subject := range subjects {
		group := subject.APIGroup
		if i < len(previous) && previous[i].Kind == subject.Kind && previous[i].Name == subject.Name &&
			previous[i].APIGroup == "" && (subject.Kind == "User" || subject.Kind == "Group") &&
			group == rbacv1.GroupName {
			group = ""
		}
		out[i] = RoleBindingSubjectOutput{Kind: subject.Kind, Name: subject.Name,
			APIGroup: group, Namespace: subject.Namespace}
	}
	return out
}

func equalBindingSubjects(before, after []RoleBindingSubject) bool {
	if len(before) != len(after) {
		return false
	}
	left, _ := bindingSubjects(before, nil)
	right, _ := bindingSubjects(after, left)
	for i := range left {
		if (before[i].APIGroup == nil && after[i].APIGroup != nil) || left[i] != right[i] {
			return false
		}
	}
	return true
}
