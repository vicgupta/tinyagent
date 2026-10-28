package lockdown

import "testing"

func TestContainerProxySetting(t *testing.T) {
	var nilState *State
	nilState.SetContainerProxy(true)
	if nilState.ContainerProxy() {
		t.Error("nil state")
	}
	st, _ := New(nil)
	if st.ContainerProxy() {
		t.Error("on by default")
	}
	st.SetContainerProxy(true)
	if !st.ContainerProxy() {
		t.Error("not recorded")
	}
	st.RefuseCommands()
	if !st.ContainerProxy() {
		t.Error("RefuseCommands dropped the setting")
	}
}
