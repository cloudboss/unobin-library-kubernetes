package v1

// EndpointSliceResource manages a namespaced discovery.k8s.io/v1 EndpointSlice.
type EndpointSliceResource struct {
	Metadata          EndpointSliceMetadata
	AddressType       string
	Endpoints         []EndpointSliceEndpoint
	Ports             []EndpointSlicePort
	IgnoreAnnotations *[]string
	IgnoreLabels      *[]string
}

type EndpointSliceMetadata struct {
	Name         *string
	GenerateName *string
	Namespace    *string
	Annotations  *map[string]string
	Labels       *map[string]string
}

type EndpointSliceEndpoint struct {
	Addresses  []string
	Conditions *EndpointSliceConditions
	Hostname   *string
	NodeName   *string
	Zone       *string
	TargetRef  *EndpointSliceReference
}

type EndpointSliceConditions struct {
	Ready       *bool
	Serving     *bool
	Terminating *bool
}

type EndpointSliceReference struct {
	Name            string
	Namespace       *string
	ResourceVersion *string
	UID             *string
	FieldPath       *string
}

type EndpointSlicePort struct {
	Port        string
	Protocol    *string
	Name        *string
	AppProtocol string
}

type EndpointSliceResourceOutput struct {
	Server      string
	APIVersion  string
	Kind        string
	Name        string
	Namespace   string
	UID         string
	Metadata    EndpointSliceMetadata
	AddressType string
	Endpoints   []EndpointSliceEndpointOutput
	Ports       []EndpointSlicePortOutput
}

type EndpointSliceEndpointOutput struct {
	Addresses  []string
	Conditions *EndpointSliceConditions
	Hostname   *string
	NodeName   *string
	Zone       *string
	TargetRef  *EndpointSliceReferenceOutput
}

type EndpointSliceReferenceOutput struct {
	Name            *string
	Namespace       *string
	ResourceVersion *string
	UID             *string
	FieldPath       *string
}

type EndpointSlicePortOutput struct {
	Port        *string
	Protocol    *string
	Name        *string
	AppProtocol *string
}
