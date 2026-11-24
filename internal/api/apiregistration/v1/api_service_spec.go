package v1

import (
	api "k8s.io/kube-aggregator/pkg/apis/apiregistration/v1"
	"k8s.io/utils/ptr"
)

func nativeSpec(input APIServiceSpec) api.APIServiceSpec {
	spec := desiredSpec(input)
	out := api.APIServiceSpec{
		Group: spec.Group, Version: spec.Version,
		GroupPriorityMinimum:  int32(spec.GroupPriorityMinimum),
		VersionPriority:       int32(spec.VersionPriority),
		CABundle:              []byte(spec.CABundle),
		InsecureSkipTLSVerify: spec.InsecureSkipTLSVerify,
	}
	if backend := spec.Service; backend != nil {
		out.Service = &api.ServiceReference{
			Name: backend.Name, Namespace: backend.Namespace,
			Port: new(int32(*backend.Port)),
		}
	}
	return out
}

func desiredSpec(spec APIServiceSpec) APIServiceSpecOutput {
	return effectiveSpec(APIServiceSpecOutput{
		Group: spec.Group, Version: spec.Version,
		GroupPriorityMinimum: spec.GroupPriorityMinimum, VersionPriority: spec.VersionPriority,
		CABundle:              ptr.Deref(spec.CABundle, ""),
		InsecureSkipTLSVerify: ptr.Deref(spec.InsecureSkipTLSVerify, false),
		Service:               spec.Service,
	})
}

func projectSpec(spec api.APIServiceSpec) APIServiceSpecOutput {
	out := APIServiceSpecOutput{
		Group: spec.Group, Version: spec.Version,
		GroupPriorityMinimum:  int64(spec.GroupPriorityMinimum),
		VersionPriority:       int64(spec.VersionPriority),
		CABundle:              string(spec.CABundle),
		InsecureSkipTLSVerify: spec.InsecureSkipTLSVerify,
	}
	if backend := spec.Service; backend != nil {
		out.Service = &APIServiceBackend{Name: backend.Name, Namespace: backend.Namespace}
		if backend.Port != nil {
			out.Service.Port = new(int64(*backend.Port))
		}
	}
	return out
}

func effectiveSpec(spec APIServiceSpecOutput) APIServiceSpecOutput {
	if spec.Service != nil && spec.Service.Port == nil {
		backend := *spec.Service
		backend.Port = new(int64(defaultPort))
		spec.Service = &backend
	}
	return spec
}
