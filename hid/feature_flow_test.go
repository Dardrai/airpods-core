package hid

import (
	"errors"
	"reflect"
	"testing"
)

type fakeOps struct {
	pending   bool
	issueErr  error
	signalled bool
	count     uint32
	resultErr error
	calls     []string
}

func (f *fakeOps) issue() (bool, error) {
	f.calls = append(f.calls, "issue")
	return f.pending, f.issueErr
}
func (f *fakeOps) waitEvent(ms uint32) bool {
	if ms != 400 {
		panic("wait must be 400 ms")
	}
	f.calls = append(f.calls, "wait")
	return f.signalled
}
func (f *fakeOps) cancel() { f.calls = append(f.calls, "cancel") }
func (f *fakeOps) result(wait bool) (uint32, error) {
	if wait {
		f.calls = append(f.calls, "result-wait")
	} else {
		f.calls = append(f.calls, "result")
	}
	return f.count, f.resultErr
}

func TestRunOverlapped(t *testing.T) {
	boom := errors.New("boom")
	for _, tc := range []struct {
		name    string
		ops     fakeOps
		writing bool
		want    error
		calls   []string
	}{
		{"completes at once", fakeOps{count: 64}, false, nil, []string{"issue", "result"}},
		{"pending then done", fakeOps{pending: true, signalled: true, count: 64}, false, nil, []string{"issue", "wait", "result"}},
		{"issue fails", fakeOps{issueErr: boom}, false, boom, []string{"issue"}},
		{"timeout cancels and waits", fakeOps{pending: true, resultErr: boom}, false, ErrFeatureTimeout, []string{"issue", "wait", "cancel", "result-wait"}},
		{"result fails", fakeOps{resultErr: boom}, false, boom, []string{"issue", "result"}},
		{"short get", fakeOps{count: 63}, false, errIncomplete, []string{"issue", "result"}},
		{"set ignores count", fakeOps{count: 0}, true, nil, []string{"issue", "result"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ops := tc.ops
			if err := runOverlapped(&ops, tc.writing); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if !reflect.DeepEqual(ops.calls, tc.calls) {
				t.Fatalf("calls = %v, want %v", ops.calls, tc.calls)
			}
		})
	}
	if !errors.Is(ErrFeatureTimeout, ErrTimeout) {
		t.Fatal("ErrFeatureTimeout must match ErrTimeout")
	}
	if ErrFeatureTimeout.Error() != "USB feature transfer exceeded 400 ms" || errIncomplete.Error() != "Incomplete USB feature report" {
		t.Fatal("error text changed")
	}
}
