package integtest

import "testing"

func TestExcludeTags(t *testing.T) {
	t.Parallel()
	const unix = "~@unix"
	tests := []struct {
		name    string
		filter  string
		exclude string
		want    string
	}{
		{name: "emptyExclude_filterUnchanged", filter: "@fake", want: "@fake"},
		{name: "emptyFilter_excludeOnly", exclude: unix, want: unix},
		{name: "singleGroup_exclusionAnded", filter: "@fake", exclude: unix, want: "@fake&&~@unix"},
		{name: "andGroup_exclusionAppended", filter: "@fake&&~@slow", exclude: unix, want: "@fake&&~@slow&&~@unix"},
		{name: "orGroups_eachGroupAnded", filter: "@fake,@live-local", exclude: unix, want: "@fake&&~@unix,@live-local&&~@unix"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := excludeTags(tt.filter, tt.exclude)

			if got != tt.want {
				t.Errorf("excludeTags(%q, %q) = %q, want %q", tt.filter, tt.exclude, got, tt.want)
			}
		})
	}
}
