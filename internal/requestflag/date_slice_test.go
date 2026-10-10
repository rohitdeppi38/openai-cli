package requestflag

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v3"
)

func TestDateAndTimeSliceFlags(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		flag   cli.Flag
		values []string
		want   any
	}{
		{"date", &Flag[[]DateValue]{Name: "value"}, []string{"05/15/2023", "2024-01-02"}, []DateValue{"2023-05-15", "2024-01-02"}},
		{"datetime", &Flag[[]DateTimeValue]{Name: "value"}, []string{"2023-05-15 14:30:45", "2024-01-02T03:04:05Z"}, []DateTimeValue{"2023-05-15T14:30:45Z", "2024-01-02T03:04:05Z"}},
		{"time", &Flag[[]TimeValue]{Name: "value"}, []string{"3:04 PM", "16:05"}, []TimeValue{"15:04:00", "16:05:00"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			for _, value := range test.values {
				require.NoError(t, test.flag.Set("value", value))
			}
			require.Equal(t, test.want, test.flag.Get())
			require.Error(t, test.flag.Set("value", "not a date or time"))
			require.Equal(t, test.want, test.flag.Get(), "invalid input must not append a value")
		})
	}
}
