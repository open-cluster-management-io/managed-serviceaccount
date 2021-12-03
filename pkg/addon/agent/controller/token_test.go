package controller

import (
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestMergeConditions(t *testing.T) {
	cases := []struct {
		name     string
		old      []metav1.Condition
		new      []metav1.Condition
		expected []metav1.Condition
	}{
		{
			name: "fully overwrite",
			old: []metav1.Condition{
				{
					Type:   "foo",
					Status: metav1.ConditionTrue,
				},
				{
					Type:   "bar",
					Status: metav1.ConditionTrue,
				},
			},
			new: []metav1.Condition{
				{
					Type:   "foo",
					Status: metav1.ConditionFalse,
				},
				{
					Type:   "bar",
					Status: metav1.ConditionFalse,
				},
			},
			expected: []metav1.Condition{
				{
					Type:   "foo",
					Status: metav1.ConditionFalse,
				},
				{
					Type:   "bar",
					Status: metav1.ConditionFalse,
				},
			},
		},
		{
			name: "overwrite and append",
			old: []metav1.Condition{
				{
					Type:   "foo",
					Status: metav1.ConditionTrue,
				},
				{
					Type:   "bar",
					Status: metav1.ConditionTrue,
				},
			},
			new: []metav1.Condition{
				{
					Type:   "foo",
					Status: metav1.ConditionFalse,
				},
			},
			expected: []metav1.Condition{
				{
					Type:   "foo",
					Status: metav1.ConditionFalse,
				},
				{
					Type:   "bar",
					Status: metav1.ConditionTrue,
				},
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := mergeConditions(c.old, c.new)
			assert.Equal(t, c.expected, out)
		})
	}

}
