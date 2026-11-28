package v1

import (
	"reflect"

	batchv1 "k8s.io/api/batch/v1"
)

func cronJobLookupOutput(obj *batchv1.CronJob) (*CronJobDataSourceOutput, error) {
	out := &CronJobDataSourceOutput{
		Metadata: CronJobLookupMetadataOutput{},
		Spec:     &CronJobLookupSpec{},
		Status:   &CronJobLookupStatus{},
	}
	for _, pair := range []struct{ dst, src any }{
		{&out.Metadata, &obj.ObjectMeta},
		{out.Spec, &obj.Spec},
		{out.Status, &obj.Status},
	} {
		if err := readJobLookup(reflect.ValueOf(pair.dst).Elem(),
			reflect.ValueOf(pair.src).Elem(), ""); err != nil {
			return nil, err
		}
	}
	return out, nil
}
